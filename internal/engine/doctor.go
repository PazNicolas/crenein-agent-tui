// Package engine — doctor sub-engine (tasks 6.1–6.5).
//
// Design (AD-1): Doctor never prints, prompts, or reads stdin. It returns a
// typed DoctorReport that consumers (TUI, CLI, --json) render as needed.
//
// Design (AD-2): all external effects go through deps.Client / Runner / FS /
// Prober. No exec.Command or os.ReadFile calls.
//
// Design (AD-7): Doctor returns a DoctorReport, not formatted text.
//
// Design (AD-8): Structured errors with fix suggestions for every non-OK check.
//
// Design (6.5): Doctor is read-only and degrades gracefully — every check runs
// even when earlier checks fail; unexpected errors become CRITICAL entries.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
)

// ─── 6.1 Types ───────────────────────────────────────────────────────────────

// Status is the severity of a single doctor check result.
type Status string

const (
	// StatusOK means the check passed with no issues.
	StatusOK Status = "OK"
	// StatusWarning means a potential issue was found that does not prevent
	// operation but should be reviewed.
	StatusWarning Status = "WARNING"
	// StatusCritical means a blocking issue was found that requires immediate
	// remediation.
	StatusCritical Status = "CRITICAL"
	// StatusSkip means the check was skipped because a prerequisite check failed.
	// It does NOT elevate the summary status.
	StatusSkip Status = "SKIP"
)

// severity returns a numeric weight for ordering: CRITICAL > WARNING > OK > SKIP.
// Higher values are more severe. SKIP is 0 (same as OK) so it never elevates summary.
func (s Status) severity() int {
	switch s {
	case StatusCritical:
		return 2
	case StatusWarning:
		return 1
	default:
		// OK and SKIP are both 0 — SKIP does not elevate the summary.
		return 0
	}
}

// worse returns the more severe of s and other.
func (s Status) worse(other Status) Status {
	if other.severity() > s.severity() {
		return other
	}
	return s
}

// Check is the result of a single doctor check. JSON-serializable for the
// --json flag (AD-7).
type Check struct {
	// ID is a stable, machine-readable identifier (e.g. "docker.installed").
	ID string `json:"id"`
	// Name is a human-readable label (e.g. "Docker installed").
	Name string `json:"name"`
	// Status is OK, WARNING, CRITICAL, or SKIP.
	Status Status `json:"status"`
	// Severity is the weight of this check if it fails: StatusCritical or StatusWarning.
	// It is always set, regardless of whether the check passes.
	Severity Status `json:"severity"`
	// Detail is a human-readable explanation of the check result.
	Detail string `json:"detail"`
	// FixSuggestion is a concrete, actionable command or instruction for any
	// non-OK status. Empty for OK checks.
	FixSuggestion string `json:"fix_suggestion,omitempty"`
	// DurationMs is the elapsed time in milliseconds for this check.
	DurationMs int64 `json:"duration_ms"`
}

// DoctorReport is the structured output of Run. JSON-serializable (AD-7).
type DoctorReport struct {
	// Checks is the ordered list of individual check results.
	Checks []Check `json:"checks"`
	// Summary is the worst individual status across all checks. OK when empty.
	// SKIP never elevates the summary.
	Summary Status `json:"summary"`
}

// DoctorOptions carries injectable seams for tests (mirrors the pattern used
// by InstallOptions and UpdateOptions).
type DoctorOptions struct {
	// InstallDir overrides the directory searched for docker-compose.yml and
	// .env. When empty the engine searches the standard locations (CWD, /root,
	// /home/*).
	InstallDir string

	// DiskSpaceProvider overrides the disk-space syscall for tests.
	DiskSpaceProvider detect.DiskSpaceProvider
}

// ─── Run entry-point ─────────────────────────────────────────────────────────

// Run performs all doctor checks and returns a DoctorReport. It never aborts
// early — every check runs regardless of what earlier checks returned (6.5).
// It never modifies any file, container, image, service, or configuration.
func Run(ctx context.Context, deps Deps, opts DoctorOptions) DoctorReport {
	report := DoctorReport{Summary: StatusOK}

	// Helper that appends a check and updates the summary (SKIP never elevates).
	add := func(c Check) {
		report.Checks = append(report.Checks, c)
		report.Summary = report.Summary.worse(c.Status)
	}

	// ── 6.2 Host checks ─────────────────────────────────────────────────────
	dockerInstalled := checkDockerInstalled(ctx, deps)
	add(dockerInstalled)

	dockerDaemon := checkDockerDaemon(ctx, deps)
	add(dockerDaemon)

	// Skip graph: if docker.installed fails, skip everything requiring docker.
	dockerInstalledFailed := dockerInstalled.Status == StatusCritical
	// If docker.daemon fails, skip compose, connectivity, stack, logs, agent.health, avx_mongo.
	dockerDaemonFailed := dockerDaemon.Status == StatusCritical

	// docker.compose is part of the Docker toolchain, so it is skipped when
	// Docker itself is unavailable.
	if dockerInstalledFailed || dockerDaemonFailed {
		add(skipCheck("docker.compose", "Compose available", StatusCritical))
	} else {
		add(checkCompose(ctx, deps))
	}

	// Network connectivity checks are pure HTTP probes that do NOT require
	// Docker, so they always run — knowing whether the host has connectivity is
	// useful precisely when Docker is broken (e.g. to explain a failed pull).
	add(checkConnectivityDockerHub(ctx, deps))
	add(checkConnectivityCrenein(ctx, deps))

	add(checkDiskSpace(ctx, deps, opts))

	// ── 6.3 Permission checks ────────────────────────────────────────────────
	installDir := resolveInstallDir(ctx, deps, opts)
	hasInstall := installDir != ""

	add(checkEnvPermission(ctx, deps, installDir))

	if hasInstall {
		add(checkComposeReadable(ctx, deps, installDir))
		add(checkCertKeyModes(ctx, deps, installDir))
	} else {
		add(skipCheck("files.compose_readable", "docker-compose.yml readable", StatusCritical))
		add(skipCheck("files.cert_modes", "Certificate file permissions", StatusCritical))
	}

	add(checkDockerSocket(ctx, deps))

	// ── 6.4 Stack checks ────────────────────────────────────────────────────
	// Skip stack checks when no installation or docker prerequisites failed.
	if !hasInstall || dockerInstalledFailed || dockerDaemonFailed {
		add(skipCheck("services.running", "Agent stack service states", StatusCritical))
		add(skipCheck("logs.recent_errors", "Recent log errors", StatusWarning))
		add(skipCheck("agent.health", "Agent /health endpoint", StatusCritical))
		add(skipCheck("cpu.avx_mongo", "CPU AVX / MongoDB compatibility", StatusCritical))
	} else {
		add(checkStackServices(ctx, deps, installDir))
		add(checkServiceLogs(ctx, deps, installDir))
		add(checkAgentHealth(ctx, deps))
		add(checkAVXMongo(ctx, deps, installDir))
	}

	return report
}

// skipCheck creates a SKIP check with the given id, name, and severity.
func skipCheck(id, name string, severity Status) Check {
	return Check{
		ID:       id,
		Name:     name,
		Status:   StatusSkip,
		Severity: severity,
		Detail:   "skipped: prerequisite check failed",
	}
}

// safeCheck wraps a check function so that any unexpected panic or unhandled
// error becomes a CRITICAL entry instead of aborting the run (6.5 guarantee).
// It also measures the elapsed time and sets DurationMs.
func safeCheck(id, name string, severity Status, fn func() Check) (result Check) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			result = Check{
				ID:            id,
				Name:          name,
				Status:        StatusCritical,
				Severity:      severity,
				Detail:        fmt.Sprintf("unexpected error in check: %v", r),
				FixSuggestion: "report this as a bug",
				DurationMs:    time.Since(start).Milliseconds(),
			}
			return
		}
		result.DurationMs = time.Since(start).Milliseconds()
	}()
	result = fn()
	return result
}

// ─── 6.2 Host checks ─────────────────────────────────────────────────────────

func checkDockerInstalled(ctx context.Context, deps Deps) Check {
	return safeCheck("docker.installed", "Docker installed", StatusCritical, func() Check {
		info := detect.Docker(ctx, deps.Runner, deps.Client)
		if !info.Installed {
			return Check{
				ID:            "docker.installed",
				Name:          "Docker installed",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "docker binary not found in PATH",
				FixSuggestion: "install Docker: apt-get install docker-ce docker-ce-cli containerd.io docker-compose-plugin",
			}
		}
		return Check{
			ID:       "docker.installed",
			Name:     "Docker installed",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "docker binary found",
		}
	})
}

func checkDockerDaemon(ctx context.Context, deps Deps) Check {
	return safeCheck("docker.daemon", "Docker daemon running", StatusCritical, func() Check {
		if err := deps.Client.Ping(ctx); err != nil {
			detail := "Docker daemon is not responding"
			fix := "start the daemon: systemctl start docker"
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "permission denied") || strings.Contains(msg, "access denied") {
				detail = "Docker daemon is not accessible: permission denied on socket"
				fix = "add user to docker group: sudo usermod -aG docker $USER (then log out and back in) — or run with sudo"
			}
			return Check{
				ID:            "docker.daemon",
				Name:          "Docker daemon running",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        detail,
				FixSuggestion: fix,
			}
		}
		return Check{
			ID:       "docker.daemon",
			Name:     "Docker daemon running",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "daemon responded to ping",
		}
	})
}

func checkCompose(ctx context.Context, deps Deps) Check {
	return safeCheck("docker.compose", "Compose available", StatusCritical, func() Check {
		info, err := detect.Compose(ctx, deps.Runner)
		if err != nil {
			return Check{
				ID:            "docker.compose",
				Name:          "Compose available",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "no compose variant found (neither docker compose v2 nor docker-compose v1)",
				FixSuggestion: "install compose v2 plugin: apt-get install docker-compose-plugin",
			}
		}
		if info.V1Available && !info.V2Available {
			return Check{
				ID:            "docker.compose",
				Name:          "Compose available",
				Status:        StatusWarning,
				Severity:      StatusCritical,
				Detail:        fmt.Sprintf("compose v1 in use (%s); v2 is preferred", info.Version),
				FixSuggestion: "upgrade to compose v2: apt-get install docker-compose-plugin",
			}
		}
		return Check{
			ID:       "docker.compose",
			Name:     "Compose available",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   fmt.Sprintf("compose v2 available (%s)", info.Version),
		}
	})
}

func checkConnectivityDockerHub(ctx context.Context, deps Deps) Check {
	return safeCheck("net.dockerhub", "Docker Hub connectivity", StatusWarning, func() Check {
		urls := []string{
			"https://registry-1.docker.io/v2/",
			"https://hub.docker.com",
		}
		results, err := detect.Connectivity(ctx, deps.Prober, urls)
		if err != nil {
			return Check{
				ID:            "net.dockerhub",
				Name:          "Docker Hub connectivity",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        "connectivity check cancelled: " + err.Error(),
				FixSuggestion: "check firewall and outbound HTTPS access to hub.docker.com and registry-1.docker.io",
			}
		}
		var failed []string
		for _, r := range results {
			if !r.Reachable {
				failed = append(failed, r.URL)
			}
		}
		if len(failed) > 0 {
			return Check{
				ID:            "net.dockerhub",
				Name:          "Docker Hub connectivity",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        fmt.Sprintf("cannot reach: %s", strings.Join(failed, ", ")),
				FixSuggestion: "check firewall, DNS, and outbound HTTPS access to hub.docker.com and registry-1.docker.io",
			}
		}
		return Check{
			ID:       "net.dockerhub",
			Name:     "Docker Hub connectivity",
			Status:   StatusOK,
			Severity: StatusWarning,
			Detail:   "Docker Hub registries reachable",
		}
	})
}

func checkConnectivityCrenein(ctx context.Context, deps Deps) Check {
	return safeCheck("net.cnetwork_api", "Crenein core connectivity", StatusWarning, func() Check {
		results, err := detect.Connectivity(ctx, deps.Prober, []string{"https://core.crenein.com"})
		if err != nil {
			return Check{
				ID:            "net.cnetwork_api",
				Name:          "Crenein core connectivity",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        "connectivity check cancelled: " + err.Error(),
				FixSuggestion: "check firewall/DNS/outbound HTTPS to core.crenein.com",
			}
		}
		if len(results) > 0 && !results[0].Reachable {
			detail := "cannot reach https://core.crenein.com"
			if results[0].Err != nil {
				detail += ": " + results[0].Err.Error()
			}
			return Check{
				ID:            "net.cnetwork_api",
				Name:          "Crenein core connectivity",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        detail,
				FixSuggestion: "check firewall, DNS, and outbound HTTPS access to core.crenein.com",
			}
		}
		return Check{
			ID:       "net.cnetwork_api",
			Name:     "Crenein core connectivity",
			Status:   StatusOK,
			Severity: StatusWarning,
			Detail:   "core.crenein.com reachable",
		}
	})
}

func checkDiskSpace(ctx context.Context, deps Deps, opts DoctorOptions) Check {
	return safeCheck("disk.space", "Disk space (>2 GB free)", StatusCritical, func() Check {
		var freeMB uint64
		var err error
		if opts.DiskSpaceProvider != nil {
			freeMB, err = detect.DiskSpaceWithProvider(ctx, "/", opts.DiskSpaceProvider)
		} else {
			freeMB, err = detect.DiskSpace(ctx, "/")
		}
		if err != nil {
			fix := "free at least 2048 MB of disk space; run: docker image prune -f"
			return Check{
				ID:       "disk.space",
				Name:     "Disk space (>2 GB free)",
				Status:   StatusCritical,
				Severity: StatusCritical,
				Detail: fmt.Sprintf("disk check failed: %v (free: %d MB, required: %d MB)",
					err, freeMB, detect.MinDiskMB),
				FixSuggestion: fix,
			}
		}
		return Check{
			ID:       "disk.space",
			Name:     "Disk space (>2 GB free)",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   fmt.Sprintf("%d MB free", freeMB),
		}
	})
}

// ─── Install directory resolution ────────────────────────────────────────────

// resolveInstallDir returns the first directory that contains a readable
// docker-compose.yml referencing the agent image. Returns "" when not found.
// It is read-only (6.5) and never fails the overall run.
func resolveInstallDir(ctx context.Context, deps Deps, opts DoctorOptions) string {
	if opts.InstallDir != "" {
		return opts.InstallDir
	}

	candidates := []string{"."}
	candidates = append(candidates, "/root")
	homeEntries, _ := deps.FS.ReadDir("/home")
	for _, entry := range homeEntries {
		candidates = append(candidates, "/home/"+entry)
	}

	for _, dir := range candidates {
		data, err := deps.FS.ReadFile(dir + "/docker-compose.yml")
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "crenein/c-network-agent-back") ||
			strings.Contains(string(data), "c-network-agent-back") {
			return dir
		}
	}
	return ""
}

// ─── 6.3 Permission checks ───────────────────────────────────────────────────

func checkEnvPermission(_ context.Context, deps Deps, installDir string) Check {
	return safeCheck("files.env_permission", ".env file permission (600)", StatusWarning, func() Check {
		envPath := installDir + "/.env"
		if installDir == "" {
			envPath = ".env"
		}
		fi, err := deps.FS.Stat(envPath)
		if err != nil {
			return Check{
				ID:            "files.env_permission",
				Name:          ".env file permission (600)",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        ".env not found — no installation detected",
				FixSuggestion: "run the installer to create .env with correct permissions",
			}
		}
		mode := fi.Mode & 0o777
		if mode != 0o600 {
			displayDir := installDir
			if displayDir == "" {
				displayDir = "."
			}
			return Check{
				ID:            "files.env_permission",
				Name:          ".env file permission (600)",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        fmt.Sprintf(".env has mode %04o, expected 0600", mode),
				FixSuggestion: fmt.Sprintf("chmod 600 %s/.env", displayDir),
			}
		}
		return Check{
			ID:       "files.env_permission",
			Name:     ".env file permission (600)",
			Status:   StatusOK,
			Severity: StatusWarning,
			Detail:   ".env exists with mode 0600",
		}
	})
}

func checkComposeReadable(_ context.Context, deps Deps, installDir string) Check {
	return safeCheck("files.compose_readable", "docker-compose.yml readable", StatusCritical, func() Check {
		composePath := installDir + "/docker-compose.yml"
		if installDir == "" {
			composePath = "docker-compose.yml"
		}
		_, err := deps.FS.ReadFile(composePath)
		if err != nil {
			return Check{
				ID:       "files.compose_readable",
				Name:     "docker-compose.yml readable",
				Status:   StatusCritical,
				Severity: StatusCritical,
				Detail:   fmt.Sprintf("docker-compose.yml not found or not readable at %s", composePath),
				FixSuggestion: "ensure the install directory contains docker-compose.yml " +
					"and the current user has read permission",
			}
		}
		return Check{
			ID:       "files.compose_readable",
			Name:     "docker-compose.yml readable",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "docker-compose.yml is readable",
		}
	})
}

// certDirPairs maps the cert subdirectory to the pair of (cert file, key file)
// expected inside it, relative to installDir.
var certDirPairs = []struct {
	label    string
	certPath string
	keyPath  string
}{
	{"backend", "c-network-agent-back/certs/cert.pem", "c-network-agent-back/certs/key.pem"},
	{"frontend", "c-network-agent-front/certs/cert.pem", "c-network-agent-front/certs/key.pem"},
}

func checkCertKeyModes(_ context.Context, deps Deps, installDir string) Check {
	return safeCheck("files.cert_modes", "Certificate file permissions", StatusCritical, func() Check {
		if installDir == "" {
			return Check{
				ID:            "files.cert_modes",
				Name:          "Certificate file permissions",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "no installation directory found; certificate check skipped",
				FixSuggestion: "run the installer so certificates are created with correct permissions",
			}
		}

		var issues []string
		for _, pair := range certDirPairs {
			certFull := installDir + "/" + pair.certPath
			keyFull := installDir + "/" + pair.keyPath

			if fi, err := deps.FS.Stat(certFull); err == nil {
				mode := fi.Mode & 0o777
				if mode != 0o644 {
					issues = append(issues,
						fmt.Sprintf("%s cert.pem mode %04o (want 0644)", pair.label, mode))
				}
			}
			if fi, err := deps.FS.Stat(keyFull); err == nil {
				mode := fi.Mode & 0o777
				if mode != 0o600 {
					issues = append(issues,
						fmt.Sprintf("%s key.pem mode %04o (want 0600)", pair.label, mode))
				}
			}
		}

		if len(issues) > 0 {
			return Check{
				ID:       "files.cert_modes",
				Name:     "Certificate file permissions",
				Status:   StatusCritical,
				Severity: StatusCritical,
				Detail:   "certificate file mode issues: " + strings.Join(issues, "; "),
				FixSuggestion: fmt.Sprintf(
					"fix with: chmod 644 %s/*/certs/cert.pem && chmod 600 %s/*/certs/key.pem",
					installDir, installDir),
			}
		}
		return Check{
			ID:       "files.cert_modes",
			Name:     "Certificate file permissions",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "certificate file permissions are correct",
		}
	})
}

func checkDockerSocket(ctx context.Context, deps Deps) Check {
	return safeCheck("files.docker_socket", "Docker socket accessible", StatusCritical, func() Check {
		info, err := detect.Permissions(ctx, deps.Runner)
		if err != nil {
			return Check{
				ID:            "files.docker_socket",
				Name:          "Docker socket accessible",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "permission check failed: " + err.Error(),
				FixSuggestion: "sudo usermod -aG docker $USER (then log out and back in)",
			}
		}
		if !info.DockerSocketAccessible {
			return Check{
				ID:            "files.docker_socket",
				Name:          "Docker socket accessible",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "current user cannot access /var/run/docker.sock",
				FixSuggestion: "sudo usermod -aG docker $USER (then log out and back in) — or re-run with sudo",
			}
		}
		return Check{
			ID:       "files.docker_socket",
			Name:     "Docker socket accessible",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "docker socket is accessible",
		}
	})
}

// ─── 6.4 Stack checks ────────────────────────────────────────────────────────

var agentServices = []string{"agent", "frontend", "mongodb", "influxdb", "redis"}

var logServices = []string{"agent", "frontend"}

var errorPatterns = []string{"error", "fatal", "panic", "exception", "critical"}

func checkStackServices(ctx context.Context, deps Deps, installDir string) Check {
	return safeCheck("services.running", "Agent stack service states", StatusCritical, func() Check {
		if installDir == "" {
			return Check{
				ID:            "services.running",
				Name:          "Agent stack service states",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "no installation found; stack check skipped",
				FixSuggestion: "run the installer first: crenein-agent-tui install",
			}
		}

		composeFile := installDir + "/docker-compose.yml"
		containers, err := deps.Client.ComposePs(ctx, composeFile, agentServices)
		if err != nil {
			return Check{
				ID:       "services.running",
				Name:     "Agent stack service states",
				Status:   StatusCritical,
				Severity: StatusCritical,
				Detail:   "compose ps failed: " + err.Error(),
				FixSuggestion: fmt.Sprintf(
					"start the stack: docker compose -f %s up -d", composeFile),
			}
		}

		running := make(map[string]bool)
		for _, c := range containers {
			if c.Running {
				running[c.Service] = true
			}
		}

		var down []string
		for _, svc := range agentServices {
			if !running[svc] {
				down = append(down, svc)
			}
		}

		if len(down) > 0 {
			return Check{
				ID:       "services.running",
				Name:     "Agent stack service states",
				Status:   StatusCritical,
				Severity: StatusCritical,
				Detail:   fmt.Sprintf("services not running: %s", strings.Join(down, ", ")),
				FixSuggestion: fmt.Sprintf(
					"start them: docker compose -f %s up -d %s — view logs: docker compose -f %s logs %s",
					composeFile, strings.Join(down, " "),
					composeFile, strings.Join(down, " ")),
			}
		}

		return Check{
			ID:       "services.running",
			Name:     "Agent stack service states",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   fmt.Sprintf("all services running: %s", strings.Join(agentServices, ", ")),
		}
	})
}

func checkServiceLogs(ctx context.Context, deps Deps, installDir string) Check {
	return safeCheck("logs.recent_errors", "Recent log errors", StatusWarning, func() Check {
		if installDir == "" {
			return Check{
				ID:            "logs.recent_errors",
				Name:          "Recent log errors",
				Status:        StatusWarning,
				Severity:      StatusWarning,
				Detail:        "no installation found; log scan skipped",
				FixSuggestion: "run the installer first: crenein-agent-tui install",
			}
		}

		composeFile := installDir + "/docker-compose.yml"
		const tail = 50

		var allMatches []string
		for _, svc := range logServices {
			logData, err := deps.Client.ComposeLogs(ctx, composeFile, svc, tail)
			if err != nil {
				allMatches = append(allMatches,
					fmt.Sprintf("[%s] could not read logs: %v", svc, err))
				continue
			}
			lines := strings.Split(string(logData), "\n")
			for _, line := range lines {
				lower := strings.ToLower(line)
				for _, pat := range errorPatterns {
					if strings.Contains(lower, pat) {
						trimmed := strings.TrimSpace(line)
						if len(trimmed) > 200 {
							trimmed = trimmed[:200] + "..."
						}
						allMatches = append(allMatches, fmt.Sprintf("[%s] %s", svc, trimmed))
						break
					}
				}
			}
		}

		if len(allMatches) > 0 {
			sample := allMatches
			if len(sample) > 5 {
				sample = sample[:5]
			}
			return Check{
				ID:       "logs.recent_errors",
				Name:     "Recent log errors",
				Status:   StatusWarning,
				Severity: StatusWarning,
				Detail: fmt.Sprintf("%d error line(s) found in last %d log lines; sample: %s",
					len(allMatches), tail, strings.Join(sample, " | ")),
				FixSuggestion: fmt.Sprintf(
					"inspect full logs: docker compose -f %s logs --tail 100 agent frontend",
					composeFile),
			}
		}

		return Check{
			ID:       "logs.recent_errors",
			Name:     "Recent log errors",
			Status:   StatusOK,
			Severity: StatusWarning,
			Detail: fmt.Sprintf("no error patterns found in last %d log lines of %s",
				tail, strings.Join(logServices, ", ")),
		}
	})
}

// agentHealthBody is the minimal JSON shape returned by /health.
type agentHealthBody struct {
	Version string `json:"version"`
}

// checkAgentHealth probes GET https://localhost:8000/health (insecure TLS),
// falling back to http://localhost:8000/health.
// HTTP 200 → OK; HTTP 404 → WARNING (legacy backend); other → CRITICAL.
func checkAgentHealth(ctx context.Context, deps Deps) Check {
	return safeCheck("agent.health", "Agent /health endpoint", StatusCritical, func() Check {
		type probeResult struct {
			statusCode int
			version    string
			err        error
		}

		probe := func(url string) probeResult {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return probeResult{err: err}
			}
			resp, err := deps.Prober.Do(req)
			if err != nil {
				return probeResult{err: err}
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var h agentHealthBody
			_ = json.Unmarshal(body, &h)
			return probeResult{statusCode: resp.StatusCode, version: h.Version}
		}

		var res probeResult
		for _, url := range []string{
			"https://localhost:8000/health",
			"http://localhost:8000/health",
		} {
			res = probe(url)
			if res.err == nil {
				break
			}
		}

		if res.err != nil {
			return Check{
				ID:            "agent.health",
				Name:          "Agent /health endpoint",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "could not reach agent /health: " + res.err.Error(),
				FixSuggestion: "check that the agent container is running: docker compose ps",
			}
		}

		if res.statusCode == http.StatusOK {
			detail := "agent /health returned 200"
			if res.version != "" {
				detail += fmt.Sprintf(" (version: %s)", res.version)
			}
			return Check{
				ID:       "agent.health",
				Name:     "Agent /health endpoint",
				Status:   StatusOK,
				Severity: StatusCritical,
				Detail:   detail,
			}
		}

		if res.statusCode == http.StatusNotFound {
			return Check{
				ID:            "agent.health",
				Name:          "Agent /health endpoint",
				Status:        StatusWarning,
				Severity:      StatusCritical,
				Detail:        "agent /health returned 404 — legacy backend without root health endpoint",
				FixSuggestion: "update the agent to a version that exposes /health",
			}
		}

		return Check{
			ID:            "agent.health",
			Name:          "Agent /health endpoint",
			Status:        StatusCritical,
			Severity:      StatusCritical,
			Detail:        fmt.Sprintf("agent /health returned unexpected status %d", res.statusCode),
			FixSuggestion: "check agent container logs: docker compose logs agent",
		}
	})
}

// checkAVXMongo reads the Mongo image from docker-compose.yml and checks AVX
// compatibility: Mongo ≥5.0 requires AVX. If AVX is absent → CRITICAL.
func checkAVXMongo(ctx context.Context, deps Deps, installDir string) Check {
	return safeCheck("cpu.avx_mongo", "CPU AVX / MongoDB compatibility", StatusCritical, func() Check {
		composePath := installDir + "/docker-compose.yml"
		data, err := deps.FS.ReadFile(composePath)
		if err != nil {
			return Check{
				ID:            "cpu.avx_mongo",
				Name:          "CPU AVX / MongoDB compatibility",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "could not read docker-compose.yml to determine MongoDB image",
				FixSuggestion: "ensure docker-compose.yml is readable",
			}
		}

		mongoNeedsAVX := mongoImageNeedsAVX(string(data))
		if !mongoNeedsAVX {
			return Check{
				ID:       "cpu.avx_mongo",
				Name:     "CPU AVX / MongoDB compatibility",
				Status:   StatusOK,
				Severity: StatusCritical,
				Detail:   "MongoDB 4.4 in use — no AVX requirement",
			}
		}

		hasAVX, avxErr := detect.AVX(ctx, deps.FS)
		if avxErr != nil {
			return Check{
				ID:            "cpu.avx_mongo",
				Name:          "CPU AVX / MongoDB compatibility",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "could not read /proc/cpuinfo to check AVX: " + avxErr.Error(),
				FixSuggestion: "verify CPU AVX support manually: grep avx /proc/cpuinfo",
			}
		}

		if !hasAVX {
			return Check{
				ID:            "cpu.avx_mongo",
				Name:          "CPU AVX / MongoDB compatibility",
				Status:        StatusCritical,
				Severity:      StatusCritical,
				Detail:        "MongoDB ≥5.0 requires AVX but CPU does not support it",
				FixSuggestion: "reinstall with --mongo 4 to use MongoDB 4.4",
			}
		}

		return Check{
			ID:       "cpu.avx_mongo",
			Name:     "CPU AVX / MongoDB compatibility",
			Status:   StatusOK,
			Severity: StatusCritical,
			Detail:   "MongoDB ≥5.0 in use and AVX is supported",
		}
	})
}

// mongoImageNeedsAVX returns true when the docker-compose.yml contains a
// MongoDB image that requires AVX (version ≥5.0).
func mongoImageNeedsAVX(composeContent string) bool {
	lines := strings.Split(composeContent, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Look for mongo image references.
		if !strings.Contains(strings.ToLower(trimmed), "mongo") {
			continue
		}
		// mongodb-community-server:X.Y → check major version.
		if strings.Contains(trimmed, "mongodb-community-server") ||
			strings.Contains(trimmed, "mongodb/mongodb-community-server") {
			// Extract version tag — these are always ≥5.0 if present.
			return true
		}
		// mongo:X.Y — check if major < 5.
		if idx := strings.Index(trimmed, "mongo:"); idx != -1 {
			tag := trimmed[idx+len("mongo:"):]
			// Remove trailing quotes/spaces.
			tag = strings.Trim(tag, `"' `)
			if strings.HasPrefix(tag, "4.") || tag == "4" {
				return false
			}
			if tag == "latest" || tag == "" {
				return true
			}
			// Any other version — assume needs AVX (≥5.0).
			return true
		}
	}
	// Default: if we couldn't determine the version, assume it might need AVX.
	return false
}
