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
	"fmt"
	"strings"

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
)

// severity returns a numeric weight for ordering: CRITICAL > WARNING > OK.
// Higher values are more severe.
func (s Status) severity() int {
	switch s {
	case StatusCritical:
		return 2
	case StatusWarning:
		return 1
	default:
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
// future --json flag (AD-7).
type Check struct {
	// ID is a stable, machine-readable identifier (e.g. "docker-installed").
	ID string `json:"id"`
	// Name is a human-readable label (e.g. "Docker installed").
	Name string `json:"name"`
	// Status is OK, WARNING, or CRITICAL.
	Status Status `json:"status"`
	// Detail is a human-readable explanation of the check result.
	Detail string `json:"detail"`
	// FixSuggestion is a concrete, actionable command or instruction for any
	// non-OK status. Empty for OK checks.
	FixSuggestion string `json:"fix_suggestion,omitempty"`
}

// DoctorReport is the structured output of Run. JSON-serializable (AD-7).
type DoctorReport struct {
	// Checks is the ordered list of individual check results.
	Checks []Check `json:"checks"`
	// Summary is the worst individual status across all checks. OK when empty.
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

	// Helper that appends a check and updates the summary.
	add := func(c Check) {
		report.Checks = append(report.Checks, c)
		report.Summary = report.Summary.worse(c.Status)
	}

	// ── 6.2 Host checks ─────────────────────────────────────────────────────
	add(checkDockerInstalled(ctx, deps))
	add(checkDockerDaemon(ctx, deps))
	add(checkCompose(ctx, deps))
	add(checkConnectivityDockerHub(ctx, deps))
	add(checkConnectivityCrenein(ctx, deps))
	add(checkDiskSpace(ctx, deps, opts))

	// ── 6.3 Permission checks ────────────────────────────────────────────────
	installDir := resolveInstallDir(ctx, deps, opts)
	add(checkEnvPermission(ctx, deps, installDir))
	add(checkComposeReadable(ctx, deps, installDir))
	add(checkCertKeyModes(ctx, deps, installDir))
	add(checkDockerSocket(ctx, deps))

	// ── 6.4 Stack checks ────────────────────────────────────────────────────
	add(checkStackServices(ctx, deps, installDir))
	add(checkServiceLogs(ctx, deps, installDir))

	return report
}

// safeCheck wraps a check function so that any unexpected panic or unhandled
// error becomes a CRITICAL entry instead of aborting the run (6.5 guarantee).
func safeCheck(id, name string, fn func() Check) (result Check) {
	defer func() {
		if r := recover(); r != nil {
			result = Check{
				ID:            id,
				Name:          name,
				Status:        StatusCritical,
				Detail:        fmt.Sprintf("unexpected error in check: %v", r),
				FixSuggestion: "report this as a bug",
			}
		}
	}()
	return fn()
}

// ─── 6.2 Host checks ─────────────────────────────────────────────────────────

func checkDockerInstalled(ctx context.Context, deps Deps) Check {
	return safeCheck("docker-installed", "Docker installed", func() Check {
		info := detect.Docker(ctx, deps.Runner, deps.Client)
		if !info.Installed {
			return Check{
				ID:            "docker-installed",
				Name:          "Docker installed",
				Status:        StatusCritical,
				Detail:        "docker binary not found in PATH",
				FixSuggestion: "install Docker: apt-get install docker-ce docker-ce-cli containerd.io docker-compose-plugin",
			}
		}
		return Check{
			ID:     "docker-installed",
			Name:   "Docker installed",
			Status: StatusOK,
			Detail: "docker binary found",
		}
	})
}

func checkDockerDaemon(ctx context.Context, deps Deps) Check {
	return safeCheck("docker-daemon", "Docker daemon running", func() Check {
		if err := deps.Client.Ping(ctx); err != nil {
			// Distinguish permission denied vs not running via error message.
			detail := "Docker daemon is not responding"
			fix := "start the daemon: systemctl start docker"
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "permission denied") || strings.Contains(msg, "access denied") {
				detail = "Docker daemon is not accessible: permission denied on socket"
				fix = "add user to docker group: sudo usermod -aG docker $USER (then log out and back in) — or run with sudo"
			}
			return Check{
				ID:            "docker-daemon",
				Name:          "Docker daemon running",
				Status:        StatusCritical,
				Detail:        detail,
				FixSuggestion: fix,
			}
		}
		return Check{
			ID:     "docker-daemon",
			Name:   "Docker daemon running",
			Status: StatusOK,
			Detail: "daemon responded to ping",
		}
	})
}

func checkCompose(ctx context.Context, deps Deps) Check {
	return safeCheck("compose-available", "Compose available", func() Check {
		info, err := detect.Compose(ctx, deps.Runner)
		if err != nil {
			return Check{
				ID:            "compose-available",
				Name:          "Compose available",
				Status:        StatusCritical,
				Detail:        "no compose variant found (neither docker compose v2 nor docker-compose v1)",
				FixSuggestion: "install compose v2 plugin: apt-get install docker-compose-plugin",
			}
		}
		if info.V1Available && !info.V2Available {
			return Check{
				ID:            "compose-available",
				Name:          "Compose available",
				Status:        StatusWarning,
				Detail:        fmt.Sprintf("compose v1 in use (%s); v2 is preferred", info.Version),
				FixSuggestion: "upgrade to compose v2: apt-get install docker-compose-plugin",
			}
		}
		return Check{
			ID:     "compose-available",
			Name:   "Compose available",
			Status: StatusOK,
			Detail: fmt.Sprintf("compose v2 available (%s)", info.Version),
		}
	})
}

func checkConnectivityDockerHub(ctx context.Context, deps Deps) Check {
	return safeCheck("connectivity-docker-hub", "Docker Hub connectivity", func() Check {
		urls := []string{
			"https://registry-1.docker.io/v2/",
			"https://hub.docker.com",
		}
		results, err := detect.Connectivity(ctx, deps.Prober, urls)
		if err != nil {
			return Check{
				ID:            "connectivity-docker-hub",
				Name:          "Docker Hub connectivity",
				Status:        StatusCritical,
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
				ID:            "connectivity-docker-hub",
				Name:          "Docker Hub connectivity",
				Status:        StatusCritical,
				Detail:        fmt.Sprintf("cannot reach: %s", strings.Join(failed, ", ")),
				FixSuggestion: "check firewall, DNS, and outbound HTTPS access to hub.docker.com and registry-1.docker.io",
			}
		}
		return Check{
			ID:     "connectivity-docker-hub",
			Name:   "Docker Hub connectivity",
			Status: StatusOK,
			Detail: "Docker Hub registries reachable",
		}
	})
}

func checkConnectivityCrenein(ctx context.Context, deps Deps) Check {
	return safeCheck("connectivity-crenein", "Crenein core connectivity", func() Check {
		results, err := detect.Connectivity(ctx, deps.Prober, []string{"https://core.crenein.com"})
		if err != nil {
			return Check{
				ID:            "connectivity-crenein",
				Name:          "Crenein core connectivity",
				Status:        StatusCritical,
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
				ID:            "connectivity-crenein",
				Name:          "Crenein core connectivity",
				Status:        StatusCritical,
				Detail:        detail,
				FixSuggestion: "check firewall, DNS, and outbound HTTPS access to core.crenein.com",
			}
		}
		return Check{
			ID:     "connectivity-crenein",
			Name:   "Crenein core connectivity",
			Status: StatusOK,
			Detail: "core.crenein.com reachable",
		}
	})
}

func checkDiskSpace(ctx context.Context, deps Deps, opts DoctorOptions) Check {
	return safeCheck("disk-space", "Disk space (>2 GB free)", func() Check {
		var freeMB uint64
		var err error
		if opts.DiskSpaceProvider != nil {
			freeMB, err = detect.DiskSpaceWithProvider(ctx, "/", opts.DiskSpaceProvider)
		} else {
			freeMB, err = detect.DiskSpace(ctx, "/")
		}
		if err != nil {
			// err may carry a structured cnerr.Error with FixSuggestion.
			fix := "free at least 2048 MB of disk space; run: docker image prune -f"
			type fixer interface{ fix() string }
			if ce, ok := err.(interface{ Error() string }); ok {
				_ = ce
			}
			// Prefer the cnerr.Error fix suggestion if available.
			type withFix interface {
				GetFix() string
			}
			return Check{
				ID:     "disk-space",
				Name:   "Disk space (>2 GB free)",
				Status: StatusCritical,
				Detail: fmt.Sprintf("disk check failed: %v (free: %d MB, required: %d MB)",
					err, freeMB, detect.MinDiskMB),
				FixSuggestion: fix,
			}
		}
		return Check{
			ID:     "disk-space",
			Name:   "Disk space (>2 GB free)",
			Status: StatusOK,
			Detail: fmt.Sprintf("%d MB free", freeMB),
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

	// Mirror findInstallDir from update.go but return "" instead of an error.
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
	return safeCheck("env-permission", ".env file permission (600)", func() Check {
		envPath := installDir + "/.env"
		if installDir == "" {
			envPath = ".env"
		}
		fi, err := deps.FS.Stat(envPath)
		if err != nil {
			return Check{
				ID:     "env-permission",
				Name:   ".env file permission (600)",
				Status: StatusWarning,
				Detail: ".env not found — no installation detected",
				FixSuggestion: fmt.Sprintf(
					"run the installer to create .env with correct permissions"),
			}
		}
		mode := fi.Mode & 0o777
		if mode != 0o600 {
			displayDir := installDir
			if displayDir == "" {
				displayDir = "."
			}
			return Check{
				ID:            "env-permission",
				Name:          ".env file permission (600)",
				Status:        StatusWarning,
				Detail:        fmt.Sprintf(".env has mode %04o, expected 0600", mode),
				FixSuggestion: fmt.Sprintf("chmod 600 %s/.env", displayDir),
			}
		}
		return Check{
			ID:     "env-permission",
			Name:   ".env file permission (600)",
			Status: StatusOK,
			Detail: ".env exists with mode 0600",
		}
	})
}

func checkComposeReadable(_ context.Context, deps Deps, installDir string) Check {
	return safeCheck("compose-readable", "docker-compose.yml readable", func() Check {
		composePath := installDir + "/docker-compose.yml"
		if installDir == "" {
			composePath = "docker-compose.yml"
		}
		_, err := deps.FS.ReadFile(composePath)
		if err != nil {
			return Check{
				ID:     "compose-readable",
				Name:   "docker-compose.yml readable",
				Status: StatusWarning,
				Detail: fmt.Sprintf("docker-compose.yml not found or not readable at %s", composePath),
				FixSuggestion: "ensure the install directory contains docker-compose.yml " +
					"and the current user has read permission",
			}
		}
		return Check{
			ID:     "compose-readable",
			Name:   "docker-compose.yml readable",
			Status: StatusOK,
			Detail: "docker-compose.yml is readable",
		}
	})
}

// certDirPairs maps the cert subdirectory to the pair of (cert file, key file)
// expected inside it, relative to installDir.
var certDirPairs = []struct {
	label    string
	certPath string // relative to installDir
	keyPath  string
}{
	{"backend", "c-network-agent-back/certs/cert.pem", "c-network-agent-back/certs/key.pem"},
	{"frontend", "c-network-agent-front/certs/cert.pem", "c-network-agent-front/certs/key.pem"},
}

func checkCertKeyModes(_ context.Context, deps Deps, installDir string) Check {
	return safeCheck("cert-key-modes", "Certificate file permissions", func() Check {
		if installDir == "" {
			return Check{
				ID:            "cert-key-modes",
				Name:          "Certificate file permissions",
				Status:        StatusWarning,
				Detail:        "no installation directory found; certificate check skipped",
				FixSuggestion: "run the installer so certificates are created with correct permissions",
			}
		}

		var issues []string
		for _, pair := range certDirPairs {
			certFull := installDir + "/" + pair.certPath
			keyFull := installDir + "/" + pair.keyPath

			// cert.pem should be 644.
			if fi, err := deps.FS.Stat(certFull); err == nil {
				mode := fi.Mode & 0o777
				if mode != 0o644 {
					issues = append(issues,
						fmt.Sprintf("%s cert.pem mode %04o (want 0644)", pair.label, mode))
				}
			}
			// key.pem should be 600.
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
				ID:     "cert-key-modes",
				Name:   "Certificate file permissions",
				Status: StatusWarning,
				Detail: "certificate file mode issues: " + strings.Join(issues, "; "),
				FixSuggestion: fmt.Sprintf(
					"fix with: chmod 644 %s/*/certs/cert.pem && chmod 600 %s/*/certs/key.pem",
					installDir, installDir),
			}
		}
		return Check{
			ID:     "cert-key-modes",
			Name:   "Certificate file permissions",
			Status: StatusOK,
			Detail: "certificate file permissions are correct",
		}
	})
}

func checkDockerSocket(ctx context.Context, deps Deps) Check {
	return safeCheck("docker-socket", "Docker socket accessible", func() Check {
		// Use detect.Permissions which checks socket access via docker info.
		info, err := detect.Permissions(ctx, deps.Runner)
		if err != nil {
			return Check{
				ID:            "docker-socket",
				Name:          "Docker socket accessible",
				Status:        StatusCritical,
				Detail:        "permission check failed: " + err.Error(),
				FixSuggestion: "sudo usermod -aG docker $USER (then log out and back in)",
			}
		}
		if !info.DockerSocketAccessible {
			return Check{
				ID:            "docker-socket",
				Name:          "Docker socket accessible",
				Status:        StatusCritical,
				Detail:        "current user cannot access /var/run/docker.sock",
				FixSuggestion: "sudo usermod -aG docker $USER (then log out and back in) — or re-run with sudo",
			}
		}
		return Check{
			ID:     "docker-socket",
			Name:   "Docker socket accessible",
			Status: StatusOK,
			Detail: "docker socket is accessible",
		}
	})
}

// ─── 6.4 Stack checks ────────────────────────────────────────────────────────

// agentServices are the compose services the doctor checks.
var agentServices = []string{"agent", "frontend", "mongodb", "influxdb", "redis"}

// logServices are the services whose recent logs are scanned for errors.
// (only agent-side services, not the databases).
var logServices = []string{"agent", "frontend"}

// errorPatterns are the case-insensitive substrings that flag a log line as an
// error. Kept simple on purpose — the doctor is a quick triage tool, not a log
// analytics platform.
var errorPatterns = []string{"error", "fatal", "panic", "exception", "critical"}

func checkStackServices(ctx context.Context, deps Deps, installDir string) Check {
	return safeCheck("stack-services", "Agent stack service states", func() Check {
		if installDir == "" {
			return Check{
				ID:            "stack-services",
				Name:          "Agent stack service states",
				Status:        StatusWarning,
				Detail:        "no installation found; stack check skipped",
				FixSuggestion: "run the installer first: crenein-agent-tui install",
			}
		}

		composeFile := installDir + "/docker-compose.yml"
		containers, err := deps.Client.ComposePs(ctx, composeFile, agentServices)
		if err != nil {
			return Check{
				ID:     "stack-services",
				Name:   "Agent stack service states",
				Status: StatusCritical,
				Detail: "compose ps failed: " + err.Error(),
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
				ID:     "stack-services",
				Name:   "Agent stack service states",
				Status: StatusCritical,
				Detail: fmt.Sprintf("services not running: %s", strings.Join(down, ", ")),
				FixSuggestion: fmt.Sprintf(
					"start them: docker compose -f %s up -d %s — view logs: docker compose -f %s logs %s",
					composeFile, strings.Join(down, " "),
					composeFile, strings.Join(down, " ")),
			}
		}

		return Check{
			ID:     "stack-services",
			Name:   "Agent stack service states",
			Status: StatusOK,
			Detail: fmt.Sprintf("all services running: %s", strings.Join(agentServices, ", ")),
		}
	})
}

func checkServiceLogs(ctx context.Context, deps Deps, installDir string) Check {
	return safeCheck("service-logs", "Recent log errors", func() Check {
		if installDir == "" {
			return Check{
				ID:            "service-logs",
				Name:          "Recent log errors",
				Status:        StatusWarning,
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
				// Non-fatal: service may not be running; report as part of detail.
				allMatches = append(allMatches,
					fmt.Sprintf("[%s] could not read logs: %v", svc, err))
				continue
			}
			lines := strings.Split(string(logData), "\n")
			for _, line := range lines {
				lower := strings.ToLower(line)
				for _, pat := range errorPatterns {
					if strings.Contains(lower, pat) {
						// Trim the line to keep the report concise.
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
			// Show at most 5 sample lines to keep the report readable.
			sample := allMatches
			if len(sample) > 5 {
				sample = sample[:5]
			}
			return Check{
				ID:     "service-logs",
				Name:   "Recent log errors",
				Status: StatusWarning,
				Detail: fmt.Sprintf("%d error line(s) found in last %d log lines; sample: %s",
					len(allMatches), tail, strings.Join(sample, " | ")),
				FixSuggestion: fmt.Sprintf(
					"inspect full logs: docker compose -f %s logs --tail 100 agent frontend",
					composeFile),
			}
		}

		return Check{
			ID:     "service-logs",
			Name:   "Recent log errors",
			Status: StatusOK,
			Detail: fmt.Sprintf("no error patterns found in last %d log lines of %s",
				tail, strings.Join(logServices, ", ")),
		}
	})
}
