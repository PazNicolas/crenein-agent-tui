package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// BackupInfo describes a single backup snapshot found in .backups/.
type BackupInfo struct {
	Timestamp       string
	Path            string
	AgentImageID    string
	FrontendImageID string
	MongoImage      string
}

// RollbackOptions carries all pre-resolved decisions for Rollback.
type RollbackOptions struct {
	InstallDir      string
	BackupTimestamp string // empty = use latest

	SkipFrontend bool

	// Injectable test seams.
	Now           func() time.Time
	RetryInterval time.Duration
	HealthTimeout time.Duration
}

// RollbackResult is the structured outcome of Rollback.
type RollbackResult struct {
	Timestamp               string
	RestoredAgentImageID    string
	RestoredFrontendImageID string
	HealthOK                bool
	Warnings                []string
}

func (opts RollbackOptions) now() time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

// ListBackups returns the available backup snapshots in installDir/.backups/,
// ordered most-recent-first. Returns an empty slice (no error) when the
// directory does not exist or contains no valid entries.
func ListBackups(deps Deps, installDir string) ([]BackupInfo, error) {
	backupsRoot := installDir + "/.backups"
	entries, err := deps.FS.ReadDir(backupsRoot)
	if err != nil {
		// Directory missing is not an error — just no backups.
		return nil, nil
	}

	const tsFormat = "20060102_150405"
	var backups []BackupInfo

	for _, entry := range entries {
		// Validate that the name matches the timestamp format.
		if _, parseErr := time.Parse(tsFormat, entry); parseErr != nil {
			continue
		}
		backupPath := backupsRoot + "/" + entry
		info, parseErr := parseBackupInfo(deps, entry, backupPath)
		if parseErr != nil {
			// Skip unparseable backups silently.
			continue
		}
		backups = append(backups, info)
	}

	// Sort most-recent-first (timestamp strings sort lexicographically = chronologically).
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp > backups[j].Timestamp
	})

	return backups, nil
}

// parseBackupInfo reads image-state.txt from backupPath and returns a BackupInfo.
func parseBackupInfo(deps Deps, timestamp, backupPath string) (BackupInfo, error) {
	stateData, err := deps.FS.ReadFile(backupPath + "/image-state.txt")
	if err != nil {
		return BackupInfo{}, err
	}

	info := BackupInfo{
		Timestamp: timestamp,
		Path:      backupPath,
	}

	scanner := bufio.NewScanner(bytes.NewReader(stateData))
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "AGENT_IMAGE_ID="); ok {
			info.AgentImageID = after
		} else if after, ok := strings.CutPrefix(line, "FRONTEND_IMAGE_ID="); ok {
			info.FrontendImageID = after
		} else if after, ok := strings.CutPrefix(line, "MONGO_IMAGE="); ok {
			info.MongoImage = after
		}
	}

	return info, nil
}

// Rollback restores a previous backup snapshot, retagging images and recreating
// affected services. Progress is reported via deps.Reporter.
//
// When opts.BackupTimestamp is empty the most-recent backup is used.
// Returns a *RollbackResult; HealthOK=false indicates post-rollback health
// checks failed (caller maps this to exit 1). A recreate failure returns an
// error (also exit 1).
func Rollback(ctx context.Context, deps Deps, opts RollbackOptions) (*RollbackResult, error) {
	now := opts.now()
	res := &RollbackResult{}

	// ── 1. Resolve backup ────────────────────────────────────────────────────
	const step = "resolve-backup"
	deps.StepStarted(step)

	backups, err := ListBackups(deps, opts.InstallDir)
	if err != nil {
		e := cnerr.Wrap("engine.rollback.resolve-backup", err, "list backups")
		deps.StepFinished(step, e)
		return nil, e
	}

	var chosen BackupInfo
	if opts.BackupTimestamp == "" {
		if len(backups) == 0 {
			e := cnerr.New("engine.rollback: no backups found",
				"run an update first to create a backup, or run crenein-agent install")
			deps.StepFinished(step, e)
			return nil, e
		}
		chosen = backups[0] // most-recent
	} else {
		found := false
		for _, b := range backups {
			if b.Timestamp == opts.BackupTimestamp {
				chosen = b
				found = true
				break
			}
		}
		if !found {
			e := cnerr.New(
				fmt.Sprintf("engine.rollback: backup %q not found", opts.BackupTimestamp),
				"use --list to see available backup timestamps",
			)
			deps.StepFinished(step, e)
			return nil, e
		}
	}

	res.Timestamp = chosen.Timestamp
	res.RestoredAgentImageID = chosen.AgentImageID
	res.RestoredFrontendImageID = chosen.FrontendImageID
	deps.Info(step, "using backup: "+chosen.Path)
	deps.StepFinished(step, nil)

	// ── 2. Restore files ─────────────────────────────────────────────────────
	const stepRestore = "restore-files"
	deps.StepStarted(stepRestore)

	composeData, err := deps.FS.ReadFile(chosen.Path + "/docker-compose.yml")
	if err != nil {
		e := cnerr.Wrap("engine.rollback.restore", err, "read backup docker-compose.yml")
		deps.StepFinished(stepRestore, e)
		return nil, e
	}
	if err := deps.FS.WriteFile(opts.InstallDir+"/docker-compose.yml", composeData, 0o644); err != nil {
		e := cnerr.Wrap("engine.rollback.restore", err, "restore docker-compose.yml")
		deps.StepFinished(stepRestore, e)
		return nil, e
	}

	envData, err := deps.FS.ReadFile(chosen.Path + "/.env")
	if err != nil {
		e := cnerr.Wrap("engine.rollback.restore", err, "read backup .env")
		deps.StepFinished(stepRestore, e)
		return nil, e
	}
	if err := deps.FS.WriteFile(opts.InstallDir+"/.env", envData, 0o600); err != nil {
		e := cnerr.Wrap("engine.rollback.restore", err, "restore .env")
		deps.StepFinished(stepRestore, e)
		return nil, e
	}
	_ = deps.FS.Chmod(opts.InstallDir+"/.env", 0o600)

	deps.StepFinished(stepRestore, nil)

	// ── 3. Retag images ──────────────────────────────────────────────────────
	const stepRetag = "retag-images"
	deps.StepStarted(stepRetag)

	// Agent retag is FATAL. An empty AgentImageID means the backup never recorded
	// the previous agent image, so recreating would reuse whatever :latest points
	// to (possibly a broken image) — abort instead of a false rollback success.
	if chosen.AgentImageID == "" {
		e := cnerr.New("engine.rollback.retag-agent: backup has no recorded agent image ID",
			"this backup cannot restore the agent image — pick another with --list or recover manually")
		deps.StepFinished(stepRetag, e)
		return nil, e
	}
	if err := deps.Client.ImageTag(ctx, chosen.AgentImageID, agentImageName+":latest"); err != nil {
		// Agent retag failure is fatal: proceeding to recreate with the wrong
		// image would restore an incorrect container state.
		e := cnerr.Wrap("engine.rollback.retag-agent", err,
			fmt.Sprintf("ensure image %s is available locally: docker pull %s", chosen.AgentImageID, chosen.AgentImageID))
		deps.StepFinished(stepRetag, e)
		return nil, e
	}
	if !opts.SkipFrontend && chosen.FrontendImageID != "" {
		if err := deps.Client.ImageTag(ctx, chosen.FrontendImageID, frontendImageName+":latest"); err != nil {
			// Frontend retag is non-critical: log a warning and proceed.
			w := "retag frontend failed: " + err.Error()
			deps.Warn(stepRetag, w)
			res.Warnings = append(res.Warnings, w)
		}
	}

	deps.StepFinished(stepRetag, nil)

	// ── 4. Recreate services ─────────────────────────────────────────────────
	const stepRecreate = "recreate"
	deps.StepStarted(stepRecreate)

	composeFile := opts.InstallDir + "/docker-compose.yml"
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
		e := cnerr.Wrap("engine.rollback.recreate", err,
			"check: docker compose logs agent")
		deps.StepFinished(stepRecreate, e)
		appendRollbackLog(deps, now, "ERR", "recreate failed: "+err.Error())
		return nil, e
	}

	deps.StepFinished(stepRecreate, nil)

	// ── 5. Health checks ─────────────────────────────────────────────────────
	updateOpts := UpdateOptions{
		SkipFrontend:  opts.SkipFrontend,
		RetryInterval: opts.RetryInterval,
		HealthTimeout: opts.HealthTimeout,
		Now:           opts.Now,
	}
	// Use a temporary UpdateResult to collect warnings from health checks.
	tmpRes := &UpdateResult{}
	if healthErr := runUpdateHealthChecks(ctx, deps, composeFile, updateOpts, tmpRes, now); healthErr != nil {
		res.HealthOK = false
		w := "post-rollback health check failed: " + healthErr.Error()
		res.Warnings = append(res.Warnings, w)
		appendRollbackLog(deps, now, "WARN", w)
	} else {
		res.HealthOK = true
	}
	res.Warnings = append(res.Warnings, tmpRes.Warnings...)

	// ── 6. Log ───────────────────────────────────────────────────────────────
	status := "OK"
	if !res.HealthOK {
		status = "WARN"
	}
	appendRollbackLog(deps, now, status,
		fmt.Sprintf("rollback to %s completed; agent=%s", chosen.Timestamp, chosen.AgentImageID))

	return res, nil
}

// appendRollbackLog writes a timestamped line to the update log file.
// Format mirrors appendUpdateLog (same file, same format).
// Uses AppendFile (not ReadFile+WriteFile) to avoid TOCTOU races.
func appendRollbackLog(deps Deps, ts time.Time, level, message string) {
	entry := fmt.Sprintf("[%s] %s: %s\n",
		ts.Format("2006-01-02 15:04:05"),
		level,
		message,
	)
	_ = deps.FS.AppendFile(updateLogFile, []byte(entry), 0o644)
}
