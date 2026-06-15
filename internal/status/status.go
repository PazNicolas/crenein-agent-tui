// Package status provides the domain types and collection logic for the
// crenein-agent status command. It is intentionally neutral — it imports no
// cmd package — so both the CLI (cmd/status.go) and the TUI dashboard can
// consume it without import cycles.
package status

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

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// ─── Fixed service list ────────────────────────────────────────────────────────

// Services is the fixed ordered list of CRENEIN agent stack services.
var Services = []string{"agent", "frontend", "mongodb", "influxdb", "redis"}

// ─── Dependency seam ──────────────────────────────────────────────────────────

// Deps holds injectable dependencies for status collection.
// The zero value does NOT wire real implementations — use NewDepsReal()
// for production and supply a fully-constructed value in tests.
type Deps struct {
	// ComposePs queries running containers via docker compose ps.
	ComposePs func(ctx context.Context, composeFile string, services []string) ([]dockerx.ContainerState, error)

	// DetectAgentVersion probes /health then Docker for the running agent version.
	// Returns (version, source) where source ∈ {"health","image_tag","unknown"}.
	DetectAgentVersion func(ctx context.Context) (version, source string)

	// ReadFile reads a file from the filesystem (for docker-compose.yml).
	ReadFile func(name string) ([]byte, error)

	// ReadDir lists directory entries (for /home/* discovery).
	ReadDir func(path string) ([]string, error)

	// InstallDir overrides installation directory detection when non-empty.
	InstallDir string

	// Now returns the current time (injectable for deterministic tests).
	Now func() time.Time

	// ManifestClient fetches the version manifest for update-available info.
	// When nil, update info is skipped. Set to a fake in tests.
	ManifestClient release.Client
}

// ─── JSON document shape ──────────────────────────────────────────────────────

// Doc is the top-level JSON document for status --json (schema_version 1).
type Doc struct {
	SchemaVersion int        `json:"schema_version"`
	Command       string     `json:"command"`
	Timestamp     string     `json:"timestamp"`
	CLIVersion    string     `json:"cli_version"`
	InstallDir    string     `json:"install_dir"`
	Agent         AgentInfo  `json:"agent"`
	Mongo         MongoInfo  `json:"mongo"`
	Services      []SvcEntry `json:"services"`
	// Updates is an additive field (schema_version 1). Best-effort — null when
	// the manifest is unreachable.
	Updates *UpdatesInfo `json:"updates,omitempty"`
	// Warnings is an additive field (schema_version 1). It carries non-fatal
	// issues surfaced during collection, e.g. ComposePs errors. May be null or absent.
	Warnings []string `json:"warnings,omitempty"`
}

// UpdatesInfo is the "updates" object in the status JSON doc.
type UpdatesInfo struct {
	CLIVersion           string  `json:"cli_version"`
	CLILatest            string  `json:"cli_latest"`
	CLIUpdateAvailable   bool    `json:"cli_update_available"`
	AgentVersion         string  `json:"agent_version"`
	AgentLatest          string  `json:"agent_latest"`
	AgentUpdateAvailable bool    `json:"agent_update_available"`
	LastChecked          *string `json:"last_checked"`
}

// AgentInfo holds agent version and health information in the JSON doc.
type AgentInfo struct {
	Version       string `json:"version"`
	VersionSource string `json:"version_source"`
	Image         string `json:"image"`
	Health        string `json:"health"`
}

// MongoInfo holds MongoDB image and major version information in the JSON doc.
type MongoInfo struct {
	Image string `json:"image"`
	Major string `json:"major"`
}

// SvcEntry holds per-service state in the JSON doc.
type SvcEntry struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	State         string `json:"state"`
	Health        string `json:"health"`
	StatusText    string `json:"status_text"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// ─── Compose-file image tag extraction ────────────────────────────────────────

// ImageTagFromCompose parses a docker-compose.yml for the `image:` value of the
// named service. Returns "" when not found. The parser is intentionally simple
// (line-by-line) — it does not handle multi-document YAML or anchors.
func ImageTagFromCompose(composeContent, service string) string {
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

// MongoInfoFromCompose extracts the mongo image tag and major version string
// from docker-compose.yml content.
// Returns (imageTag, major) where major is e.g. "7.x", "4.x", or "unknown".
func MongoInfoFromCompose(composeContent string) (imageTag, major string) {
	imageTag = ImageTagFromCompose(composeContent, "mongodb")
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

// ParseUptimeSeconds converts a Docker status string like "Up 3 days",
// "Up 2 minutes", "Up About an hour" to approximate seconds.
// Returns 0 when the string is not in a recognized format.
func ParseUptimeSeconds(status string) int64 {
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

// ParseHealthFromStatus infers health from the Docker status string.
// "Up N days (healthy)" → "healthy", "(unhealthy)" → "unhealthy", else "none".
func ParseHealthFromStatus(status string) string {
	lower := strings.ToLower(status)
	if strings.Contains(lower, "(healthy)") {
		return "healthy"
	}
	if strings.Contains(lower, "(unhealthy)") {
		return "unhealthy"
	}
	return "none"
}

// ContainerStateFromStatus derives the container state enum from the docker
// status string. Recognized prefixes: "Up"→running, "Restarting"→restarting,
// "Created"→created, "Paused"→paused, "Exited"→exited. Falls back to the
// Running bool when no prefix matches.
func ContainerStateFromStatus(status string, running bool) string {
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

// ProbeAgentHealthVersion sends a GET to url and returns (version, true) when
// /health returns HTTP 200 with a non-empty "version" field.
func ProbeAgentHealthVersion(ctx context.Context, prober dockerx.HTTPProber, url string) (string, bool) {
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

// ─── Core status collection ───────────────────────────────────────────────────

// Collect queries compose + version and builds the JSON doc plus a flag
// indicating whether the stack is fully running, and an optional warning string
// describing any non-fatal collection error (e.g. ComposePs failure).
//
// cliVersion must be resolved by the caller (e.g. build.version or "dev" when
// empty). stderr is used for best-effort manifest warnings only.
func Collect(ctx context.Context, deps Deps, cliVersion, installDir string, stderr io.Writer) (Doc, bool, string) {
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	// Read compose file for image tags / mongo info.
	composeFile := installDir + "/docker-compose.yml"
	var composeContent string
	if data, err := deps.ReadFile(composeFile); err == nil {
		composeContent = string(data)
	}

	mongoImage, mongoMajor := MongoInfoFromCompose(composeContent)

	// Query running containers.
	containersByService := make(map[string]dockerx.ContainerState)
	var composePsWarning string
	if deps.ComposePs != nil {
		containers, err := deps.ComposePs(ctx, composeFile, Services)
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
	if deps.DetectAgentVersion != nil {
		agentVersion, versionSource = deps.DetectAgentVersion(ctx)
	}

	// Build services slice.
	services := make([]SvcEntry, 0, len(Services))
	allRunning := true
	for _, svcName := range Services {
		c, present := containersByService[svcName]

		var entry SvcEntry
		entry.Name = svcName

		// Image tag: prefer compose file, fallback to ImageID.
		imageFromCompose := ImageTagFromCompose(composeContent, svcName)
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
			entry.State = ContainerStateFromStatus(c.Status, c.Running)
			entry.Health = ParseHealthFromStatus(c.Status)
			entry.StatusText = c.Status
			if entry.State == "running" {
				entry.UptimeSeconds = ParseUptimeSeconds(c.Status)
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
	agentImage := ImageTagFromCompose(composeContent, "agent")
	// agent.health enum: healthy|unhealthy|unknown (distinct from services[].health which uses "none").
	agentHealth := "unknown"
	if c, ok := containersByService["agent"]; ok {
		if agentImage == "" {
			agentImage = c.ImageID
		}
		h := ParseHealthFromStatus(c.Status)
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

	doc := Doc{
		SchemaVersion: 1,
		Command:       "status",
		Timestamp:     nowFn().UTC().Format(time.RFC3339),
		CLIVersion:    cliVersion,
		InstallDir:    installDir,
		Agent: AgentInfo{
			Version:       agentVersion,
			VersionSource: versionSource,
			Image:         agentImage,
			Health:        agentHealth,
		},
		Mongo: MongoInfo{
			Image: mongoImage,
			Major: mongoMajor,
		},
		Services: services,
		Warnings: docWarnings,
	}

	// Best-effort update info via manifest (24h cache respected; never fails status).
	if deps.ManifestClient != nil {
		doc.Updates = FetchUpdatesInfo(ctx, deps.ManifestClient, cliVersion, agentVersion, stderr)
	}

	return doc, allRunning, composePsWarning
}

// FetchUpdatesInfo fetches the version manifest (cache-first) and returns the
// update availability info. Returns nil on any manifest error — update info is
// best-effort and must never cause status to fail.
func FetchUpdatesInfo(ctx context.Context, mc release.Client, cliVersion, agentVersion string, stderr io.Writer) *UpdatesInfo {
	m, fetchErr := mc.FetchManifest(ctx, false)
	if fetchErr != nil {
		fmt.Fprintf(stderr, "warning: could not fetch version manifest: %v\n", fetchErr) //nolint:errcheck
		return &UpdatesInfo{
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

	return &UpdatesInfo{
		CLIVersion:           cliVersion,
		CLILatest:            info.CLILatest,
		CLIUpdateAvailable:   info.CLIStatus == release.UpdateAvailable,
		AgentVersion:         agentVersion,
		AgentLatest:          info.AgentLatest,
		AgentUpdateAvailable: info.AgentStatus == release.UpdateAvailable,
		LastChecked:          lastCheckedStr,
	}
}

// ─── Install-dir resolution ───────────────────────────────────────────────────

// ResolveInstallDir searches CWD, /root, and /home/*/ for a docker-compose.yml
// that references the CRENEIN agent image. Returns "" when not found.
//
// If override is non-empty it is returned directly (used by tests and the
// per-command --dir override). This is the single shared implementation used by
// both cmd/ (status, logs, rollback) and the TUI dashboard; readFile/readDir
// are injected so the resolution is hermetic in tests.
func ResolveInstallDir(
	readFile func(name string) ([]byte, error),
	readDir func(path string) ([]string, error),
	override string,
) string {
	if override != "" {
		return override
	}

	candidates := []string{".", "/root"}
	if entries, err := readDir("/home"); err == nil {
		for _, entry := range entries {
			candidates = append(candidates, "/home/"+entry)
		}
	}

	for _, dir := range candidates {
		data, err := readFile(dir + "/docker-compose.yml")
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "crenein/c-network-agent-back") ||
			strings.Contains(content, "c-network-agent-back") {
			return dir
		}
	}
	return ""
}

// ─── Real deps constructor ────────────────────────────────────────────────────

// NewDepsReal constructs real-production Deps.
func NewDepsReal() Deps {
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
			if ver, ok := ProbeAgentHealthVersion(ctx, prober, url); ok {
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

	return Deps{
		ComposePs: func(ctx context.Context, composeFile string, services []string) ([]dockerx.ContainerState, error) {
			return composeClient(ctx).ComposePs(ctx, composeFile, services)
		},
		DetectAgentVersion: detectVersionFn,
		ReadFile:           fs.ReadFile,
		ReadDir:            fs.ReadDir,
		Now:                time.Now,
		ManifestClient:     mc,
	}
}
