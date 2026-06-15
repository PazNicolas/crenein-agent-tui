// Package engine — install sub-engine (tasks 4.1–4.12).
//
// Design (AD-1): Install never prints, prompts, or reads stdin. Progress flows
// through deps.Reporter; interactive decisions arrive pre-resolved in
// InstallOptions.
//
// Design (AD-2): all external effects go through deps.Client / Runner / FS /
// Prober. No exec.Command or os.WriteFile calls.
//
// Design (AD-4): MongoDB image auto-selected via AVX; override via
// InstallOptions.MongoImageOverride.
//
// Design (AD-5): InfluxDB token generated cryptographically random per install.
package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/compose"
	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Options & Result ────────────────────────────────────────────────────────

// InstallOptions carries all pre-resolved decisions for Install. The caller
// (CLI/TUI) is responsible for interactive resolution before calling Install.
type InstallOptions struct {
	// MongoImageOverride forces a specific MongoDB image, bypassing AVX detection.
	// Empty means automatic selection via AVX.
	MongoImageOverride string

	// InstallDir is the working directory where docker-compose.yml and .env are
	// written. Defaults to "." when empty.
	InstallDir string

	// AdminEmail overrides the default admin account email (admin@example.com).
	AdminEmail string

	// AdminPassword overrides the default admin account password (admin123).
	AdminPassword string

	// APIURL overrides the CNETWORK_API_URL written to the .env file.
	// When empty, the default value "http://localhost:8000" is used.
	APIURL string

	// APIToken overrides the CNETWORK_API_TOKEN written to the .env file.
	// When empty, the default value "your-api-token-here" is used.
	APIToken string

	// DiskSpaceProvider injects a fake disk-space prober for tests.
	// When nil, the real syscall is used.
	DiskSpaceProvider detect.DiskSpaceProvider

	// IsRootFunc overrides the root-privilege check. When nil, the real
	// effective-UID syscall is used. Inject a func that returns true in tests
	// that need to exercise the post-root-check code paths.
	IsRootFunc func() bool

	// ServiceVerifyTimeout overrides the service-verification polling window
	// (default 60s). Useful in tests to avoid long waits.
	ServiceVerifyTimeout time.Duration

	// RetryInterval overrides the inter-retry sleep duration for admin and
	// bucket creation retries (default 3s). Set to 0 in tests.
	RetryInterval time.Duration
}

// installDir returns the resolved install directory (never empty).
func (o InstallOptions) installDir() string {
	if o.InstallDir != "" {
		return o.InstallDir
	}
	return "."
}

// adminEmail returns the resolved admin email.
func (o InstallOptions) adminEmail() string {
	if o.AdminEmail != "" {
		return o.AdminEmail
	}
	return "admin@example.com"
}

// adminPassword returns the resolved admin password.
func (o InstallOptions) adminPassword() string {
	if o.AdminPassword != "" {
		return o.AdminPassword
	}
	return "admin123"
}

// apiURL returns the resolved C-Network API URL.
func (o InstallOptions) apiURL() string {
	if o.APIURL != "" {
		return o.APIURL
	}
	return "http://localhost:8000"
}

// apiToken returns the resolved C-Network API token.
func (o InstallOptions) apiToken() string {
	if o.APIToken != "" {
		return o.APIToken
	}
	return "your-api-token-here"
}

// StepResult records the outcome of one install step.
type StepResult struct {
	// Name is the human-readable step name.
	Name string
	// Skipped is true when the step was intentionally bypassed (e.g. certs
	// already existed, .env preserved, user already existed).
	Skipped bool
	// Note carries an optional detail message (e.g. "defaults used for vsftpd").
	Note string
}

// ServiceStatus records the running state of one compose service.
type ServiceStatus struct {
	Service string
	Running bool
}

// AccessEntry is one access URL / credential pair in the access summary.
type AccessEntry struct {
	Label string
	Value string
}

// InstallResult is the structured outcome of Install.
type InstallResult struct {
	// Steps is the ordered list of step outcomes.
	Steps []StepResult

	// Services records the post-startup running state of each service.
	Services []ServiceStatus

	// AccessSummary is the final access report (URLs, credentials, paths).
	AccessSummary []AccessEntry

	// Warnings is a list of non-fatal issues the operator should review.
	Warnings []string

	// ReinstallMode is true when Install detected an existing installation.
	ReinstallMode bool

	// ReusedComponents lists items that were preserved from a prior install.
	ReusedComponents []string
}

// ─── Install entry-point ─────────────────────────────────────────────────────

// Install runs the full installation sequence, reporting progress through
// deps.Reporter. It returns InstallResult on success and a *cnerr.Error on
// fatal failure.
//
// The operation is idempotent: running Install over an existing installation
// preserves .env credentials, /data contents, and certificates (AD-5, 4.11).
func Install(ctx context.Context, deps Deps, opts InstallOptions) (*InstallResult, error) {
	res := &InstallResult{}
	dir := opts.installDir()

	// ── 4.2 Pre-flight ───────────────────────────────────────────────────────
	distro, mongoImage, err := runPreflight(ctx, deps, opts, res)
	if err != nil {
		return nil, err
	}

	// ── 4.11 Idempotence detection ───────────────────────────────────────────
	detectReinstall(ctx, deps, dir, res)

	// ── 4.3 System preparation ───────────────────────────────────────────────
	if err := runSystemPrep(ctx, deps, res, distro); err != nil {
		return nil, err
	}

	// ── 4.4 Persistent directories ───────────────────────────────────────────
	if err := runDirectories(ctx, deps, res); err != nil {
		return nil, err
	}

	// ── 4.5 vsftpd + tftpd-hpa ──────────────────────────────────────────────
	runFTPConfig(ctx, deps, res)

	// ── 4.6 backups user ─────────────────────────────────────────────────────
	runBackupsUser(ctx, deps, res)

	// ── 4.7 .env ─────────────────────────────────────────────────────────────
	influxToken, err := runEnvFile(ctx, deps, dir, res, opts)
	if err != nil {
		return nil, err
	}

	// ── 4.8 Self-signed certificates ─────────────────────────────────────────
	runCerts(ctx, deps, dir, res)

	// ── 4.9 Render compose + stack up + service verification ─────────────────
	if err := runStack(ctx, deps, dir, mongoImage, res, opts); err != nil {
		return nil, err
	}

	// ── 4.10 Post-install ────────────────────────────────────────────────────
	composeFile := dir + "/docker-compose.yml"
	runPostInstall(ctx, deps, dir, composeFile, influxToken, opts.adminEmail(), opts.adminPassword(), res, opts)

	// ── 4.12 Access summary ──────────────────────────────────────────────────
	buildAccessSummary(dir, opts.adminEmail(), res)

	return res, nil
}

// ─── 4.2 Pre-flight ──────────────────────────────────────────────────────────

func runPreflight(ctx context.Context, deps Deps, opts InstallOptions, res *InstallResult) (detect.DistroInfo, string, error) {
	const step = "preflight"
	deps.StepStarted(step)

	// Distro check.
	distro, err := detect.Distro(ctx, deps.FS)
	if err != nil {
		deps.StepFinished(step, err)
		return detect.DistroInfo{}, "", err
	}

	// Root / permissions check.
	isRoot := false
	if opts.IsRootFunc != nil {
		isRoot = opts.IsRootFunc()
	} else {
		perm, permErr := detect.Permissions(ctx, deps.Runner)
		if permErr != nil {
			deps.StepFinished(step, permErr)
			return detect.DistroInfo{}, "", permErr
		}
		isRoot = perm.IsRoot
	}
	if !isRoot {
		e := cnerr.New("engine.install.preflight",
			"re-run as root: sudo ./crenein-agent-tui install")
		deps.StepFinished(step, e)
		return detect.DistroInfo{}, "", e
	}

	// Disk space check.
	var freeMB uint64
	if opts.DiskSpaceProvider != nil {
		freeMB, err = detect.DiskSpaceWithProvider(ctx, "/", opts.DiskSpaceProvider)
	} else {
		freeMB, err = detect.DiskSpace(ctx, "/")
	}
	if err != nil {
		deps.StepFinished(step, err)
		return detect.DistroInfo{}, "", err
	}
	deps.Info(step, fmt.Sprintf("disk free: %d MB", freeMB))

	// Connectivity check.
	connResults, err := detect.Connectivity(ctx, deps.Prober, detect.DefaultConnectivityURLs)
	if err != nil {
		deps.StepFinished(step, err)
		return detect.DistroInfo{}, "", err
	}
	for _, r := range connResults {
		if !r.Reachable {
			e := cnerr.Wrap("engine.install.preflight", r.Err,
				"check firewall and outbound HTTPS access to "+r.URL)
			deps.StepFinished(step, e)
			return detect.DistroInfo{}, "", e
		}
	}

	// AVX → MongoDB image selection.
	var mongoImage string
	if opts.MongoImageOverride != "" {
		mongoImage = opts.MongoImageOverride
		deps.Info(step, "MongoDB image override: "+mongoImage)
	} else {
		hasAVX, avxErr := detect.AVX(ctx, deps.FS)
		if avxErr != nil {
			deps.Warn(step, "AVX detection failed; defaulting to mongo:4.4 — use MongoImageOverride to force")
			mongoImage = detect.MongoImage(false)
		} else {
			mongoImage = detect.MongoImage(hasAVX)
			if !hasAVX {
				res.Warnings = append(res.Warnings,
					"MongoDB 4.4 selected: CPU does not support AVX (required for MongoDB ≥5.0)")
			}
		}
	}
	deps.Info(step, "MongoDB image: "+mongoImage)

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
	return distro, mongoImage, nil
}

// ─── 4.11 Idempotence detection ──────────────────────────────────────────────

func detectReinstall(ctx context.Context, deps Deps, dir string, res *InstallResult) {
	_ = ctx
	composeFile := dir + "/docker-compose.yml"
	envFile := dir + "/.env"

	// Check for existing compose file referencing the agent image.
	composeData, composeErr := deps.FS.ReadFile(composeFile)
	if composeErr == nil && bytes.Contains(composeData, []byte("crenein/c-network-agent-back")) {
		res.ReinstallMode = true
		res.ReusedComponents = append(res.ReusedComponents, "docker-compose.yml")
	}

	// Check for existing .env.
	if _, envErr := deps.FS.ReadFile(envFile); envErr == nil {
		res.ReinstallMode = true
		res.ReusedComponents = append(res.ReusedComponents, ".env")
	}

	// Check for non-empty /data directories.
	for _, dataDir := range []string{"/data/mongodb", "/data/influxdb2", "/data/redis"} {
		entries, err := deps.FS.ReadDir(dataDir)
		if err == nil && len(entries) > 0 {
			res.ReinstallMode = true
			res.ReusedComponents = append(res.ReusedComponents, dataDir)
		}
	}

	if res.ReinstallMode {
		deps.Info("reinstall", fmt.Sprintf("existing installation detected; reusing: %s",
			strings.Join(res.ReusedComponents, ", ")))
	}
}

// ─── 4.3 System preparation ──────────────────────────────────────────────────

func runSystemPrep(ctx context.Context, deps Deps, res *InstallResult, distro detect.DistroInfo) error {
	const step = "system-prep"
	deps.StepStarted(step)

	// apt-get update.
	if _, err := deps.Runner.Run(ctx, "apt-get", "update", "-y"); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err, "run: apt-get update -y")
		deps.StepFinished(step, e)
		return e
	}

	// Install required packages (parity row 3).
	packages := []string{
		"apt-transport-https", "ca-certificates", "curl", "gnupg",
		"lsb-release", "fping", "vsftpd", "tftpd-hpa", "jq",
	}
	args := append([]string{"install", "-y"}, packages...)
	if _, err := deps.Runner.Run(ctx, "apt-get", args...); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err,
			"run: apt-get install -y "+strings.Join(packages, " "))
		deps.StepFinished(step, e)
		return e
	}

	// Docker install: skip when already installed (parity row 4).
	dockerInfo := detect.Docker(ctx, deps.Runner, deps.Client)
	if dockerInfo.Installed && dockerInfo.DaemonRunning {
		deps.Info(step, "Docker already installed and running — skipping")
		res.Steps = append(res.Steps, StepResult{Name: step, Skipped: false,
			Note: "Docker already present"})
		deps.StepFinished(step, nil)
		return nil
	}

	// GPG key.
	gpgURL := fmt.Sprintf("https://download.docker.com/linux/%s/gpg", distro.ID)
	if _, err := deps.Runner.Run(ctx, "bash", "-c",
		fmt.Sprintf(`curl -fsSL %s | gpg --dearmor --yes -o /usr/share/keyrings/docker-archive-keyring.gpg`, gpgURL),
	); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err,
			"download Docker GPG key from "+gpgURL)
		deps.StepFinished(step, e)
		return e
	}

	// Add Docker apt repository.
	repoLine := fmt.Sprintf(
		`deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/%s $(lsb_release -cs) stable`,
		distro.ID,
	)
	if _, err := deps.Runner.Run(ctx, "bash", "-c",
		fmt.Sprintf(`echo "%s" | tee /etc/apt/sources.list.d/docker.list > /dev/null`, repoLine),
	); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err, "add Docker apt repository")
		deps.StepFinished(step, e)
		return e
	}

	// apt-get update again after adding the repo.
	if _, err := deps.Runner.Run(ctx, "apt-get", "update", "-y"); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err, "run: apt-get update -y (post-repo)")
		deps.StepFinished(step, e)
		return e
	}

	// Install Docker packages.
	dockerPkgs := []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-compose-plugin"}
	dkArgs := append([]string{"install", "-y"}, dockerPkgs...)
	if _, err := deps.Runner.Run(ctx, "apt-get", dkArgs...); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err,
			"run: apt-get install -y "+strings.Join(dockerPkgs, " "))
		deps.StepFinished(step, e)
		return e
	}

	// systemctl start + enable docker.
	if _, err := deps.Runner.Run(ctx, "systemctl", "start", "docker"); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err, "run: systemctl start docker")
		deps.StepFinished(step, e)
		return e
	}
	if _, err := deps.Runner.Run(ctx, "systemctl", "enable", "docker"); err != nil {
		e := cnerr.Wrap("engine.install.system-prep", err, "run: systemctl enable docker")
		deps.StepFinished(step, e)
		return e
	}

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
	return nil
}

// ─── 4.4 Persistent directories ──────────────────────────────────────────────

func runDirectories(ctx context.Context, deps Deps, res *InstallResult) error {
	_ = ctx
	const step = "directories"
	deps.StepStarted(step)

	// /data base.
	if err := deps.FS.MkdirAll("/data", 0o755); err != nil {
		e := cnerr.Wrap("engine.install.directories", err, "create /data base directory")
		deps.StepFinished(step, e)
		return e
	}

	// Dirs owned 1000:1000.
	for _, dir := range []string{"/data/mongodb", "/data/influxdb2", "/data/redis"} {
		if err := ensureDataDir(deps, dir, 1000, 1000); err != nil {
			deps.StepFinished(step, err)
			return err
		}
	}

	// /data/files — only create if absent; chown tftp:tftp is a named lookup.
	// We use UID/GID 0 as a signal to skip chown in the fake; the real system
	// resolves "tftp" at runtime via the Runner (see bash parity row 5).
	// In practice the install script does: chown tftp:tftp /data/files
	// We emit the command through the Runner so it is testable.
	if err := deps.FS.MkdirAll("/data/files", 0o755); err != nil {
		e := cnerr.Wrap("engine.install.directories", err, "create /data/files")
		deps.StepFinished(step, e)
		return e
	}

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
	return nil
}

// ensureDataDir creates dir with 1000:1000 ownership if it does not exist or
// is empty. If it already contains data, ownership and contents are preserved.
func ensureDataDir(deps Deps, dir string, uid, gid int) error {
	entries, err := deps.FS.ReadDir(dir)
	if err != nil {
		// Dir does not exist — create it.
		if mkErr := deps.FS.MkdirAll(dir, 0o755); mkErr != nil {
			return cnerr.Wrapf(mkErr, "create "+dir, "engine.install.directories: MkdirAll %s", dir)
		}
		if chErr := deps.FS.Chown(dir, uid, gid); chErr != nil {
			return cnerr.Wrapf(chErr, "chown "+dir, "engine.install.directories: Chown %s", dir)
		}
		if chErr := deps.FS.Chmod(dir, 0o755); chErr != nil {
			return cnerr.Wrapf(chErr, "chmod "+dir, "engine.install.directories: Chmod %s", dir)
		}
		return nil
	}
	if len(entries) == 0 {
		// Exists but empty — safe to adjust ownership.
		_ = deps.FS.Chown(dir, uid, gid)
		_ = deps.FS.Chmod(dir, 0o755)
	}
	// Dir with data — do not touch it (parity row 5, AD-4 note in spec).
	return nil
}

// ─── 4.5 vsftpd + tftpd-hpa ─────────────────────────────────────────────────

const (
	vsftpdURL  = "https://cnetworkspace.nyc3.digitaloceanspaces.com/resources/vsftpd.conf"
	tftpdURL   = "https://cnetworkspace.nyc3.digitaloceanspaces.com/resources/tftpd-hpa"
	vsftpdConf = "/etc/vsftpd.conf"
	tftpdConf  = "/etc/default/tftpd-hpa"
)

func runFTPConfig(ctx context.Context, deps Deps, res *InstallResult) {
	const step = "ftp-tftp-config"
	deps.StepStarted(step)

	// vsftpd config.
	vsftpdData, vsftpdWarn := downloadConfig(ctx, deps, vsftpdURL, "vsftpd")
	if vsftpdWarn != "" {
		res.Warnings = append(res.Warnings, vsftpdWarn)
		vsftpdData = compose.DefaultVsftpdConf
	}
	_ = deps.FS.WriteFile(vsftpdConf, vsftpdData, 0o644)
	_, _ = deps.Runner.Run(ctx, "systemctl", "restart", "vsftpd")
	_, _ = deps.Runner.Run(ctx, "systemctl", "enable", "vsftpd")

	// tftpd-hpa config.
	tftpdData, tftpdWarn := downloadConfig(ctx, deps, tftpdURL, "tftpd-hpa")
	if tftpdWarn != "" {
		res.Warnings = append(res.Warnings, tftpdWarn)
		tftpdData = compose.DefaultTftpdHpa
	}
	_ = deps.FS.WriteFile(tftpdConf, tftpdData, 0o644)

	// chown tftp:tftp /data/files (via Runner for testability).
	_, _ = deps.Runner.Run(ctx, "chown", "tftp:tftp", "/data/files")
	_, _ = deps.Runner.Run(ctx, "systemctl", "restart", "tftpd-hpa")
	_, _ = deps.Runner.Run(ctx, "systemctl", "enable", "tftpd-hpa")

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
}

// downloadConfig fetches a remote config file via the HTTPProber seam.
// Returns (data, "") on success or (nil, warningMessage) on failure.
func downloadConfig(ctx context.Context, deps Deps, url, name string) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Sprintf("%s config download failed (bad URL): %v; using embedded defaults", name, err)
	}
	resp, err := deps.Prober.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("%s config download failed: %v; using embedded defaults", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Sprintf("%s config download returned HTTP %d; using embedded defaults", name, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Sprintf("%s config download read error: %v; using embedded defaults", name, err)
	}
	return data, ""
}

// ─── 4.6 backups user ────────────────────────────────────────────────────────

func runBackupsUser(ctx context.Context, deps Deps, res *InstallResult) {
	const step = "backups-user"
	deps.StepStarted(step)

	// Check if "backups" user already exists (via id command).
	out, err := deps.Runner.Run(ctx, "id", "backups")
	if err == nil && len(out) > 0 {
		// User exists — skip.
		deps.Info(step, "backups user already exists — skipping")
		res.Steps = append(res.Steps, StepResult{Name: step, Skipped: true, Note: "user already exists"})
		deps.StepFinished(step, nil)
		return
	}

	// Create the user without a home directory (bash uses useradd -M -d).
	_, _ = deps.Runner.Run(ctx, "useradd", "-M", "-d", "/data/files", "backups")

	// Ensure /data/files exists and set ownership to backups:backups (parity).
	_ = deps.FS.MkdirAll("/data/files", 0o755)
	_, _ = deps.Runner.Run(ctx, "chown", "backups:backups", "/data/files")
	_ = deps.FS.Chmod("/data/files", 0o755)

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
}

// ─── 4.7 .env generation ─────────────────────────────────────────────────────

// randomAlphaNum returns n cryptographically random alphanumeric characters.
func randomAlphaNum(n int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	for i := range buf {
		b := make([]byte, 1)
		for {
			if _, err := rand.Read(b); err != nil {
				return "", err
			}
			// Rejection-sample to avoid modulo bias.
			if int(b[0]) < len(charset)*(256/len(charset)) {
				buf[i] = charset[int(b[0])%len(charset)]
				break
			}
		}
	}
	return string(buf), nil
}

// randomInfluxToken returns a 64-hex-character (32-byte) random token (AD-5).
// The legacy value was a fixed 64-char hex string; we generate a new one.
func randomInfluxToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// runEnvFile creates .env in dir if it does not exist, or reuses it if it does
// (idempotence, AD-5). Returns the InfluxDB token (needed for bucket creation).
func runEnvFile(_ context.Context, deps Deps, dir string, res *InstallResult, opts InstallOptions) (string, error) {
	const step = "env-file"
	deps.StepStarted(step)

	envPath := dir + "/.env"

	// If .env already exists, read token from it and return (idempotence).
	if existing, err := deps.FS.ReadFile(envPath); err == nil {
		token := extractEnvVar(string(existing), "INFLUXDB_TOKEN")
		deps.Info(step, ".env already exists — preserving credentials")
		res.Steps = append(res.Steps, StepResult{Name: step, Skipped: true, Note: ".env preserved"})
		res.ReusedComponents = appendUnique(res.ReusedComponents, ".env")
		deps.StepFinished(step, nil)
		return token, nil
	}

	// Generate credentials (AD-5).
	influxToken, err := randomInfluxToken()
	if err != nil {
		e := cnerr.Wrap("engine.install.env-file", err, "generate InfluxDB token")
		deps.StepFinished(step, e)
		return "", e
	}
	mongoPwd, err := randomAlphaNum(32)
	if err != nil {
		e := cnerr.Wrap("engine.install.env-file", err, "generate MongoDB password")
		deps.StepFinished(step, e)
		return "", e
	}
	redisPwd, err := randomAlphaNum(32)
	if err != nil {
		e := cnerr.Wrap("engine.install.env-file", err, "generate Redis password")
		deps.StepFinished(step, e)
		return "", e
	}

	// Build the .env content (exact variable set from the bash script + spec).
	content := fmt.Sprintf(`# InfluxDB Configuration
INFLUXDB_TOKEN=%s
DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=%s

# MongoDB Configuration (autogenerado - NO COMPARTIR)
MONGODB_INITDB_ROOT_USERNAME=cnetwork_admin
MONGODB_INITDB_ROOT_PASSWORD=%s

# Redis Configuration (autogenerado - NO COMPARTIR)
REDIS_PASSWORD=%s

# C-Network API Configuration (configurar según necesidad)
CNETWORK_API_URL=%s
CNETWORK_API_TOKEN=%s
`, influxToken, influxToken, mongoPwd, redisPwd, opts.apiURL(), opts.apiToken())

	// Write with chmod 600.
	if err := deps.FS.WriteFile(envPath, []byte(content), 0o600); err != nil {
		e := cnerr.Wrap("engine.install.env-file", err, "write .env — check directory permissions")
		deps.StepFinished(step, e)
		return "", e
	}

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
	return influxToken, nil
}

// extractEnvVar reads a VAR=value line from env content. Returns "" if absent.
func extractEnvVar(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

// appendUnique appends s to slice only when not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// ─── 4.8 Self-signed certificates ────────────────────────────────────────────

// certDirs maps the friendly name to the directory path (relative to installDir).
var certDirs = map[string]string{
	"backend":  "c-network-agent-back/certs",
	"frontend": "c-network-agent-front/certs",
}

func runCerts(ctx context.Context, deps Deps, dir string, res *InstallResult) {
	const step = "certificates"
	deps.StepStarted(step)

	allSkipped := true
	for _, name := range []string{"backend", "frontend"} {
		rel := certDirs[name]
		certDir := dir + "/" + rel
		certPath := certDir + "/cert.pem"
		keyPath := certDir + "/key.pem"

		// Skip if both files already exist.
		_, certErr := deps.FS.Stat(certPath)
		_, keyErr := deps.FS.Stat(keyPath)
		if certErr == nil && keyErr == nil {
			deps.Info(step, name+" certificates already exist — skipping")
			res.ReusedComponents = appendUnique(res.ReusedComponents, rel)
			continue
		}
		allSkipped = false

		_ = deps.FS.MkdirAll(certDir, 0o755)

		// Generate via openssl CLI through CommandRunner (safe default per design).
		// Parameters match bash parity row 10 and spec exactly.
		_, err := deps.Runner.Run(ctx, "openssl", "req",
			"-x509", "-newkey", "rsa:4096", "-nodes",
			"-keyout", keyPath,
			"-out", certPath,
			"-days", "365",
			"-subj", "/C=US/ST=State/L=City/O=Crenein/OU=IT/CN=localhost",
			"-addext", "subjectAltName=DNS:localhost,DNS:*.localhost,IP:127.0.0.1,IP:0.0.0.0",
		)
		if err != nil {
			deps.Warn(step, fmt.Sprintf("%s cert generation failed: %v", name, err))
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("certificate generation failed for %s: %v", name, err))
			continue
		}

		_ = deps.FS.Chmod(certPath, 0o644)
		_ = deps.FS.Chmod(keyPath, 0o600)
	}

	if allSkipped {
		res.Steps = append(res.Steps, StepResult{Name: step, Skipped: true, Note: "all certs already present"})
	} else {
		res.Steps = append(res.Steps, StepResult{Name: step})
	}
	deps.StepFinished(step, nil)
}

// ─── 4.9 Stack up + service verification ─────────────────────────────────────

// requiredServices is the ordered list of compose services that must be running.
var requiredServices = []string{"mongodb", "influxdb", "redis", "agent", "frontend"}

func runStack(ctx context.Context, deps Deps, dir, mongoImage string, res *InstallResult, opts InstallOptions) error {
	const step = "stack-up"
	deps.StepStarted(step)

	composeFile := dir + "/docker-compose.yml"

	// Render compose template.
	composeBytes, err := compose.Render(compose.ComposeParams{MongoImage: mongoImage})
	if err != nil {
		e := cnerr.Wrap("engine.install.stack-up", err, "render docker-compose.yml template")
		deps.StepFinished(step, e)
		return e
	}
	if err := deps.FS.WriteFile(composeFile, composeBytes, 0o644); err != nil {
		e := cnerr.Wrap("engine.install.stack-up", err, "write docker-compose.yml")
		deps.StepFinished(step, e)
		return e
	}

	// docker compose up -d.
	if err := deps.Client.ComposeUp(ctx, composeFile, dockerx.ComposeUpOptions{Detach: true}); err != nil {
		e := cnerr.Wrap("engine.install.stack-up", err,
			"run: docker compose up -d — check docker logs for details")
		deps.StepFinished(step, e)
		return e
	}
	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})

	// Service verification.
	return runServiceVerification(ctx, deps, composeFile, res, opts)
}

func runServiceVerification(ctx context.Context, deps Deps, composeFile string, res *InstallResult, opts InstallOptions) error {
	const step = "service-verification"
	deps.StepStarted(step)

	// Poll for all services to reach running state.
	verifyTimeout := 60 * time.Second
	if opts.ServiceVerifyTimeout > 0 {
		verifyTimeout = opts.ServiceVerifyTimeout
	}
	deadline := time.Now().Add(verifyTimeout)
	interval := 3 * time.Second

	for time.Now().Before(deadline) {
		containers, err := deps.Client.ComposePs(ctx, composeFile, requiredServices)
		if err != nil {
			deps.Warn(step, "compose ps error: "+err.Error())
		}

		// Build running map.
		running := make(map[string]bool)
		for _, c := range containers {
			if c.Running {
				running[c.Service] = true
			}
		}

		// Check all required services.
		allRunning := true
		for _, svc := range requiredServices {
			if !running[svc] {
				allRunning = false
				break
			}
		}
		if allRunning {
			// Record status.
			for _, svc := range requiredServices {
				res.Services = append(res.Services, ServiceStatus{Service: svc, Running: true})
			}
			deps.StepFinished(step, nil)
			res.Steps = append(res.Steps, StepResult{Name: step})
			return nil
		}

		// Brief sleep before next poll (we use a goroutine-safe select on ctx).
		select {
		case <-ctx.Done():
			return cnerr.Wrap("engine.install.service-verification", ctx.Err(),
				"install cancelled; run: docker compose ps to check service status")
		case <-time.After(interval):
		}
	}

	// Timeout — find the failing service.
	containers, _ := deps.Client.ComposePs(ctx, composeFile, requiredServices)
	running := make(map[string]bool)
	for _, c := range containers {
		if c.Running {
			running[c.Service] = true
		}
	}
	for _, svc := range requiredServices {
		res.Services = append(res.Services, ServiceStatus{Service: svc, Running: running[svc]})
		if !running[svc] {
			e := cnerr.New(
				fmt.Sprintf("engine.install.service-verification: service %q did not reach running state", svc),
				fmt.Sprintf("inspect logs: docker compose logs %s", svc),
			)
			deps.StepFinished(step, e)
			return e
		}
	}

	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step})
	return nil
}

// ─── 4.10 Post-install ───────────────────────────────────────────────────────

func runPostInstall(
	ctx context.Context,
	deps Deps,
	dir, composeFile, influxToken, adminEmail, adminPassword string,
	res *InstallResult,
	opts InstallOptions,
) {
	// InfluxDB health gate.
	runInfluxHealthWait(ctx, deps, res, opts)

	// Admin user creation.
	runAdminUserCreation(ctx, deps, adminEmail, adminPassword, res, opts)

	// InfluxDB bucket creation (4-method fallback chain).
	runInfluxBuckets(ctx, deps, composeFile, influxToken, res, opts)
}

// retryInterval returns the retry interval to use (3s default, or opts.RetryInterval when non-zero).
func retryInterval(opts InstallOptions) time.Duration {
	if opts.RetryInterval != 0 {
		return opts.RetryInterval
	}
	return 3 * time.Second
}

// runInfluxHealthWait polls http://localhost:8086/health until "status":"pass"
// or 60 s timeout, then continues (spec: warn + continue on timeout).
func runInfluxHealthWait(ctx context.Context, deps Deps, res *InstallResult, opts InstallOptions) {
	const step = "influx-health"
	deps.StepStarted(step)

	deadline := time.Now().Add(60 * time.Second)
	interval := retryInterval(opts)

poll:
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8086/health", nil)
		if err != nil {
			break
		}
		resp, err := deps.Prober.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), `"status":"pass"`) {
				deps.Info(step, "InfluxDB health check passed")
				deps.StepFinished(step, nil)
				res.Steps = append(res.Steps, StepResult{Name: step})
				return
			}
		}
		select {
		case <-ctx.Done():
			break poll
		case <-time.After(interval):
		}
	}

	// Timeout — warn and continue (spec: bucket creation still attempts).
	warn := "InfluxDB did not pass health check within 60s; manual check: GET http://localhost:8086/health"
	res.Warnings = append(res.Warnings, warn)
	deps.Warn(step, warn)
	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step, Note: "health timeout — continuing"})
}

// runAdminUserCreation calls POST /api/v1/admins/register with up to 3 retries.
func runAdminUserCreation(ctx context.Context, deps Deps, email, password string, res *InstallResult, opts InstallOptions) {
	const step = "admin-user"
	deps.StepStarted(step)

	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://localhost:8000/api/v1/admins/register",
			strings.NewReader(body))
		if err != nil {
			break
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := deps.Prober.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				deps.Info(step, fmt.Sprintf("admin user created (attempt %d)", attempt))
				deps.StepFinished(step, nil)
				res.Steps = append(res.Steps, StepResult{Name: step})
				return
			}
		}
		if attempt < 3 {
			ri := retryInterval(opts)
			deps.Info(step, fmt.Sprintf("attempt %d failed — retrying in %s", attempt, ri))
			select {
			case <-ctx.Done():
				break
			case <-time.After(ri):
			}
		}
	}

	// All attempts exhausted — warn and continue.
	// NOTE: password is intentionally NOT logged. Retrieve it from the .env file.
	warn := fmt.Sprintf(
		"admin user creation failed after 3 attempts; create manually:\n"+
			"  curl -k -X POST https://localhost:8000/api/v1/admins/register \\\n"+
			"    -H 'Content-Type: application/json' \\\n"+
			"    -d '{\"email\":\"%s\",\"password\":\"<ADMIN_PASSWORD>\"}'"+
			"\n  (ADMIN_PASSWORD is stored in the .env file — do not log or share it)",
		email)
	res.Warnings = append(res.Warnings, warn)
	deps.Warn(step, warn)
	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step, Note: "failed — see warnings"})
}

// ─── 4.10 InfluxDB bucket creation (4-method fallback chain) ─────────────────

// bucketNames are the two critical buckets the fping and devices pollers use.
var bucketNames = []string{"fping", "devices"}

// influxContainerName is the name docker assigns to the influxdb container.
const influxContainerName = "srv-influxdb-1"

func runInfluxBuckets(ctx context.Context, deps Deps, composeFile, influxToken string, res *InstallResult, opts InstallOptions) {
	const step = "influx-buckets"
	deps.StepStarted(step)

	// Method 1: REST GET /api/v2/orgs — up to 10 attempts, retryInterval apart.
	orgID := resolveOrgIDviaREST(ctx, deps, influxToken, 10, opts)

	// Method 2: CLI org list — up to 5 attempts (only when method 1 failed).
	if orgID == "" {
		deps.Warn(step, "REST org lookup failed; falling back to influx CLI org list")
		orgID = resolveOrgIDviaCLI(ctx, deps, composeFile, influxToken, 5, opts)
	}

	// Method 3: CLI bucket create (direct, no org ID needed).
	if orgID == "" {
		deps.Warn(step, "CLI org list failed; attempting direct CLI bucket create")
		allOK := true
		for _, bucket := range bucketNames {
			if !createBucketViaCLI(ctx, deps, composeFile, influxToken, bucket) {
				allOK = false
			}
		}
		if allOK {
			deps.StepFinished(step, nil)
			res.Steps = append(res.Steps, StepResult{Name: step, Note: "method: CLI bucket create"})
			return
		}
		// Fall through to method 4.
	}

	// Method 4: REST POST /api/v2/buckets — up to 5 attempts per bucket.
	if orgID != "" {
		allOK := true
		for _, bucket := range bucketNames {
			if !createBucketViaREST(ctx, deps, influxToken, orgID, bucket, 5, opts) {
				allOK = false
			}
		}
		if allOK {
			deps.StepFinished(step, nil)
			res.Steps = append(res.Steps, StepResult{Name: step, Note: "method: REST bucket create"})
			return
		}
	}

	// Entire chain failed — warn with manual instructions.
	warn := "InfluxDB bucket creation failed by all methods. Create manually:\n" +
		"  docker exec " + influxContainerName + " influx bucket create -n fping -o crenein -t <token>\n" +
		"  docker exec " + influxContainerName + " influx bucket create -n devices -o crenein -t <token>\n" +
		"  OR via UI: http://localhost:8086 → Data → Buckets → Create Bucket"
	res.Warnings = append(res.Warnings, warn)
	deps.Warn(step, warn)
	deps.StepFinished(step, nil)
	res.Steps = append(res.Steps, StepResult{Name: step, Note: "all methods failed — see warnings"})
}

// resolveOrgIDviaREST calls GET /api/v2/orgs up to maxAttempts times to extract
// the "crenein" org ID. Returns "" when all attempts fail.
func resolveOrgIDviaREST(ctx context.Context, deps Deps, token string, maxAttempts int, opts InstallOptions) string {
	for i := 0; i < maxAttempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"http://localhost:8086/api/v2/orgs", nil)
		if err != nil {
			return ""
		}
		req.Header.Set("Authorization", "Token "+token)

		resp, err := deps.Prober.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if id := parseOrgID(body); id != "" {
				return id
			}
		}
		if i < maxAttempts-1 {
			ri := retryInterval(opts)
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(ri):
			}
		}
	}
	return ""
}

// orgsResponse is used to parse the InfluxDB GET /api/v2/orgs response.
type orgsResponse struct {
	Orgs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"orgs"`
}

// parseOrgID extracts the first org ID from the JSON body using native JSON parsing.
func parseOrgID(body []byte) string {
	var r orgsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ""
	}
	if len(r.Orgs) > 0 && r.Orgs[0].ID != "" {
		return r.Orgs[0].ID
	}
	return ""
}

// resolveOrgIDviaCLI runs `influx org list` in the influxdb container up to
// maxAttempts times and parses the first hex-16 org ID.
func resolveOrgIDviaCLI(ctx context.Context, deps Deps, composeFile, token string, maxAttempts int, opts InstallOptions) string {
	for i := 0; i < maxAttempts; i++ {
		out, err := deps.Client.ComposeExec(ctx, composeFile, dockerx.ExecOptions{
			Service: "influxdb",
			Cmd:     []string{"influx", "org", "list", "--skip-verify", "--token", token},
		})
		if err == nil {
			if id := parseOrgIDFromCLI(string(out)); id != "" {
				return id
			}
		}
		if i < maxAttempts-1 {
			ri := retryInterval(opts)
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(ri):
			}
		}
	}
	return ""
}

// parseOrgIDFromCLI parses a 16-char hex org ID from influx org list output.
// The CLI output format is: "ID\t\t\tName\n<16hexchars>\t\t\tcrenein".
func parseOrgIDFromCLI(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 1 && len(fields[0]) == 16 && isHex(fields[0]) {
			return fields[0]
		}
	}
	return ""
}

// isHex reports whether s consists only of hex characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// createBucketViaCLI runs influx bucket create in the container.
func createBucketViaCLI(ctx context.Context, deps Deps, composeFile, token, bucket string) bool {
	_, err := deps.Client.ComposeExec(ctx, composeFile, dockerx.ExecOptions{
		Service: "influxdb",
		Cmd: []string{
			"influx", "bucket", "create",
			"--skip-verify",
			"--token", token,
			"--org", "crenein",
			"--name", bucket,
			"--retention", "0",
		},
	})
	return err == nil
}

// createBucketViaREST posts to POST /api/v2/buckets up to maxAttempts times.
func createBucketViaREST(ctx context.Context, deps Deps, token, orgID, bucket string, maxAttempts int, opts InstallOptions) bool {
	type retentionRule struct {
		Type         string `json:"type"`
		EverySeconds int    `json:"everySeconds"`
	}
	type createBucketReq struct {
		OrgID          string          `json:"orgID"`
		Name           string          `json:"name"`
		RetentionRules []retentionRule `json:"retentionRules"`
	}
	payload, _ := json.Marshal(createBucketReq{
		OrgID: orgID,
		Name:  bucket,
		RetentionRules: []retentionRule{
			{Type: "expire", EverySeconds: 0},
		},
	})

	for i := 0; i < maxAttempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://localhost:8086/api/v2/buckets",
			bytes.NewReader(payload))
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Token "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := deps.Prober.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			// Success when name appears in response, or bucket already exists.
			if strings.Contains(string(body), `"name":"`+bucket+`"`) {
				return true
			}
			lc := strings.ToLower(string(body))
			if strings.Contains(lc, "already exists") || strings.Contains(lc, "conflict") {
				return true
			}
		}
		if i < maxAttempts-1 {
			ri := retryInterval(opts)
			select {
			case <-ctx.Done():
				return false
			case <-time.After(ri):
			}
		}
	}
	return false
}

// ─── 4.12 Access summary ─────────────────────────────────────────────────────

func buildAccessSummary(dir, adminEmail string, res *InstallResult) {
	res.AccessSummary = []AccessEntry{
		{Label: "Backend API (HTTPS)", Value: "https://<VM_IP>:8000"},
		{Label: "Frontend (HTTPS)", Value: "https://<VM_IP>:443"},
		{Label: "Frontend (HTTP)", Value: "http://<VM_IP>:80"},
		{Label: "Admin credentials", Value: adminEmail + " / **** (see .env)"},
		{Label: "InfluxDB", Value: "http://<VM_IP>:8086 (admin/adminpassword)"},
		{Label: "Persistent data", Value: "/data/{mongodb,influxdb2,redis,files}"},
		{Label: "Backend certificates", Value: dir + "/c-network-agent-back/certs/ (365 days)"},
		{Label: "Frontend certificates", Value: dir + "/c-network-agent-front/certs/ (365 days)"},
		{Label: ".env location", Value: dir + "/.env (chmod 600 — do NOT share)"},
	}
}
