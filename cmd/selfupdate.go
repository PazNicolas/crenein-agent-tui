package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/selfupdate"
)

// selfUpdateDeps holds injectable dependencies for the self-update command.
// The zero value wires real implementations.
type selfUpdateDeps struct {
	// manifestClient overrides the release.Client used for manifest fetches.
	// When nil, a real ManifestClient is constructed from prober.
	manifestClient release.Client

	// updater overrides the selfupdate.Performer used to install the binary.
	// When nil, selfupdate.New(src, prober) is used with releaseSource/prober.
	updater selfupdate.Performer

	// releaseSource overrides the selfupdate.ReleaseSource used by the Updater.
	// Used only when updater is nil.
	releaseSource selfupdate.ReleaseSource

	// prober overrides the HTTP prober used for all HTTP calls.
	// When nil, the package-level httpClient is used.
	prober dockerx.HTTPProber

	// homeDir overrides the user's home directory for cache resolution.
	// When empty, os.UserHomeDir() is used.
	homeDir string

	// noTTYOverride when true causes the TTY check to always report no TTY.
	// Used in tests to simulate non-interactive environments.
	noTTYOverride bool

	// stdin overrides the reader used for interactive confirmation.
	// When nil, cmd.InOrStdin() is used.
	stdin io.Reader
}

// devBuild reports whether the running binary was built without version injection.
func devBuild() bool { return build.version == "" || build.version == "dev" }

// newSelfUpdateCmd constructs the `self-update` subcommand wired to real deps.
func newSelfUpdateCmd() *cobra.Command {
	return newSelfUpdateCmdWithDeps(selfUpdateDeps{})
}

// newSelfUpdateCmdWithDeps constructs the `self-update` subcommand with
// injectable dependencies (used by tests).
func newSelfUpdateCmdWithDeps(deps selfUpdateDeps) *cobra.Command {
	var (
		flagYes        bool
		flagCheck      bool
		flagVersion    string
		flagForceCheck bool
	)

	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update crenein-agent to the latest (or a specific) release",
		Long: "Downloads, verifies (SHA256), and atomically installs a new\n" +
			"crenein-agent binary from the GitHub Releases of PazNicolas/crenein-agent-tui.\n\n" +
			"Use --check to query update availability without modifying anything.\n" +
			"Use --version X.Y.Z to install a specific version (allows downgrade).\n" +
			"Use --force-check to bypass the 24-hour manifest cache.",
		Args: cobra.NoArgs,
		// This command prints all its user-facing messages itself (to stdout for
		// data, stderr for errors) and signals outcome via exit codes. Silence
		// cobra's own "Error: ..."/usage output so --check's exit 10 ("update
		// available", not a failure) and our formatted errors aren't duplicated.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runSelfUpdate(ctx, cmd, deps, flagYes, flagCheck, flagVersion, flagForceCheck)
		},
	}

	cmd.Flags().BoolVar(&flagYes, "yes", false, "skip interactive confirmation")
	cmd.Flags().BoolVar(&flagCheck, "check", false, "report update availability; exit 0=up-to-date 10=available 1=error")
	cmd.Flags().StringVar(&flagVersion, "version", "", "install a specific version (e.g. 0.2.0)")
	cmd.Flags().BoolVar(&flagForceCheck, "force-check", false, "bypass the local manifest cache and fetch from GitHub")

	return cmd
}

func runSelfUpdate(ctx context.Context, cmd *cobra.Command, deps selfUpdateDeps, yes, checkOnly bool, pinVersion string, forceCheck bool) error {
	// ── dev build guard ───────────────────────────────────────────────────────
	if devBuild() {
		if checkOnly {
			fmt.Fprintln(cmd.OutOrStdout(), "current version: dev (development build — update check skipped)")
			return nil
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "error: this is a development build (version=dev); self-update is not supported")
		fmt.Fprintln(cmd.ErrOrStderr(), "       build with ldflags -X main.version=<semver> to enable self-update")
		return &exitCodeError{code: 1, err: fmt.Errorf("development build")}
	}

	currentVersion := build.version

	// ── resolve prober ────────────────────────────────────────────────────────
	prober := deps.prober
	if prober == nil {
		prober = dockerx.NewHTTPProber(httpClient)
	}

	// ── resolve manifest client ───────────────────────────────────────────────
	mc := deps.manifestClient
	if mc == nil {
		homeDir := deps.homeDir
		if homeDir == "" {
			var err error
			homeDir, err = os.UserHomeDir()
			if err != nil {
				homeDir = "/root"
			}
		}
		mc = release.NewManifestClient(prober, nil, dockerx.NewOSFS(), homeDir, time.Now)
	}

	// ── --check mode ──────────────────────────────────────────────────────────
	if checkOnly {
		return runCheck(ctx, cmd, mc, currentVersion, forceCheck)
	}

	// ── resolve performer (updater) ───────────────────────────────────────────
	performer := deps.updater
	if performer == nil {
		src := deps.releaseSource
		if src == nil {
			src = release.NewGitHubReleaseSource(prober)
		}
		performer = selfupdate.New(src, prober)
	}

	// ── update / downgrade mode ───────────────────────────────────────────────
	noTTY := deps.noTTYOverride
	return runUpdate(ctx, cmd, mc, performer, currentVersion, pinVersion, yes, noTTY, deps.stdin)
}

// runCheck implements --check: no filesystem modifications, exits 0/10/1.
func runCheck(ctx context.Context, cmd *cobra.Command, mc release.Client, currentVersion string, forceCheck bool) error {
	m, cerr := mc.FetchManifest(ctx, forceCheck)
	if cerr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", cerr)
		return &exitCodeError{code: 1, err: cerr}
	}

	info := release.ComputeUpdateInfo(m, currentVersion, "")

	switch info.CLIStatus {
	case release.UpdateUpToDate:
		fmt.Fprintf(cmd.OutOrStdout(), "crenein-agent is up to date (%s)\n", currentVersion)
		return nil // exit 0
	case release.UpdateAvailable:
		fmt.Fprintf(cmd.OutOrStdout(), "update available: %s → %s\n", currentVersion, info.CLILatest)
		return &exitCodeError{code: 10, err: fmt.Errorf("update available")}
	default: // UpdateUnknown
		fmt.Fprintf(cmd.ErrOrStderr(), "update status unknown (current=%s, latest=%s)\n", currentVersion, info.CLILatest)
		return &exitCodeError{code: 1, err: fmt.Errorf("update status unknown")}
	}
}

// runUpdate performs the actual self-update (or downgrade with --version).
func runUpdate(
	ctx context.Context,
	cmd *cobra.Command,
	mc release.Client,
	updater selfupdate.Performer,
	currentVersion, pinVersion string,
	yes bool,
	noTTY bool,
	stdinOverride io.Reader,
) error {

	var (
		targetVersion  string
		allowDowngrade bool
		releaseNotes   string
	)

	if pinVersion != "" {
		// Explicit pin — allows downgrade.
		targetVersion = pinVersion
		allowDowngrade = true
	} else {
		// Latest from manifest.
		m, cerr := mc.FetchManifest(ctx, false)
		if cerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "error fetching manifest: %v\n", cerr)
			return &exitCodeError{code: 1, err: cerr}
		}
		targetVersion = m.CLI.Latest
		if rel, ok := m.CLI.Releases[targetVersion]; ok {
			releaseNotes = rel.Notes
		}

		// Already up to date?
		if release.CompareSemver(currentVersion, targetVersion) >= 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "crenein-agent is already up to date (%s)\n", currentVersion)
			return nil
		}
	}

	// ── interactive confirmation ──────────────────────────────────────────────
	if !yes {
		// Require both stdin and stderr to be TTYs (consistent with install/update/rollback).
		ttyState := DetectTTY()
		tty := !noTTY && ttyState.StdinIsTTY && ttyState.StderrIsTTY
		if !tty {
			fmt.Fprintln(cmd.ErrOrStderr(), "error: no TTY detected and --yes not set; pass --yes to confirm non-interactively")
			return usageError("no TTY and --yes not set")
		}

		fmt.Fprintf(cmd.ErrOrStderr(), "Update crenein-agent %s → %s\n", currentVersion, targetVersion)
		if releaseNotes != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "\nRelease notes:\n  %s\n", releaseNotes)
		}
		fmt.Fprint(cmd.ErrOrStderr(), "\nProceed? [y/N] ")

		var in io.Reader
		if stdinOverride != nil {
			in = stdinOverride
		} else {
			in = cmd.InOrStdin()
		}
		reader := bufio.NewReader(in)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Aborted.")
			return nil
		}
	}

	// ── perform update ────────────────────────────────────────────────────────
	result, updateErr := updater.Update(ctx, currentVersion, targetVersion, allowDowngrade)
	if updateErr != nil {
		return handleUpdateError(cmd, updateErr)
	}

	switch result.Action {
	case "updated":
		fmt.Fprintf(cmd.OutOrStdout(), "updated %s → %s\n", result.FromVersion, result.ToVersion)
	case "downgraded":
		fmt.Fprintf(cmd.OutOrStdout(), "downgraded %s → %s\n", result.FromVersion, result.ToVersion)
	case "no-op":
		fmt.Fprintf(cmd.OutOrStdout(), "crenein-agent is already up to date (%s)\n", result.ToVersion)
	}
	return nil
}

// handleUpdateError inspects the error and prints an appropriate user-facing
// message before returning an exitCodeError.
func handleUpdateError(cmd *cobra.Command, err error) error {
	var cnErr *cnerr.Error
	if errors.As(err, &cnErr) {
		// Permission-denied: hint sudo.
		if strings.Contains(cnErr.FixSuggestion, "sudo") {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: insufficient permissions to update the binary\n")
			fmt.Fprintf(cmd.ErrOrStderr(), "       %s\n", cnErr.FixSuggestion)
			return &exitCodeError{code: 1, err: err}
		}
		// Checksum mismatch: explain clearly.
		if strings.Contains(cnErr.Op, "verifyChecksum") || strings.Contains(cnErr.Op, "SHA256") {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: checksum verification failed — the downloaded binary does not match checksums.txt\n")
			fmt.Fprintf(cmd.ErrOrStderr(), "       the partial download has been removed; the original binary is intact\n")
			fmt.Fprintf(cmd.ErrOrStderr(), "       details: %v\n", err)
			return &exitCodeError{code: 1, err: err}
		}
	}
	// Generic error.
	fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
	return &exitCodeError{code: 1, err: err}
}

// httpClient is the real *http.Client used for production requests.
var httpClient = &http.Client{Timeout: 60 * time.Second}
