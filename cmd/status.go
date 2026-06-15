package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// ─── Fixed service list ────────────────────────────────────────────────────────

// statusServices is the fixed ordered list of CRENEIN agent stack services.
var statusServices = []string{"agent", "frontend", "mongodb", "influxdb", "redis"}

// ─── Dependency seam ──────────────────────────────────────────────────────────

// statusDeps holds injectable dependencies for the status command.
// The zero value does NOT wire real implementations — use newStatusDepsReal()
// for production and supply a fully-constructed value in tests.
type statusDeps struct {
	// composePs queries running containers via docker compose ps.
	composePs func(ctx context.Context, composeFile string, services []string) ([]dockerx.ContainerState, error)

	// detectAgentVersion probes /health then Docker for the running agent version.
	// Returns (version, source) where source ∈ {"health","image_tag","unknown"}.
	detectAgentVersion func(ctx context.Context) (version, source string)

	// readFile reads a file from the filesystem (for docker-compose.yml).
	readFile func(name string) ([]byte, error)

	// readDir lists directory entries (for /home/* discovery).
	readDir func(path string) ([]string, error)

	// installDir overrides installation directory detection when non-empty.
	installDir string

	// now returns the current time (injectable for deterministic tests).
	now func() time.Time

	// manifestClient fetches the version manifest for update-available info.
	// When nil, a real ManifestClient is constructed. Set to a fake in tests.
	manifestClient release.Client
}

// ─── Compose-file image tag extraction ────────────────────────────────────────

// imageTagFromCompose parses a docker-compose.yml for the `image:` value of the
// named service. Returns "" when not found. The parser is intentionally simple
// (line-by-line) — it does not handle multi-document YAML or anchors.
func imageTagFromCompose(composeContent, service string) string {
	lines := strings.Split(composeContent, "\n")
	inService := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Detect service block: "  <service>:"
		if strings.HasSuffix(trimmed, ":") {
			svcName := strings.TrimSuffix(trimmed, ":")
			inService = svcName == service
			continue
		}
		if inService && strings.HasPrefix(trimmed, "image:") {
			tag := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
			tag = strings.Trim(tag, `"' `)
			return tag
		}
	}
	return ""
}

// mongoInfoFromCompose extracts the mongo image tag and major version string
// from docker-compose.yml content.
// Returns (imageTag, major) where major is e.g. "7.x", "4.x", or "unknown".
func mongoInfoFromCompose(composeContent string) (imageTag, major string) {
	imageTag = imageTagFromCompose(composeContent, "mongodb")
	if imageTag == "" {
		return "", "unknown"
	}
	// Determine major from image tag.
	// Patterns: "mongo:4.4", "mongodb/mongodb-community-server:7.0-ubi8", etc.
	tag := imageTag
	if idx := strings.LastIndex(tag, ":"); idx != -1 {
		tag = tag[idx+1:]
	}
	// Take the first digit(s) before the first dot.
	if dotIdx := strings.Index(tag, "."); dotIdx > 0 {
		maj := tag[:dotIdx]
		if _, err := strconv.Atoi(maj); err == nil {
			return imageTag, maj + ".x"
		}
	}
	return imageTag, "unknown"
}

// ─── Status text → uptime/health parsing ─────────────────────────────────────

// parseUptimeSeconds converts a Docker status string like "Up 3 days",
// "Up 2 minutes", "Up About an hour" to approximate seconds.
// Returns 0 when the string is not in a recognized format.
func parseUptimeSeconds(status string) int64 {
	// Remove health annotation like " (healthy)" or " (unhealthy)".
	if idx := strings.Index(status, " ("); idx != -1 {
		status = status[:idx]
	}
	status = strings.TrimSpace(status)

	if !strings.HasPrefix(strings.ToLower(status), "up ") {
		return 0
	}
	rest := strings.TrimSpace(status[3:])
	rest = strings.ToLower(rest)

	// "About an hour" → 3600
	if strings.Contains(rest, "about an hour") || rest == "an hour" {
		return 3600
	}
	// "Less than a second" → 0
	if strings.Contains(rest, "less than") {
		return 0
	}

	// Tokenize: first token is the count, second is the unit.
	parts := strings.Fields(rest)
	if len(parts) < 2 {
		return 0
	}

	// Handle "about N" prefix.
	offset := 0
	if parts[0] == "about" && len(parts) >= 3 {
		offset = 1
	}

	// Parse count (may be "a" or "an" for 1).
	count := int64(0)
	countStr := parts[offset]
	if countStr == "a" || countStr == "an" {
		count = 1
	} else {
		n, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil {
			return 0
		}
		count = n
	}

	if len(parts) <= offset+1 {
		return 0
	}
	unit := parts[offset+1]
	// Strip trailing 's' for plurals.
	unit = strings.TrimSuffix(unit, "s")

	switch unit {
	case "second":
		return count
	case "minute":
		return count * 60
	case "hour":
		return count * 3600
	case "day":
		return count * 86400
	case "week":
		return count * 7 * 86400
	case "month":
		return count * 30 * 86400
	case "year":
		return count * 365 * 86400
	}
	return 0
}

// parseHealthFromStatus infers health from the Docker status string.
// "Up N days (healthy)" → "healthy", "(unhealthy)" → "unhealthy", else "none".
func parseHealthFromStatus(status string) string {
	lower := strings.ToLower(status)
	if strings.Contains(lower, "(healthy)") {
		return "healthy"
	}
	if strings.Contains(lower, "(unhealthy)") {
		return "unhealthy"
	}
	return "none"
}

// containerStateFromStatus derives the container state enum from the docker
// status string. Recognized prefixes: "Up"→running, "Restarting"→restarting,
// "Created"→created, "Paused"→paused, "Exited"→exited. Falls back to the
// Running bool when no prefix matches.
func containerStateFromStatus(status string, running bool) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.HasPrefix(s, "up"):
		return "running"
	case strings.HasPrefix(s, "restarting"):
		return "restarting"
	case strings.HasPrefix(s, "created"):
		return "created"
	case strings.HasPrefix(s, "paused"):
		return "paused"
	case strings.HasPrefix(s, "exited"):
		return "exited"
	default:
		if running {
			return "running"
		}
		return "exited"
	}
}

// ─── Version detection ────────────────────────────────────────────────────────

// probeAgentHealthVersion sends a GET to url and returns (version, true) when
// /health returns HTTP 200 with a non-empty "version" field.
func probeAgentHealthVersion(ctx context.Context, prober dockerx.HTTPProber, url string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	resp, err := prober.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false
	}
	var h struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &h); err != nil || h.Version == "" {
		return "", false
	}
	return h.Version, true
}

// ─── JSON document shape ──────────────────────────────────────────────────────

// statusJSONDoc is the top-level JSON document for status --json (schema_version 1).
type statusJSONDoc struct {
	SchemaVersion int              `json:"schema_version"`
	Command       string           `json:"command"`
	Timestamp     string           `json:"timestamp"`
	CLIVersion    string           `json:"cli_version"`
	InstallDir    string           `json:"install_dir"`
	Agent         statusAgentInfo  `json:"agent"`
	Mongo         statusMongoInfo  `json:"mongo"`
	Services      []statusSvcEntry `json:"services"`
	// Updates is an additive field (schema_version 1). Best-effort — null when
	// the manifest is unreachable.
	Updates *statusUpdatesInfo `json:"updates,omitempty"`
	// Warnings is an additive field (schema_version 1). It carries non-fatal
	// issues surfaced during collection, e.g. composePs errors. May be null or absent.
	Warnings []string `json:"warnings,omitempty"`
}

// statusUpdatesInfo is the "updates" object in the status JSON doc.
type statusUpdatesInfo struct {
	CLIVersion           string  `json:"cli_version"`
	CLILatest            string  `json:"cli_latest"`
	CLIUpdateAvailable   bool    `json:"cli_update_available"`
	AgentVersion         string  `json:"agent_version"`
	AgentLatest          string  `json:"agent_latest"`
	AgentUpdateAvailable bool    `json:"agent_update_available"`
	LastChecked          *string `json:"last_checked"`
}

type statusAgentInfo struct {
	Version       string `json:"version"`
	VersionSource string `json:"version_source"`
	Image         string `json:"image"`
	Health        string `json:"health"`
}

type statusMongoInfo struct {
	Image string `json:"image"`
	Major string `json:"major"`
}

type statusSvcEntry struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	State         string `json:"state"`
	Health        string `json:"health"`
	StatusText    string `json:"status_text"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// ─── Core status logic ────────────────────────────────────────────────────────

// collectStatus queries compose + version and builds the JSON doc plus a flag
// indicating whether the stack is fully running, and an optional warning string
// describing any non-fatal collection error (e.g. composePs failure).
// stderr is used for best-effort manifest warnings only.
func collectStatus(ctx context.Context, deps statusDeps, installDir string, stderr io.Writer) (statusJSONDoc, bool, string) {
	nowFn := deps.now
	if nowFn == nil {
		nowFn = time.Now
	}

	cliVersion := build.version
	if cliVersion == "" {
		cliVersion = "dev"
	}

	// Read compose file for image tags / mongo info.
	composeFile := installDir + "/docker-compose.yml"
	var composeContent string
	if data, err := deps.readFile(composeFile); err == nil {
		composeContent = string(data)
	}

	mongoImage, mongoMajor := mongoInfoFromCompose(composeContent)

	// Query running containers.
	containersByService := make(map[string]dockerx.ContainerState)
	var composePsWarning string
	if deps.composePs != nil {
		containers, err := deps.composePs(ctx, composeFile, statusServices)
		if err != nil {
			composePsWarning = fmt.Sprintf("could not query containers: %v", err)
		} else {
			for _, c := range containers {
				containersByService[c.Service] = c
			}
		}
	}

	// Detect agent version.
	agentVersion, versionSource := "unknown", "unknown"
	if deps.detectAgentVersion != nil {
		agentVersion, versionSource = deps.detectAgentVersion(ctx)
	}

	// Build services slice.
	services := make([]statusSvcEntry, 0, len(statusServices))
	allRunning := true
	for _, svcName := range statusServices {
		c, present := containersByService[svcName]

		var entry statusSvcEntry
		entry.Name = svcName

		// Image tag: prefer compose file, fallback to ImageID.
		imageFromCompose := imageTagFromCompose(composeContent, svcName)
		if imageFromCompose != "" {
			entry.Image = imageFromCompose
		} else if present {
			entry.Image = c.ImageID
		} else {
			entry.Image = ""
		}

		if !present {
			entry.State = "missing"
			entry.Health = "none"
			entry.StatusText = ""
			entry.UptimeSeconds = 0
			allRunning = false
		} else {
			entry.State = containerStateFromStatus(c.Status, c.Running)
			entry.Health = parseHealthFromStatus(c.Status)
			entry.StatusText = c.Status
			if entry.State == "running" {
				entry.UptimeSeconds = parseUptimeSeconds(c.Status)
			} else {
				entry.UptimeSeconds = 0
				allRunning = false
			}
		}

		// Health that is "unhealthy" also counts as degraded.
		if entry.Health == "unhealthy" {
			allRunning = false
		}

		services = append(services, entry)
	}

	// Agent-specific image + health from container info.
	agentImage := imageTagFromCompose(composeContent, "agent")
	// agent.health enum: healthy|unhealthy|unknown (distinct from services[].health which uses "none").
	agentHealth := "unknown"
	if c, ok := containersByService["agent"]; ok {
		if agentImage == "" {
			agentImage = c.ImageID
		}
		h := parseHealthFromStatus(c.Status)
		if h == "none" {
			// Map "none" (no healthcheck annotation) to "unknown" for the agent-level field.
			h = "unknown"
		}
		agentHealth = h
	}

	var docWarnings []string
	if composePsWarning != "" {
		docWarnings = []string{composePsWarning}
	}

	doc := statusJSONDoc{
		SchemaVersion: 1,
		Command:       "status",
		Timestamp:     nowFn().UTC().Format(time.RFC3339),
		CLIVersion:    cliVersion,
		InstallDir:    installDir,
		Agent: statusAgentInfo{
			Version:       agentVersion,
			VersionSource: versionSource,
			Image:         agentImage,
			Health:        agentHealth,
		},
		Mongo: statusMongoInfo{
			Image: mongoImage,
			Major: mongoMajor,
		},
		Services: services,
		Warnings: docWarnings,
	}

	// Best-effort update info via manifest (24h cache respected; never fails status).
	if deps.manifestClient != nil {
		doc.Updates = fetchUpdatesInfo(ctx, deps.manifestClient, cliVersion, agentVersion, stderr)
	}

	return doc, allRunning, composePsWarning
}

// fetchUpdatesInfo fetches the version manifest (cache-first) and returns the
// update availability info. Returns nil on any manifest error — update info is
// best-effort and must never cause status to fail.
func fetchUpdatesInfo(ctx context.Context, mc release.Client, cliVersion, agentVersion string, stderr io.Writer) *statusUpdatesInfo {
	m, fetchErr := mc.FetchManifest(ctx, false)
	if fetchErr != nil {
		fmt.Fprintf(stderr, "warning: could not fetch version manifest: %v\n", fetchErr)
		return &statusUpdatesInfo{
			CLIVersion:           cliVersion,
			CLILatest:            "",
			CLIUpdateAvailable:   false,
			AgentVersion:         agentVersion,
			AgentLatest:          "",
			AgentUpdateAvailable: false,
			LastChecked:          nil,
		}
	}

	// dev build: never report CLI update available.
	localCLI := cliVersion
	if localCLI == "dev" || localCLI == "" {
		localCLI = "" // ComputeUpdateInfo treats "" as unknown → UpdateUnknown
	}

	info := release.ComputeUpdateInfo(m, localCLI, agentVersion)

	var lastCheckedStr *string
	if !m.FetchedAt.IsZero() {
		s := m.FetchedAt.UTC().Format(time.RFC3339)
		lastCheckedStr = &s
	}

	return &statusUpdatesInfo{
		CLIVersion:           cliVersion,
		CLILatest:            info.CLILatest,
		CLIUpdateAvailable:   info.CLIStatus == release.UpdateAvailable,
		AgentVersion:         agentVersion,
		AgentLatest:          info.AgentLatest,
		AgentUpdateAvailable: info.AgentStatus == release.UpdateAvailable,
		LastChecked:          lastCheckedStr,
	}
}

// ─── Human renderer ───────────────────────────────────────────────────────────

// renderStatusHuman writes the human-readable status output to w.
func renderStatusHuman(w io.Writer, doc statusJSONDoc) {
	fmt.Fprintf(w, "Install dir:   %s\n", doc.InstallDir)                                          //nolint:errcheck
	fmt.Fprintf(w, "Agent version: %s (source: %s)\n", doc.Agent.Version, doc.Agent.VersionSource) //nolint:errcheck
	fmt.Fprintf(w, "Mongo:         %s (major: %s)\n", doc.Mongo.Image, doc.Mongo.Major)            //nolint:errcheck

	if doc.Updates != nil {
		fmt.Fprintln(w) //nolint:errcheck
		renderUpdateLine(w, "CLI", doc.Updates.CLIVersion, doc.Updates.CLILatest, doc.Updates.CLIUpdateAvailable)
		renderUpdateLine(w, "Agent", doc.Updates.AgentVersion, doc.Updates.AgentLatest, doc.Updates.AgentUpdateAvailable)
	}

	fmt.Fprintln(w) //nolint:errcheck

	// Aligned service table.
	const hdrFmt = "%-12s  %-40s  %-9s  %-11s  %s\n"
	const rowFmt = "%-12s  %-40s  %-9s  %-11s  %s\n"
	fmt.Fprintf(w, hdrFmt, "SERVICE", "IMAGE", "STATE", "HEALTH", "UPTIME") //nolint:errcheck
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 100))                        //nolint:errcheck
	for _, svc := range doc.Services {
		uptime := ""
		if svc.UptimeSeconds > 0 {
			uptime = formatUptimeHuman(svc.UptimeSeconds)
		} else if svc.StatusText != "" {
			uptime = svc.StatusText
		}
		fmt.Fprintf(w, rowFmt, svc.Name, svc.Image, svc.State, svc.Health, uptime) //nolint:errcheck
	}
}

// renderUpdateLine writes one update-available line for a component.
// Format: "<label>: <version> (<update available: <latest>> | up to date | unknown)"
func renderUpdateLine(w io.Writer, label, version, latest string, updateAvailable bool) {
	if updateAvailable && latest != "" {
		fmt.Fprintf(w, "%-6s %s (update available: %s)\n", label+":", version, latest) //nolint:errcheck
	} else if latest == "" || latest == "unknown" {
		fmt.Fprintf(w, "%-6s %s (update status: unknown)\n", label+":", version) //nolint:errcheck
	} else {
		fmt.Fprintf(w, "%-6s %s (up to date)\n", label+":", version) //nolint:errcheck
	}
}

// formatUptimeHuman converts seconds to a human string like "3d 2h 5m".
func formatUptimeHuman(secs int64) string {
	days := secs / 86400
	secs %= 86400
	hours := secs / 3600
	secs %= 3600
	mins := secs / 60

	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if len(parts) == 0 {
		return "< 1m"
	}
	return strings.Join(parts, " ")
}

// ─── Real deps constructor ────────────────────────────────────────────────────

// newStatusDepsReal constructs real-production statusDeps.
func newStatusDepsReal() statusDeps {
	fs := dockerx.NewOSFS()
	runner := dockerx.NewOSCommandRunner()

	// composeClient resolves the CLI client with the detected compose variant
	// (v1 vs v2). Client VMs may run compose v1; hardcoding v2 would make every
	// service appear "missing" there. Falls back to v2 when detection fails.
	composeClient := func(ctx context.Context) dockerx.Client {
		variant := dockerx.ComposeV2
		if info, err := detect.Compose(ctx, runner); err == nil {
			variant = info.Variant
		}
		return dockerx.NewCLIClient(variant)
	}

	// Build an insecure HTTP prober for /health probing (same TLS skip as doctor).
	insecureTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	insecureClient := &http.Client{Timeout: 5 * time.Second, Transport: insecureTransport}
	prober := dockerx.NewHTTPProber(insecureClient)

	detectVersionFn := func(ctx context.Context) (string, string) {
		// Step 1 & 2: probe /health for version.
		for _, url := range []string{
			"https://localhost:8000/health",
			"http://localhost:8000/health",
		} {
			if ver, ok := probeAgentHealthVersion(ctx, prober, url); ok {
				return ver, "health"
			}
		}

		// Step 3: inspect running containers for agent image tag.
		cliClient := composeClient(ctx)
		containers, err := cliClient.ContainerList(ctx, "agent")
		if err == nil {
			for _, c := range containers {
				info, err := cliClient.ImageInspect(ctx, c.ImageID)
				if err != nil {
					continue
				}
				const pfx = "crenein/c-network-agent-back:"
				for _, tag := range info.RepoTags {
					if strings.HasPrefix(tag, pfx) {
						suffix := strings.TrimPrefix(tag, pfx)
						if suffix != "" && suffix != "latest" {
							return suffix, "image_tag"
						}
					}
				}
			}
		}
		return "unknown", "unknown"
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/root"
	}
	mc := release.NewManifestClient(prober, nil, fs, homeDir, time.Now)

	return statusDeps{
		composePs: func(ctx context.Context, composeFile string, services []string) ([]dockerx.ContainerState, error) {
			return composeClient(ctx).ComposePs(ctx, composeFile, services)
		},
		detectAgentVersion: detectVersionFn,
		readFile:           fs.ReadFile,
		readDir:            fs.ReadDir,
		now:                time.Now,
		manifestClient:     mc,
	}
}

// ─── Command constructor ──────────────────────────────────────────────────────

// newStatusCmd constructs the `status` subcommand wired to real deps.
func newStatusCmd() *cobra.Command {
	return newStatusCmdWithDeps(newStatusDepsReal())
}

// newStatusCmdWithDeps constructs the `status` subcommand with injectable deps.
func newStatusCmdWithDeps(deps statusDeps) *cobra.Command {
	var flagJSON bool

	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show the running status of the CRENEIN agent stack",
		Long:          "Queries the agent stack and reports the state of all services.\nReturns exit code 0 when all services are running, 1 when degraded, 3 when no installation is found.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runStatus(ctx, cmd, deps, flagJSON)
		},
	}

	cmd.Flags().BoolVar(&flagJSON, "json", false, "emit machine-readable JSON to stdout")
	return cmd
}

// runStatus executes the status logic and renders output in human or JSON mode.
func runStatus(
	ctx context.Context,
	cmd *cobra.Command,
	deps statusDeps,
	jsonMode bool,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// Detect install dir.
	installDir := resolveInstallDir(deps.readFile, deps.readDir, deps.installDir)
	if installDir == "" {
		WriteError(stderr, "no CRENEIN installation found (no docker-compose.yml referencing crenein/c-network-agent-back in . or /root or /home/*/)\n")
		WriteError(stderr, "hint: run `crenein-agent install` to set up the agent stack\n")
		return preflightError(fmt.Errorf("no installation found"))
	}

	// Collect status.
	doc, allRunning, collectionWarning := collectStatus(ctx, deps, installDir, stderr)

	// Surface any non-fatal collection warning to stderr before emitting output.
	if collectionWarning != "" {
		WriteError(stderr, "warning: %s\n", collectionWarning)
	}

	// Render.
	if jsonMode {
		mp := NewMachinePresenter(stdout, stderr)
		if err := mp.EmitJSON(doc); err != nil {
			mp.WriteError("error: failed to encode JSON: " + err.Error())
			return opFailureError(err)
		}
	} else {
		renderStatusHuman(stdout, doc)
	}

	if !allRunning {
		return opFailureError(fmt.Errorf("one or more services are not running or are unhealthy"))
	}
	return nil
}
