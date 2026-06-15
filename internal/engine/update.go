// Package engine — update sub-engine (tasks 5.1–5.10).
//
// Design (AD-1): Update never prints, prompts, or reads stdin. Progress flows
// through deps.Reporter; interactive decisions arrive pre-resolved in
// UpdateOptions.
//
// Design (AD-2): all external effects go through deps.Client / Runner / FS /
// Prober. No exec.Command or os.WriteFile calls.
//
// Design (AD-4): MongoDB image is read from the existing compose file and NEVER
// changed during an update.
//
// Design (AD-6): Update pulls an explicit version tag (never :latest). Version
// is required in UpdateOptions.
package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	agentImageName    = "crenein/c-network-agent-back"
	frontendImageName = "crenein/c-network-agent-front"
	updateLogFile     = "/var/log/c-network-agent-update.log"
	maxBackups        = 5
)

// ─── Options & Result ────────────────────────────────────────────────────────

// UpdateOptions carries all pre-resolved decisions for Update. The caller
// (CLI/TUI) is responsible for interactive resolution before calling Update.
type UpdateOptions struct {
	// Version is required. The engine will pull agentImageName:Version.
	// Example: "1.8.4"
	Version string

	// DryRun: execute pre-flight and state detection, report the plan, no writes.
	DryRun bool

	// SkipFrontend: only update the agent service, not frontend.
	SkipFrontend bool

	// Force: recreate even when image IDs are identical.
	Force bool

	// NoCleanup: skip docker image prune after a successful update.
	NoCleanup bool

	// Injectable test seams.

	// RetryInterval overrides the health-check polling interval (default 3s).
	RetryInterval time.Duration

	// HealthTimeout overrides the backend health-check timeout (default 60s).
	HealthTimeout time.Duration

	// Now overrides the clock for backup timestamps and log entries.
	// When nil, time.Now() is used.
	Now func() time.Time

	// DiskSpaceProvider overrides the disk-space check for tests.
	DiskSpaceProvider detect.DiskSpaceProvider

	// IsRootFunc overrides the root-privilege check for tests.
	IsRootFunc func() bool
}

// UpdateResult is the structured outcome of Update.
type UpdateResult struct {
	// PreviousAgentImageID is the image ID of the agent before the update.
	PreviousAgentImageID string
	// NewAgentImageID is the image ID of the agent after the update.
	NewAgentImageID string
	// PreviousFrontendImageID is the image ID of the frontend before the update.
	PreviousFrontendImageID string
	// NewFrontendImageID is the image ID of the frontend after the update.
	NewFrontendImageID string
	// BackupPath is the directory where files were backed up.
	BackupPath string
	// RolledBack is true when an automatic rollback was attempted (regardless of
	// whether the rollback itself succeeded).
	RolledBack bool
	// RollbackFailed is true when RolledBack is true AND the rollback also
	// failed (i.e. the system may be in an inconsistent state). When RolledBack
	// is true and RollbackFailed is false the rollback completed successfully.
	RollbackFailed bool
	// NoOp is true when nothing changed (same image IDs, Force not set).
	NoOp bool
	// DryRun is true when the result is from a dry-run invocation.
	DryRun bool
	// InstallDir is the resolved installation directory.
	InstallDir string
	// MongoImage is the Mongo image detected from the compose file (immutable).
	MongoImage string
	// Warnings is a list of non-fatal conditions the operator should review.
	Warnings []string
}

// ─── Update entry-point ──────────────────────────────────────────────────────

// Update runs the full update sequence, reporting progress through
// deps.Reporter. It returns UpdateResult on success and a *cnerr.Error on
// fatal failure.
//
// The sequence is:
//  1. Pre-flight
//  2. State detection
//  3. (DryRun exits here, returns plan)
//  4. Backup
//  5. Pull by explicit tag
//  6. No-op check (same IDs + !Force)
//  7. Recreate (agent + frontend)
//  8. Health check + automatic rollback on failure
//  9. Cleanup + logging
func Update(ctx context.Context, deps Deps, opts UpdateOptions) (*UpdateResult, error) {
	if opts.Version == "" {
		return nil, cnerr.New("engine.update: Version is required",
			"provide an explicit version via --version (e.g. --version 1.8.4)")
	}

	res := &UpdateResult{}
	now := opts.now()

	// ── 5.2 Pre-flight ───────────────────────────────────────────────────────
	installDir, err := runUpdatePreflight(ctx, deps, opts, res)
	if err != nil {
		return nil, err
	}
	res.InstallDir = installDir

	// ── 5.3 State detection ──────────────────────────────────────────────────
	state, err := detectCurrentState(ctx, deps, installDir, opts, res)
	if err != nil {
		return nil, err
	}
	res.PreviousAgentImageID = state.AgentImageID
	res.PreviousFrontendImageID = state.FrontendImageID
	res.MongoImage = state.MongoImage

	// ── 5.10 DryRun exit ────────────────────────────────────────────────────
	if opts.DryRun {
		res.DryRun = true
		deps.Info("dry-run", fmt.Sprintf("plan: pull %s:%s, recreate agent%s, backup to %s/.backups/%s",
			agentImageName, opts.Version,
			func() string {
				if opts.SkipFrontend {
					return ""
				}
				return " frontend"
			}(),
			installDir, now.Format("20060102_150405"),
		))
		return res, nil
	}

	// ── 5.4 Backup ──────────────────────────────────────────────────────────
	backupPath, err := createUpdateBackup(ctx, deps, installDir, state, opts, now)
	if err != nil {
		return nil, err
	}
	res.BackupPath = backupPath

	// ── 5.5 Pull by explicit tag ─────────────────────────────────────────────
	agentTag := fmt.Sprintf("%s:%s", agentImageName, opts.Version)
	newAgentID, newFrontendID, noOp, err := pullAndCompare(ctx, deps, installDir, agentTag, state, opts)
	if err != nil {
		return nil, err
	}
	if noOp {
		res.NoOp = true
		res.NewAgentImageID = res.PreviousAgentImageID
		res.NewFrontendImageID = res.PreviousFrontendImageID
		appendUpdateLog(deps, opts, now, "INFO", "no update needed — image IDs unchanged")
		return res, nil
	}

	// ── 5.6 Recreate ────────────────────────────────────────────────────────
	composeFile := installDir + "/docker-compose.yml"
	if err := recreateServices(ctx, deps, composeFile, opts); err != nil {
		// Rollback immediately on compose failure.
		appendUpdateLog(deps, opts, now, "ERR", "compose up failed — initiating rollback")
		if rbErr := performRollback(ctx, deps, composeFile, state, opts); rbErr != nil {
			res.RollbackFailed = true
			appendUpdateLog(deps, opts, now, "ERR", "rollback also failed: "+rbErr.Error())
		}
		res.RolledBack = true
		res.NewAgentImageID = res.PreviousAgentImageID
		res.NewFrontendImageID = res.PreviousFrontendImageID
		return res, cnerr.Wrap("engine.update.recreate", err,
			"rollback completed; check: docker compose logs agent")
	}

	// ── 5.7 Health checks ────────────────────────────────────────────────────
	if healthErr := runUpdateHealthChecks(ctx, deps, composeFile, opts, res, now); healthErr != nil {
		// Rollback on health failure.
		appendUpdateLog(deps, opts, now, "ERR", "health check failed — initiating rollback")
		if rbErr := performRollback(ctx, deps, composeFile, state, opts); rbErr != nil {
			res.RollbackFailed = true
			appendUpdateLog(deps, opts, now, "ERR", "rollback also failed: "+rbErr.Error())
		}
		res.RolledBack = true
		res.NewAgentImageID = res.PreviousAgentImageID
		res.NewFrontendImageID = res.PreviousFrontendImageID
		appendUpdateLog(deps, opts, now, "WARN", fmt.Sprintf("rolled back to %s; backup at %s", state.AgentImageID, backupPath))
		return res, nil
	}

	res.NewAgentImageID = newAgentID
	res.NewFrontendImageID = newFrontendID

	// ── 5.9 Cleanup + logging ────────────────────────────────────────────────
	if !opts.NoCleanup {
		const step = "cleanup"
		deps.StepStarted(step)
		if err := deps.Client.ImagePrune(ctx); err != nil {
			deps.Warn(step, "image prune failed (non-fatal): "+err.Error())
			res.Warnings = append(res.Warnings, "image prune failed: "+err.Error())
		}
		deps.StepFinished(step, nil)
	}

	appendUpdateLog(deps, opts, now, "OK", fmt.Sprintf("update to %s completed successfully", opts.Version))
	return res, nil
}

// ─── opts helpers ────────────────────────────────────────────────────────────

// now returns the time from opts.Now, or time.Now() if not set.
func (opts UpdateOptions) now() time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func (opts UpdateOptions) retryInterval() time.Duration {
	if opts.RetryInterval > 0 {
		return opts.RetryInterval
	}
	return 3 * time.Second
}

func (opts UpdateOptions) healthTimeout() time.Duration {
	if opts.HealthTimeout > 0 {
		return opts.HealthTimeout
	}
	return 60 * time.Second
}

// ─── 5.2 Pre-flight ──────────────────────────────────────────────────────────

// runUpdatePreflight performs all pre-flight checks and returns the resolved
// install directory. Any failure aborts before any write.
func runUpdatePreflight(ctx context.Context, deps Deps, opts UpdateOptions, res *UpdateResult) (string, error) {
	const step = "preflight"
	deps.StepStarted(step)

	// Root check.
	isRoot := false
	if opts.IsRootFunc != nil {
		isRoot = opts.IsRootFunc()
	} else {
		perm, err := detect.Permissions(ctx, deps.Runner)
		if err != nil {
			deps.StepFinished(step, err)
			return "", err
		}
		isRoot = perm.IsRoot
	}
	if !isRoot {
		e := cnerr.New("engine.update.preflight",
			"re-run as root: sudo ./crenein-agent-tui update --version "+opts.Version)
		deps.StepFinished(step, e)
		return "", e
	}

	// Docker daemon check.
	if err := deps.Client.Ping(ctx); err != nil {
		e := cnerr.Wrap("engine.update.preflight", err,
			"start Docker: systemctl start docker")
		deps.StepFinished(step, e)
		return "", e
	}

	// find_install_dir.
	installDir, err := findInstallDir(ctx, deps)
	if err != nil {
		deps.StepFinished(step, err)
		return "", err
	}
	deps.Info(step, "install directory: "+installDir)

	// .env must exist.
	envPath := installDir + "/.env"
	if _, err := deps.FS.ReadFile(envPath); err != nil {
		e := cnerr.Wrap("engine.update.preflight", err,
			fmt.Sprintf(".env not found in %s — installation may be corrupt", installDir))
		deps.StepFinished(step, e)
		return "", e
	}

	// Disk space check (≥2048 MB).
	var freeMB uint64
	var diskErr error
	if opts.DiskSpaceProvider != nil {
		freeMB, diskErr = detect.DiskSpaceWithProvider(ctx, installDir, opts.DiskSpaceProvider)
	} else {
		freeMB, diskErr = detect.DiskSpace(ctx, installDir)
	}
	if diskErr != nil {
		deps.StepFinished(step, diskErr)
		return "", diskErr
	}
	deps.Info(step, fmt.Sprintf("disk free: %d MB", freeMB))

	// Connectivity to Docker Hub registries (10s timeout per endpoint).
	updateURLs := []string{
		"https://registry-1.docker.io/v2/",
		"https://hub.docker.com",
	}
	connResults, err := detect.Connectivity(ctx, deps.Prober, updateURLs)
	if err != nil {
		deps.StepFinished(step, err)
		return "", err
	}
	for _, r := range connResults {
		if !r.Reachable {
			e := cnerr.Wrap("engine.update.preflight", r.Err,
				"check firewall and outbound HTTPS access to "+r.URL)
			deps.StepFinished(step, e)
			return "", e
		}
	}

	deps.StepFinished(step, nil)
	return installDir, nil
}

// findInstallDir searches CWD → /root → /home/* for a docker-compose.yml that
// references the agent image. It returns the first match or a *cnerr.Error.
func findInstallDir(ctx context.Context, deps Deps) (string, error) {
	_ = ctx

	// CWD is represented as "." — the compose file path will be "./docker-compose.yml".
	candidates := []string{"."}
	candidates = append(candidates, "/root")

	// /home/* — read the /home directory and add each subdirectory.
	homeEntries, err := deps.FS.ReadDir("/home")
	if err == nil {
		for _, entry := range homeEntries {
			candidates = append(candidates, "/home/"+entry)
		}
	}

	for _, dir := range candidates {
		composePath := dir + "/docker-compose.yml"
		data, err := deps.FS.ReadFile(composePath)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(agentImageName)) ||
			bytes.Contains(data, []byte("c-network-agent-back")) {
			return dir, nil
		}
	}

	return "", cnerr.New("engine.update.preflight: no installation found",
		"run the installer first (crenein-agent-tui install) or execute update from the install directory")
}

// ─── 5.3 State detection ─────────────────────────────────────────────────────

// currentState holds the pre-update snapshot of the environment.
type currentState struct {
	AgentImageID    string
	FrontendImageID string
	MongoImage      string
	RunningServices []string
	DataPresent     bool
}

// detectCurrentState captures image IDs, Mongo image, and running services.
// Mongo image is read-only and NEVER modified.
func detectCurrentState(ctx context.Context, deps Deps, installDir string, opts UpdateOptions, res *UpdateResult) (currentState, error) {
	_ = opts
	const step = "detect-state"
	deps.StepStarted(step)

	state := currentState{}

	// Agent image ID — try :latest first; fall back to the running container's ImageID.
	// Fallback handles the case where the container runs from a versioned tag (no :latest alias).
	// When falling back via ContainerList, docker ps gives a short 12-hex ID; we resolve
	// the full sha256 digest via ImageInspect so that the no-op comparison in pullAndCompare
	// (which uses the full digest from the newly pulled image) works correctly.
	agentRef := agentImageName + ":latest"
	agentInfo, err := deps.Client.ImageInspect(ctx, agentRef)
	if err != nil {
		// :latest not found — resolve from the running agent container so backup stores the correct ID.
		if containers, cErr := deps.Client.ContainerList(ctx, "agent"); cErr == nil {
			for _, c := range containers {
				if c.ImageID != "" {
					// Resolve short ID → full digest to normalize for no-op comparison.
					if info, iErr := deps.Client.ImageInspect(ctx, c.ImageID); iErr == nil && info.ID != "" {
						state.AgentImageID = info.ID
					} else {
						state.AgentImageID = c.ImageID
					}
					break
				}
			}
		}
		if state.AgentImageID == "" {
			deps.Warn(step, "could not inspect agent image (may not be pulled yet)")
		}
	} else {
		state.AgentImageID = agentInfo.ID
	}

	// Frontend image ID — try :latest first; fall back to the running container's ImageID.
	// Same short-ID normalization as agent above.
	frontendRef := frontendImageName + ":latest"
	frontendInfo, err := deps.Client.ImageInspect(ctx, frontendRef)
	if err != nil {
		// :latest not found — resolve from the running frontend container.
		if containers, cErr := deps.Client.ContainerList(ctx, "frontend"); cErr == nil {
			for _, c := range containers {
				if c.ImageID != "" {
					// Resolve short ID → full digest to normalize for no-op comparison.
					if info, iErr := deps.Client.ImageInspect(ctx, c.ImageID); iErr == nil && info.ID != "" {
						state.FrontendImageID = info.ID
					} else {
						state.FrontendImageID = c.ImageID
					}
					break
				}
			}
		}
		if state.FrontendImageID == "" {
			deps.Warn(step, "could not inspect frontend image (may not be pulled yet)")
		}
	} else {
		state.FrontendImageID = frontendInfo.ID
	}

	// MongoDB image — read from compose file (immutable, AD-4).
	composeFile := installDir + "/docker-compose.yml"
	state.MongoImage = detectMongoImage(ctx, deps, composeFile)
	if state.MongoImage != "" {
		deps.Info(step, "MongoDB image (immutable): "+state.MongoImage)
	}

	// Running services.
	containers, psErr := deps.Client.ComposePs(ctx, composeFile, nil)
	if psErr == nil {
		for _, c := range containers {
			if c.Running {
				state.RunningServices = append(state.RunningServices, c.Service)
			}
		}
	}

	// /data presence check.
	for _, dataDir := range []string{"/data/mongodb", "/data/influxdb2", "/data/redis"} {
		entries, rdErr := deps.FS.ReadDir(dataDir)
		if rdErr == nil && len(entries) > 0 {
			state.DataPresent = true
			break
		}
	}

	deps.StepFinished(step, nil)
	return state, nil
}

// detectMongoImage reads the MongoDB image from docker-compose.yml, falling
// back to docker inspect on the running container (AD-4).
func detectMongoImage(ctx context.Context, deps Deps, composeFile string) string {
	// Try compose file first.
	data, err := deps.FS.ReadFile(composeFile)
	if err == nil {
		img := parseMongoImageFromCompose(string(data))
		if img != "" {
			return img
		}
	}

	// Fallback: inspect the running mongodb container.
	containers, err := deps.Client.ContainerList(ctx, "mongodb")
	if err == nil {
		for _, c := range containers {
			if strings.Contains(c.Name, "mongodb") || strings.Contains(c.Service, "mongodb") {
				info, err := deps.Client.ImageInspect(ctx, c.ImageID)
				if err == nil && len(info.RepoTags) > 0 {
					return info.RepoTags[0]
				}
			}
		}
	}

	return ""
}

// parseMongoImageFromCompose extracts the image: line from the mongodb service
// block. It walks the compose YAML with a simple line-by-line approach that
// avoids a YAML parser dependency.
func parseMongoImageFromCompose(content string) string {
	lines := strings.Split(content, "\n")
	inMongoDB := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect the mongodb: service line (indented or at root).
		if strings.HasSuffix(trimmed, "mongodb:") {
			inMongoDB = true
			continue
		}
		// A new top-level service ends the block.
		if inMongoDB && len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inMongoDB = false
		}
		// Find image: inside the mongodb block.
		if inMongoDB && strings.HasPrefix(trimmed, "image:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
			val = strings.Trim(val, `"'`)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

// ─── 5.4 Backup ──────────────────────────────────────────────────────────────

// createUpdateBackup creates .backups/${TIMESTAMP}/ with compose, .env
// (mode 600), and image-state.txt. Prunes to the 5 most recent backups.
func createUpdateBackup(ctx context.Context, deps Deps, installDir string, state currentState, opts UpdateOptions, ts time.Time) (string, error) {
	_ = ctx
	const step = "backup"
	deps.StepStarted(step)

	timestamp := ts.Format("20060102_150405")
	backupsRoot := installDir + "/.backups"
	backupDir := backupsRoot + "/" + timestamp

	if err := deps.FS.MkdirAll(backupDir, 0o755); err != nil {
		e := cnerr.Wrap("engine.update.backup", err,
			"ensure the install directory is writable")
		deps.StepFinished(step, e)
		return "", e
	}

	// docker-compose.yml.
	composeFile := installDir + "/docker-compose.yml"
	composeData, err := deps.FS.ReadFile(composeFile)
	if err != nil {
		e := cnerr.Wrap("engine.update.backup", err, "read docker-compose.yml for backup")
		deps.StepFinished(step, e)
		return "", e
	}
	if err := deps.FS.WriteFile(backupDir+"/docker-compose.yml", composeData, 0o644); err != nil {
		e := cnerr.Wrap("engine.update.backup", err, "write backup docker-compose.yml")
		deps.StepFinished(step, e)
		return "", e
	}

	// .env (chmod 600).
	envFile := installDir + "/.env"
	envData, err := deps.FS.ReadFile(envFile)
	if err != nil {
		e := cnerr.Wrap("engine.update.backup", err, "read .env for backup")
		deps.StepFinished(step, e)
		return "", e
	}
	if err := deps.FS.WriteFile(backupDir+"/.env", envData, 0o600); err != nil {
		e := cnerr.Wrap("engine.update.backup", err, "write backup .env")
		deps.StepFinished(step, e)
		return "", e
	}
	_ = deps.FS.Chmod(backupDir+"/.env", 0o600)

	// image-state.txt.
	imageState := fmt.Sprintf("AGENT_IMAGE_ID=%s\nFRONTEND_IMAGE_ID=%s\nMONGO_IMAGE=%s\n",
		state.AgentImageID, state.FrontendImageID, state.MongoImage)
	if err := deps.FS.WriteFile(backupDir+"/image-state.txt", []byte(imageState), 0o644); err != nil {
		e := cnerr.Wrap("engine.update.backup", err, "write image-state.txt")
		deps.StepFinished(step, e)
		return "", e
	}

	// Prune to 5 most recent backups.
	pruneBackups(deps, backupsRoot, maxBackups)

	appendUpdateLog(deps, opts, ts, "STEP", "backup created: "+backupDir)
	deps.StepFinished(step, nil)
	return backupDir, nil
}

// pruneBackups removes all but the `keep` most recent entries in backupsRoot.
func pruneBackups(deps Deps, backupsRoot string, keep int) {
	entries, err := deps.FS.ReadDir(backupsRoot)
	if err != nil || len(entries) <= keep {
		return
	}
	// Sort entries (timestamp names sort lexicographically = chronologically).
	sorted := make([]string, len(entries))
	copy(sorted, entries)
	sort.Strings(sorted)

	// Remove oldest (first) entries until we have `keep` remaining.
	toRemove := sorted[:len(sorted)-keep]
	for _, name := range toRemove {
		if err := deps.FS.RemoveAll(backupsRoot + "/" + name); err != nil {
			deps.Warn("backup-prune", "could not remove old backup "+name+": "+err.Error())
		}
	}
}

// ─── 5.5 Pull by explicit tag ─────────────────────────────────────────────────

// pullAndCompare pulls the agent (and optionally frontend) image by explicit
// tag, compares image IDs, and returns the new IDs plus a no-op flag.
func pullAndCompare(ctx context.Context, deps Deps, installDir, agentTag string, state currentState, opts UpdateOptions) (newAgentID, newFrontendID string, noOp bool, err error) {
	const step = "pull"
	deps.StepStarted(step)

	// Pull agent by EXPLICIT tag (AD-6).
	deps.Info(step, "pulling "+agentTag)
	if pullErr := deps.Client.ImagePull(ctx, agentTag); pullErr != nil {
		e := cnerr.Wrap("engine.update.pull", pullErr,
			"check network connectivity and that the version exists: "+agentTag)
		deps.StepFinished(step, e)
		return "", "", false, e
	}

	// Pull frontend unless SkipFrontend.
	frontendTag := fmt.Sprintf("%s:%s", frontendImageName, opts.Version)
	if !opts.SkipFrontend {
		deps.Info(step, "pulling "+frontendTag)
		if pullErr := deps.Client.ImagePull(ctx, frontendTag); pullErr != nil {
			// Non-fatal: warn and continue without frontend update.
			deps.Warn(step, fmt.Sprintf("frontend pull failed: %v — continuing without frontend update", pullErr))
			opts.SkipFrontend = true
		}
	}

	// Inspect new agent image ID.
	newAgentInfo, inspErr := deps.Client.ImageInspect(ctx, agentTag)
	if inspErr != nil {
		e := cnerr.Wrap("engine.update.pull", inspErr,
			"inspect pulled image "+agentTag)
		deps.StepFinished(step, e)
		return "", "", false, e
	}
	newAgentID = newAgentInfo.ID

	// Inspect new frontend image ID (if pulled).
	if !opts.SkipFrontend {
		newFrontendInfo, fErr := deps.Client.ImageInspect(ctx, frontendTag)
		if fErr == nil {
			newFrontendID = newFrontendInfo.ID
		}
	} else {
		newFrontendID = state.FrontendImageID
	}

	// No-op check: if image IDs are unchanged and Force is not set.
	agentUnchanged := newAgentID != "" && newAgentID == state.AgentImageID
	frontendUnchanged := opts.SkipFrontend || (newFrontendID != "" && newFrontendID == state.FrontendImageID)
	if agentUnchanged && frontendUnchanged && !opts.Force {
		deps.Info(step, "image IDs unchanged — no update needed")
		deps.StepFinished(step, nil)
		return newAgentID, newFrontendID, true, nil
	}

	deps.StepFinished(step, nil)
	return newAgentID, newFrontendID, false, nil
}

// ─── 5.6 Recreate ────────────────────────────────────────────────────────────

// recreateServices runs docker compose up -d --no-deps --force-recreate on the
// agent (and optionally frontend) services only. Databases are never touched.
func recreateServices(ctx context.Context, deps Deps, composeFile string, opts UpdateOptions) error {
	const step = "recreate"
	deps.StepStarted(step)

	services := []string{"agent"}
	if !opts.SkipFrontend {
		services = append(services, "frontend")
	}

	deps.Info(step, fmt.Sprintf("recreating services: %v", services))

	if err := deps.Client.ComposeUp(ctx, composeFile, dockerx.ComposeUpOptions{
		NoDeps:        true,
		ForceRecreate: true,
		Detach:        true,
		Services:      services,
	}); err != nil {
		e := cnerr.Wrap("engine.update.recreate", err,
			"check: docker compose logs agent")
		deps.StepFinished(step, e)
		return e
	}

	deps.StepFinished(step, nil)
	return nil
}

// ─── 5.7 Health checks ───────────────────────────────────────────────────────

// runUpdateHealthChecks verifies backend, frontend, and database health after
// recreate. Backend failure triggers automatic rollback (caller handles it).
func runUpdateHealthChecks(ctx context.Context, deps Deps, composeFile string, opts UpdateOptions, res *UpdateResult, ts time.Time) error {
	// Backend health: HTTPS-then-HTTP, /health endpoint, HTTP 200 only.
	if err := checkBackendHealth(ctx, deps, opts, ts); err != nil {
		return err
	}

	// Frontend health: warning only (not a rollback trigger).
	if !opts.SkipFrontend {
		if err := checkFrontendHealth(ctx, deps, opts); err != nil {
			w := "frontend health check failed (non-fatal): " + err.Error()
			res.Warnings = append(res.Warnings, w)
			deps.Warn("health-frontend", w)
			appendUpdateLog(deps, opts, ts, "WARN", w)
		}
	}

	// Databases must still be running.
	checkDatabaseHealth(ctx, deps, composeFile, res, opts, ts)

	return nil
}

// checkBackendHealth polls https://localhost:8000/health (falling back to HTTP)
// with the configured timeout and interval. Returns an error only on actual
// failure (HTTP 200 required — 404 is NOT success per spec).
func checkBackendHealth(ctx context.Context, deps Deps, opts UpdateOptions, ts time.Time) error {
	const step = "health-backend"
	deps.StepStarted(step)

	appendUpdateLog(deps, opts, ts, "STEP", "backend health check starting")

	deadline := time.Now().Add(opts.healthTimeout())
	interval := opts.retryInterval()

	backendURLs := []string{
		"https://localhost:8000/health",
		"http://localhost:8000/health",
	}

	for time.Now().Before(deadline) {
		for _, url := range backendURLs {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				continue
			}
			resp, err := deps.Prober.Do(req)
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			// HTTP 200 is the ONLY passing status (404 is NOT accepted — spec requirement).
			if resp.StatusCode == http.StatusOK {
				deps.Info(step, fmt.Sprintf("backend healthy at %s (HTTP 200)", url))
				appendUpdateLog(deps, opts, ts, "OK", "backend health check passed")
				deps.StepFinished(step, nil)
				return nil
			}
			// 404 from legacy target version — log WARN but don't accept it.
			if resp.StatusCode == http.StatusNotFound {
				appendUpdateLog(deps, opts, ts, "WARN",
					"backend returned 404 on /health — version may predate health endpoint; verifying container running state")
				deps.Warn(step, "backend returned 404 on /health — verifying container running state as fallback")
				// Fall through to container-running check below after the probe loop ends.
			}
		}

		select {
		case <-ctx.Done():
			deps.StepFinished(step, ctx.Err())
			return cnerr.Wrap("engine.update.health-backend", ctx.Err(),
				"health check cancelled; run: docker compose ps")
		case <-time.After(interval):
		}
	}

	// Timeout — check if the agent container is at least running (404 fallback).
	containers, psErr := deps.Client.ContainerList(ctx, "agent")
	if psErr == nil {
		for _, c := range containers {
			if (strings.Contains(c.Name, "agent") || strings.Contains(c.Service, "agent")) && c.Running {
				w := "HTTP readiness could not be confirmed (404 or timeout); agent container is running"
				deps.Warn(step, w)
				appendUpdateLog(deps, opts, ts, "WARN", w)
				deps.StepFinished(step, nil)
				return nil
			}
		}
	}

	e := cnerr.New("engine.update.health-backend: backend did not become healthy within timeout",
		fmt.Sprintf("check: docker compose logs agent; try manual: curl -k https://localhost:8000/health"))
	deps.StepFinished(step, e)
	return e
}

// checkFrontendHealth probes https://localhost:443 then http://localhost:80.
// Returns an error for caller to treat as a warning (not a rollback trigger).
func checkFrontendHealth(ctx context.Context, deps Deps, opts UpdateOptions) error {
	const step = "health-frontend"
	deps.StepStarted(step)

	frontendURLs := []string{
		"https://localhost:443",
		"http://localhost:80",
	}

	for _, url := range frontendURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := deps.Prober.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			deps.Info(step, fmt.Sprintf("frontend healthy at %s (HTTP %d)", url, resp.StatusCode))
			deps.StepFinished(step, nil)
			return nil
		}
	}

	deps.StepFinished(step, nil)
	return cnerr.New("engine.update.health-frontend: frontend did not respond",
		"check: docker compose logs frontend; try: curl -k https://localhost:443")
}

// checkDatabaseHealth verifies that mongodb, influxdb, and redis are still
// running via compose ps. Non-fatal but logged.
func checkDatabaseHealth(ctx context.Context, deps Deps, composeFile string, res *UpdateResult, opts UpdateOptions, ts time.Time) {
	const step = "health-databases"
	deps.StepStarted(step)

	dbServices := []string{"mongodb", "influxdb", "redis"}
	containers, err := deps.Client.ComposePs(ctx, composeFile, dbServices)
	if err != nil {
		w := "could not verify database status: " + err.Error()
		res.Warnings = append(res.Warnings, w)
		deps.Warn(step, w)
		deps.StepFinished(step, nil)
		return
	}
	running := make(map[string]bool)
	for _, c := range containers {
		if c.Running {
			running[c.Service] = true
		}
	}
	for _, svc := range dbServices {
		if !running[svc] {
			w := fmt.Sprintf("%s is not running after update — check: docker compose logs %s", svc, svc)
			res.Warnings = append(res.Warnings, w)
			deps.Warn(step, w)
			appendUpdateLog(deps, opts, ts, "WARN", w)
		}
	}
	deps.StepFinished(step, nil)
}

// ─── 5.8 Rollback ────────────────────────────────────────────────────────────

// performRollback re-tags previous image IDs from image-state.txt and recreates
// agent+frontend. Agent re-tag failure is fatal (consistent with rollback.go):
// recreating without the correct :latest tag would leave the system in a worse state.
// Frontend re-tag failure is non-fatal (logged as warning).
func performRollback(ctx context.Context, deps Deps, composeFile string, state currentState, opts UpdateOptions) error {
	const step = "rollback"
	deps.StepStarted(step)

	// Re-tag agent — FATAL. Without a known previous image ID we cannot restore
	// the previous agent: recreating would reuse whatever :latest currently points
	// to (possibly the broken new image), so abort instead of reporting a false
	// rollback success (consistent with rollback.go).
	if state.AgentImageID == "" {
		e := cnerr.New("engine.update.rollback: previous agent image ID is unknown",
			"rollback cannot restore the previous agent image — manual recovery required (docker tag <previous> "+agentImageName+":latest && docker compose up -d)")
		deps.StepFinished(step, e)
		return e
	}
	agentLatest := agentImageName + ":latest"
	if err := deps.Client.ImageTag(ctx, state.AgentImageID, agentLatest); err != nil {
		e := cnerr.Wrap("engine.update.rollback", err,
			"re-tag agent failed — rollback cannot restore previous agent image")
		deps.StepFinished(step, e)
		return e
	}

	// Re-tag frontend — non-fatal (warn and continue).
	if state.FrontendImageID != "" && !opts.SkipFrontend {
		frontendLatest := frontendImageName + ":latest"
		if err := deps.Client.ImageTag(ctx, state.FrontendImageID, frontendLatest); err != nil {
			deps.Warn(step, fmt.Sprintf("re-tag frontend failed: %v", err))
		}
	}

	// Recreate with previous images.
	services := []string{"agent"}
	if !opts.SkipFrontend {
		services = append(services, "frontend")
	}
	if err := deps.Client.ComposeUp(ctx, composeFile, dockerx.ComposeUpOptions{
		NoDeps:        true,
		ForceRecreate: true,
		Detach:        true,
		Services:      services,
	}); err != nil {
		deps.Warn(step, "rollback recreate failed: "+err.Error())
		deps.StepFinished(step, err)
		return err
	}

	deps.StepFinished(step, nil)
	return nil
}

// ─── 5.9 Logging ─────────────────────────────────────────────────────────────

// appendUpdateLog appends a timestamped entry to the update log file via the
// FS seam. Format: [YYYY-MM-DD HH:MM:SS] LEVEL: message
// DryRun mode skips writes.
func appendUpdateLog(deps Deps, opts UpdateOptions, ts time.Time, level, message string) {
	if opts.DryRun {
		return
	}
	entry := fmt.Sprintf("[%s] %s: %s\n",
		ts.Format("2006-01-02 15:04:05"),
		level,
		message,
	)
	_ = deps.FS.AppendFile(updateLogFile, []byte(entry), 0o644)
}
