package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// listBackupsFn is the engine.ListBackups seam. Tests replace it with a fake.
var listBackupsFn = func(deps engine.Deps, installDir string) ([]engine.BackupInfo, error) {
	return engine.ListBackups(deps, installDir)
}

// rollbackFn is the engine.Rollback seam. Tests replace it with a fake.
var rollbackFn = func(ctx context.Context, deps engine.Deps, opts engine.RollbackOptions) (*engine.RollbackResult, error) {
	return engine.Rollback(ctx, deps, opts)
}

// rollbackDeps holds injectable dependencies for the rollback command.
type rollbackDeps struct {
	// stdinIsTTY overrides stdin TTY detection (for tests).
	stdinIsTTY *bool
	// stderrIsTTY overrides stderr TTY detection (for tests).
	stderrIsTTY *bool
	// stdin overrides the reader for confirmation prompts (for tests).
	stdin *bufio.Reader
	// installDir overrides install-dir detection (for tests).
	installDir string
	// readFile overrides filesystem reads during install-dir detection (for tests).
	// When nil, the real OS filesystem is used.
	readFile func(name string) ([]byte, error)
	// readDir overrides directory listing during install-dir detection (for tests).
	// When nil, the real OS filesystem is used.
	readDir func(path string) ([]string, error)
}

// newRollbackCmd constructs the `rollback` subcommand wired to real deps.
func newRollbackCmd() *cobra.Command {
	return newRollbackCmdWithDeps(rollbackDeps{})
}

// newRollbackCmdWithDeps constructs the `rollback` subcommand with injectable deps.
func newRollbackCmdWithDeps(deps rollbackDeps) *cobra.Command {
	var (
		flagYes    bool
		flagBackup string
		flagList   bool
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Restore the CRENEIN agent stack to a previous backup snapshot",
		Long: `Restores the agent and frontend services from a previous backup snapshot.

Backups are created automatically during every successful update and stored in
<install-dir>/.backups/<TIMESTAMP>/.

Exit codes:
  0   rollback complete and health checks passed
  1   rollback failed or post-rollback health checks failed
  3   no installation found or no backups available
  4   aborted by user (declined confirmation)
  64  usage error (unknown --backup timestamp, or no TTY without --yes)`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runRollback(ctx, cmd, deps, flagYes, flagBackup, flagList)
		},
	}

	cmd.Flags().BoolVar(&flagYes, "yes", false, "skip interactive confirmation")
	cmd.Flags().StringVar(&flagBackup, "backup", "", "backup timestamp to restore (e.g. 20240101_120000); default: latest")
	cmd.Flags().BoolVar(&flagList, "list", false, "list available backup snapshots and exit")

	return cmd
}

func runRollback(
	ctx context.Context,
	cmd *cobra.Command,
	deps rollbackDeps,
	flagYes bool,
	flagBackup string,
	flagList bool,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	fs := dockerx.NewOSFS()

	// ── Resolve install dir ───────────────────────────────────────────────────
	// readFile/readDir are injectable so the resolution is hermetic in tests
	// (no real filesystem dependency). They default to the OS filesystem.
	readFile := deps.readFile
	if readFile == nil {
		readFile = fs.ReadFile
	}
	readDir := deps.readDir
	if readDir == nil {
		readDir = fs.ReadDir
	}
	installDir := resolveInstallDir(readFile, readDir, deps.installDir)
	if installDir == "" {
		WriteError(stderr, "no CRENEIN installation found (no docker-compose.yml referencing crenein/c-network-agent-back in . or /root or /home/*/)\n")
		WriteError(stderr, "hint: run `crenein-agent install` to set up the agent stack\n")
		return preflightError(fmt.Errorf("no installation found"))
	}

	// ── Build a minimal engine.Deps for ListBackups (read-only) ──────────────
	ttyState := DetectTTY()
	noColorEnv := os.Getenv("NO_COLOR")
	policy := NewDecorPolicy(ttyState, false, globalFlags.noColor, globalFlags.quiet, noColorEnv)
	presenter := NewHumanPresenter(stderr, policy)

	composeInfo, composeErr := detect.Compose(ctx, dockerx.NewOSCommandRunner())
	var engineClient dockerx.Client
	if composeErr == nil {
		engineClient = dockerx.NewCLIClient(composeInfo.Variant)
	} else {
		engineClient = dockerx.NewCLIClient(dockerx.ComposeV2)
	}

	engineDeps := engine.Deps{
		Client: engineClient,
		Runner: dockerx.NewOSCommandRunner(),
		FS:     fs,
		// Use an insecure HTTP client for health probing: the agent uses
		// self-signed certs on localhost, so TLS verification would always fail
		// and trigger spurious rollbacks after every update.
		Prober:   dockerx.NewHTTPProber(newInsecureHTTPClient()),
		Reporter: presenter,
	}

	// ── --list mode ───────────────────────────────────────────────────────────
	backups, err := listBackupsFn(engineDeps, installDir)
	if err != nil {
		WriteError(stderr, "error: failed to list backups: %v\n", err)
		return opFailureError(err)
	}

	if flagList {
		if len(backups) == 0 {
			WriteError(stderr, "no backups available in %s/.backups/\n", installDir)
			return preflightError(fmt.Errorf("no backups available"))
		}
		for _, b := range backups {
			fmt.Fprintf(stdout, "%s  agent=%s  frontend=%s  mongo=%s\n",
				b.Timestamp, b.AgentImageID, b.FrontendImageID, b.MongoImage)
		}
		return nil
	}

	// ── Validate --backup flag ────────────────────────────────────────────────
	if flagBackup != "" {
		found := false
		for _, b := range backups {
			if b.Timestamp == flagBackup {
				found = true
				break
			}
		}
		if !found {
			WriteError(stderr, "error: backup %q not found\n", flagBackup)
			WriteError(stderr, "available backups:\n")
			for _, b := range backups {
				WriteError(stderr, "  %s\n", b.Timestamp)
			}
			return usageError(fmt.Sprintf("backup %q not found", flagBackup))
		}
	}

	// Determine the chosen backup (for confirmation display).
	var chosen engine.BackupInfo
	if flagBackup == "" {
		if len(backups) == 0 {
			WriteError(stderr, "no backups available in %s/.backups/\n", installDir)
			WriteError(stderr, "hint: run an update first to create a backup snapshot\n")
			return preflightError(fmt.Errorf("no backups available"))
		}
		chosen = backups[0]
	} else {
		for _, b := range backups {
			if b.Timestamp == flagBackup {
				chosen = b
				break
			}
		}
	}

	// ── Confirmation ──────────────────────────────────────────────────────────
	if !flagYes {
		tty := DetectTTY()
		stdinIsTTY := tty.StdinIsTTY
		stderrIsTTY := tty.StderrIsTTY
		if deps.stdinIsTTY != nil {
			stdinIsTTY = *deps.stdinIsTTY
		}
		if deps.stderrIsTTY != nil {
			stderrIsTTY = *deps.stderrIsTTY
		}

		if !stdinIsTTY || !stderrIsTTY {
			WriteError(stderr, "error: confirmation required but no TTY detected; use --yes to confirm non-interactively\n")
			return usageError("confirmation required; use --yes to confirm non-interactively")
		}

		fmt.Fprintf(stderr, "\nRollback CRENEIN C-Network agent stack\n")
		fmt.Fprintf(stderr, "  backup:   %s\n", chosen.Timestamp)
		fmt.Fprintf(stderr, "  agent:    %s\n", chosen.AgentImageID)
		fmt.Fprintf(stderr, "  frontend: %s\n", chosen.FrontendImageID)
		fmt.Fprint(stderr, "\nProceed? [y/N] ")

		reader := deps.stdin
		if reader == nil {
			reader = bufio.NewReader(cmd.InOrStdin())
		}
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			return abortedError()
		}
	}

	// ── Call engine ───────────────────────────────────────────────────────────
	opts := engine.RollbackOptions{
		InstallDir:      installDir,
		BackupTimestamp: flagBackup,
		Now:             time.Now,
	}

	res, engErr := rollbackFn(ctx, engineDeps, opts)
	if engErr != nil {
		WriteError(stderr, "rollback failed: %v\n", engErr)
		return opFailureError(engErr)
	}

	// Print warnings.
	for _, w := range res.Warnings {
		WriteError(stderr, "warning: %s\n", w)
	}

	if !res.HealthOK {
		WriteError(stderr, "error: rollback completed but post-rollback health checks failed\n")
		WriteError(stderr, "  check: docker compose logs agent\n")
		return opFailureError(fmt.Errorf("post-rollback health checks failed"))
	}

	WriteData(stdout, "rollback complete: restored snapshot %s (agent=%s)\n",
		res.Timestamp, res.RestoredAgentImageID)
	return nil
}
