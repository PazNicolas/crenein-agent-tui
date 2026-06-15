package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// updateFn is the engine.Update call seam. Tests replace it with a fake.
var updateFn = func(ctx context.Context, deps engine.Deps, opts engine.UpdateOptions) (*engine.UpdateResult, error) {
	return engine.Update(ctx, deps, opts)
}

// updateDeps holds injectable dependencies for the update command.
// The zero value wires real implementations.
type updateDeps struct {
	// manifestClient overrides the release.Client used for manifest fetches.
	// When nil, a real ManifestClient is constructed.
	manifestClient release.Client

	// stdinIsTTY overrides stdin TTY detection (for tests).
	stdinIsTTY *bool
	// stderrIsTTY overrides stderr TTY detection (for tests).
	stderrIsTTY *bool

	// stdin overrides the reader for confirmation prompts (for tests).
	stdin *bufio.Reader
}

// newUpdateCmd constructs the `update` subcommand wired to real deps.
func newUpdateCmd() *cobra.Command {
	return newUpdateCmdWithDeps(updateDeps{})
}

// newUpdateCmdWithDeps constructs the `update` subcommand with injectable
// dependencies (used by tests).
func newUpdateCmdWithDeps(deps updateDeps) *cobra.Command {
	var (
		flagYes          bool
		flagVersion      string
		flagDryRun       bool
		flagSkipFrontend bool
		flagNoCleanup    bool
		flagForce        bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the CRENEIN C-Network agent stack to a new version",
		Long: `Updates the CRENEIN C-Network agent stack by pulling a new agent image and
recreating the affected containers, with automatic rollback on health failure.

IMPORTANT — flag semantics differ from the legacy update-agent.sh:

  --force  Forces container recreation even when the pulled image ID is
           identical to the current one. It does NOT grant confirmation
           consent. You still need --yes (or an interactive TTY) to approve
           the update.

  --yes    Grants confirmation consent and skips the interactive prompt.
           This is the flag that replaces the implicit "proceed" in
           update-agent.sh.

In short: --force is about RECREATION POLICY; --yes is about CONSENT.
Combining both (--force --yes) is the equivalent of the old unattended mode.

Exit codes:
  0   success or already up-to-date
  1   pre-mutation failure (manifest/pull)
  3   pre-flight check failed (root, Docker, disk, connectivity)
  4   aborted by user (declined confirmation)
  5   update failed, rollback succeeded
  6   update failed AND rollback also failed (manual recovery required)
  64  usage error (e.g. confirmation required without TTY and without --yes)`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runAgentUpdate(ctx, cmd, deps,
				flagYes, flagVersion, flagDryRun,
				flagSkipFrontend, flagNoCleanup, flagForce)
		},
	}

	cmd.Flags().BoolVar(&flagYes, "yes", false, "skip interactive confirmation (grants consent)")
	cmd.Flags().StringVar(&flagVersion, "version", "", "target version to update to (e.g. 1.8.4); defaults to manifest latest")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "show the update plan without making any changes (no confirmation required)")
	cmd.Flags().BoolVar(&flagSkipFrontend, "skip-frontend", false, "update only the agent service, skip the frontend")
	cmd.Flags().BoolVar(&flagNoCleanup, "no-cleanup", false, "skip docker image prune after a successful update")
	cmd.Flags().BoolVar(&flagForce, "force", false, "recreate containers even when image IDs are identical (does NOT imply --yes)")

	return cmd
}

func runAgentUpdate(
	ctx context.Context,
	cmd *cobra.Command,
	deps updateDeps,
	flagYes bool,
	flagVersion string,
	flagDryRun bool,
	flagSkipFrontend bool,
	flagNoCleanup bool,
	flagForce bool,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// ── Build manifest client ─────────────────────────────────────────────────
	mc := deps.manifestClient
	if mc == nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "/root"
		}
		fs := dockerx.NewOSFS()
		mc = release.NewManifestClient(dockerx.NewHTTPProber(httpClient), nil, fs, homeDir, time.Now)
	}

	// ── Resolve target version ────────────────────────────────────────────────
	targetVersion, releaseNotes, currentVersion, err := resolveUpdateVersion(ctx, mc, flagVersion, flagForce, stderr)
	if err != nil {
		return err
	}

	// ── DryRun: run engine, print plan, exit 0 (no confirmation needed) ───────
	if flagDryRun {
		return runAgentUpdateDryRun(ctx, cmd, deps, targetVersion, flagSkipFrontend, flagNoCleanup, flagForce)
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

		// Show update preview on stderr.
		fmt.Fprintf(stderr, "\nUpdate CRENEIN C-Network agent stack\n")
		fmt.Fprintf(stderr, "  current: %s\n", currentVersion)
		fmt.Fprintf(stderr, "  target:  %s\n", targetVersion)
		if releaseNotes != "" {
			fmt.Fprintf(stderr, "\nRelease notes:\n  %s\n", releaseNotes)
		}
		fmt.Fprint(stderr, "\nProceed? [y/N] ")

		// Read confirmation.
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

	// ── Build engine deps ─────────────────────────────────────────────────────
	engineDeps, preflightErr := buildUpdateEngineDeps(ctx, stderr)
	if preflightErr != nil {
		return preflightErr
	}

	opts := engine.UpdateOptions{
		Version:      targetVersion,
		DryRun:       false,
		SkipFrontend: flagSkipFrontend,
		Force:        flagForce,
		NoCleanup:    flagNoCleanup,
	}

	// ── Call engine ───────────────────────────────────────────────────────────
	res, engErr := updateFn(ctx, engineDeps, opts)

	// ── Map exit codes (priority order matters) ───────────────────────────────

	// Priority 1: rollback (regardless of engErr).
	if res != nil && res.RolledBack {
		if res.RollbackFailed {
			WriteError(stderr, "error: update failed and rollback also failed — system may be in an inconsistent state\n")
			WriteError(stderr, "manual recovery steps:\n")
			WriteError(stderr, "  1. Check running containers:  docker compose ps\n")
			WriteError(stderr, "  2. Inspect logs:              docker compose logs agent\n")
			if res.BackupPath != "" {
				WriteError(stderr, "  3. Restore from backup:\n")
				WriteError(stderr, "       cp %s/.env . && cp %s/docker-compose.yml .\n", res.BackupPath, res.BackupPath)
				WriteError(stderr, "       docker compose up -d\n")
			}
			WriteError(stderr, "  4. Or pull the previous image manually and recreate\n")
			if engErr != nil {
				return rollbackFailedError(engErr)
			}
			return rollbackFailedError(fmt.Errorf("rollback failed"))
		}
		WriteError(stderr, "error: update failed — rolled back to previous version\n")
		if engErr != nil {
			WriteError(stderr, "  cause: %v\n", engErr)
			return rolledBackError(engErr)
		}
		return rolledBackError(fmt.Errorf("update failed with rollback"))
	}

	// Priority 2: engine returned an error (pre-mutation failures).
	if engErr != nil {
		if isPreflightErr(engErr) {
			WriteError(stderr, "preflight failed: %v\n", engErr)
			return preflightError(engErr)
		}
		WriteError(stderr, "update failed: %v\n", engErr)
		return opFailureError(engErr)
	}

	// Priority 3: success cases (res != nil, engErr == nil).
	if res.NoOp {
		WriteData(stdout, "already up to date (%s)\n", targetVersion)
		return nil
	}

	// Print warnings to stderr.
	for _, w := range res.Warnings {
		WriteError(stderr, "warning: %s\n", w)
	}

	// Success line to stdout: show human-readable versions, not image IDs.
	WriteData(stdout, "updated: %s → %s\n", currentVersion, targetVersion)

	return nil
}

// resolveUpdateVersion resolves the target agent version from the manifest.
// Returns (targetVersion, releaseNotes, currentVersion, error).
func resolveUpdateVersion(
	ctx context.Context,
	mc release.Client,
	pinVersion string,
	force bool,
	stderr io.Writer,
) (targetVersion, releaseNotes, currentVersion string, err error) {
	// Detect current running version (best-effort; may be "unknown").
	currentVersion = mc.DetectAgentVersion(ctx)

	// Try to fetch the manifest.
	m, fetchErr := mc.FetchManifest(ctx, false)
	if fetchErr != nil {
		if force {
			// --force + manifest unreachable: bypass validation, use :latest.
			fmt.Fprintf(stderr, "warning: manifest unreachable (%v); --force active, falling back to :latest\n", fetchErr)
			return "latest", "", currentVersion, nil
		}
		// No --force: hard error.
		fmt.Fprintf(stderr, "error: manifest unreachable: %v\n", fetchErr)
		fmt.Fprintf(stderr, "       use --force to update to :latest without manifest validation\n")
		return "", "", currentVersion, opFailureError(fmt.Errorf("manifest unreachable: %w", fetchErr))
	}

	if pinVersion != "" {
		// Validate format before lookup: must be X.Y.Z.
		if matched, _ := regexp.MatchString(`^\d+\.\d+\.\d+$`, pinVersion); !matched {
			return "", "", currentVersion, usageError("--version requires an X.Y.Z value")
		}
		// Validate pin against manifest.
		resolved, verr := release.ResolveAgentVersion(m, pinVersion)
		if verr != nil {
			// List available versions on stderr.
			fmt.Fprintf(stderr, "error: version %q not found in manifest\n", pinVersion)
			fmt.Fprintf(stderr, "available versions:\n")
			for _, v := range availableVersionsSorted(m) {
				fmt.Fprintf(stderr, "  %s\n", v)
			}
			return "", "", currentVersion, opFailureError(fmt.Errorf("version %q not found in manifest", pinVersion))
		}
		notes := release.ResolveReleaseNotes(m, resolved)
		return resolved, notes, currentVersion, nil
	}

	// No pin: use manifest latest.
	resolved, verr := release.ResolveAgentVersion(m, "")
	if verr != nil {
		return "", "", currentVersion, opFailureError(verr)
	}
	notes := release.ResolveReleaseNotes(m, resolved)
	return resolved, notes, currentVersion, nil
}

// availableVersionsSorted returns the agent release versions from the manifest,
// sorted in descending semver order (newest first).
func availableVersionsSorted(m *release.Manifest) []string {
	versions := make([]string, 0, len(m.Agent.Releases))
	for v := range m.Agent.Releases {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return release.CompareSemver(versions[i], versions[j]) > 0
	})
	return versions
}

// buildUpdateEngineDeps builds the real engine.Deps for update. Extracted so it
// can be shared between the normal path and dry-run path.
func buildUpdateEngineDeps(ctx context.Context, stderr interface{ Write([]byte) (int, error) }) (engine.Deps, error) {
	ttyState := DetectTTY()
	noColorEnv := os.Getenv("NO_COLOR")
	policy := NewDecorPolicy(ttyState, false, globalFlags.noColor, globalFlags.quiet, noColorEnv)
	presenter := NewHumanPresenter(stderr, policy)

	composeInfo, composeErr := detect.Compose(ctx, dockerx.NewOSCommandRunner())
	if composeErr != nil {
		fmt.Fprintf(stderr, "preflight failed: docker compose not found: %v\n", composeErr)
		return engine.Deps{}, preflightError(composeErr)
	}

	return engine.Deps{
		Client: dockerx.NewCLIClient(composeInfo.Variant),
		Runner: dockerx.NewOSCommandRunner(),
		FS:     dockerx.NewOSFS(),
		// Use an insecure HTTP client for health probing: the agent uses
		// self-signed certs on localhost, so TLS verification would always fail
		// and trigger spurious rollbacks after every update.
		Prober:   dockerx.NewHTTPProber(newInsecureHTTPClient()),
		Reporter: presenter,
	}, nil
}

// runAgentUpdateDryRun calls the engine in dry-run mode and prints the plan to stdout.
func runAgentUpdateDryRun(
	ctx context.Context,
	cmd *cobra.Command,
	deps updateDeps,
	targetVersion string,
	skipFrontend bool,
	noCleanup bool,
	force bool,
) error {
	stdout := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	engineDeps, preflightErr := buildUpdateEngineDeps(ctx, stderr)
	if preflightErr != nil {
		return preflightErr
	}

	opts := engine.UpdateOptions{
		Version:      targetVersion,
		DryRun:       true,
		SkipFrontend: skipFrontend,
		Force:        force,
		NoCleanup:    noCleanup,
	}

	res, err := updateFn(ctx, engineDeps, opts)
	if err != nil {
		if isPreflightErr(err) {
			WriteError(stderr, "preflight failed: %v\n", err)
			return preflightError(err)
		}
		WriteError(stderr, "dry-run failed: %v\n", err)
		return opFailureError(err)
	}

	// Print the dry-run plan to stdout.
	services := "agent"
	if !skipFrontend {
		services = "agent, frontend"
	}
	WriteData(stdout, "DRY RUN — no changes will be made\n")
	WriteData(stdout, "  target version: %s\n", targetVersion)
	WriteData(stdout, "  services:       %s\n", services)
	if res.InstallDir != "" {
		WriteData(stdout, "  install dir:    %s\n", res.InstallDir)
		WriteData(stdout, "  backup dir:     %s/.backups/%s\n", res.InstallDir, time.Now().Format("20060102_150405"))
	}
	if res.MongoImage != "" {
		WriteData(stdout, "  mongo image:    %s (immutable)\n", res.MongoImage)
	}
	if force {
		WriteData(stdout, "  force recreate: yes\n")
	}
	if noCleanup {
		WriteData(stdout, "  cleanup:        skipped\n")
	}

	return nil
}
