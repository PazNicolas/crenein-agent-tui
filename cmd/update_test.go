package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// ─── Fakes ───────────────────────────────────────────────────────────────────

// fakeUpdateManifestClient is a test double for release.Client (update-specific
// to avoid collisions with the selfupdate test's fakeManifestClient).
type fakeUpdateManifestClient struct {
	manifest     *release.Manifest
	fetchErr     *cnerr.Error
	agentVersion string
}

func (f *fakeUpdateManifestClient) FetchManifest(_ context.Context, _ bool) (*release.Manifest, *cnerr.Error) {
	return f.manifest, f.fetchErr
}

func (f *fakeUpdateManifestClient) DetectAgentVersion(_ context.Context) string {
	if f.agentVersion == "" {
		return "unknown"
	}
	return f.agentVersion
}

// testAgentManifest builds a minimal valid Manifest for update tests.
func testAgentManifest() *release.Manifest {
	return &release.Manifest{
		Agent: release.AgentSection{
			Latest: "1.8.4",
			Releases: map[string]release.AgentRelease{
				"1.8.3": {
					Date:  "2026-05-01",
					Image: "crenein/c-network-agent-back:1.8.3",
					Mongo: map[string]string{"7": "mongo7", "4": "mongo4"},
					Notes: "Previous release",
				},
				"1.8.4": {
					Date:  "2026-06-14",
					Image: "crenein/c-network-agent-back:1.8.4",
					Mongo: map[string]string{"7": "mongo7", "4": "mongo4"},
					Notes: "Security fixes and performance improvements",
				},
			},
		},
		CLI: release.CLISection{
			Latest: "0.2.0",
			Releases: map[string]release.CLIRelease{
				"0.2.0": {Date: "2026-06-14", Notes: "CLI release"},
			},
		},
	}
}

// capturedUpdateOpts records the options the fake update engine received.
type capturedUpdateOpts struct {
	opts engine.UpdateOptions
}

// updateTestSetup wires a fake engine.Update and returns a cleanup function.
func updateTestSetup(result *engine.UpdateResult, err error, capture *capturedUpdateOpts) func() {
	original := updateFn
	updateFn = func(_ context.Context, _ engine.Deps, opts engine.UpdateOptions) (*engine.UpdateResult, error) {
		if capture != nil {
			capture.opts = opts
		}
		return result, err
	}
	return func() { updateFn = original }
}

// runUpdateCmd runs the update command with the given args and injected deps,
// returning stdout, stderr, and the resolved exit code.
func runUpdateCmd(t *testing.T, args []string, deps updateDeps) cmdResult {
	t.Helper()

	root := newRootCmd()
	// Replace the update subcommand with the one carrying test deps.
	for _, sub := range root.Commands() {
		if sub.Use == "update" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newUpdateCmdWithDeps(deps))

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true

	root.SetArgs(append([]string{"update"}, args...))
	err := root.Execute()

	code := 0
	if err != nil {
		var ecErr *exitCodeError
		if errors.As(err, &ecErr) {
			code = ecErr.code
		} else {
			code = 1
		}
	}
	return cmdResult{
		stdout:   outBuf.String(),
		stderr:   errBuf.String(),
		exitCode: code,
	}
}

// ─── 3.5 Tests ───────────────────────────────────────────────────────────────

// TestUpdate_Success_Exit0 verifies that a successful update exits 0 and
// prints a result line to stdout.
func TestUpdate_Success_Exit0(t *testing.T) {
	cleanup := updateTestSetup(&engine.UpdateResult{
		PreviousAgentImageID: "sha256:old",
		NewAgentImageID:      "sha256:new",
	}, nil, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "updated") {
		t.Errorf("stdout should contain 'updated', got: %q", res.stdout)
	}
}

// TestUpdate_NoOp_AlreadyUpToDate_Exit0 verifies that a no-op (already
// up to date) exits 0 with an "already up to date" message on stdout.
func TestUpdate_NoOp_AlreadyUpToDate_Exit0(t *testing.T) {
	cleanup := updateTestSetup(&engine.UpdateResult{NoOp: true}, nil, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.4",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "already up to date") {
		t.Errorf("stdout should contain 'already up to date', got: %q", res.stdout)
	}
}

// TestUpdate_DryRun_Exit0 verifies that --dry-run exits 0 with plan on stdout,
// no confirmation needed, and no side effects.
func TestUpdate_DryRun_Exit0(t *testing.T) {
	cleanup := updateTestSetup(&engine.UpdateResult{DryRun: true, InstallDir: "/install"}, nil, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		// No TTY required for dry-run.
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--dry-run"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "DRY RUN") {
		t.Errorf("stdout should contain 'DRY RUN', got: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "1.8.4") {
		t.Errorf("stdout should contain target version '1.8.4', got: %q", res.stdout)
	}
}

// TestUpdate_ManifestUnreachable_NoForce_Exit1 verifies that when the manifest
// is unreachable and --force is not set, the command exits 1 with a helpful
// message including the --force suggestion.
func TestUpdate_ManifestUnreachable_NoForce_Exit1(t *testing.T) {
	fetchErr := cnerr.New("release.FetchManifest: fetch latest release: GET ...: connection refused",
		"check network connectivity")

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			fetchErr:     fetchErr,
			agentVersion: "unknown",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.exitCode)
	}
	if !strings.Contains(res.stderr, "manifest unreachable") {
		t.Errorf("stderr should mention 'manifest unreachable', got: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "--force") {
		t.Errorf("stderr should suggest '--force', got: %q", res.stderr)
	}
}

// TestUpdate_VersionNotInManifest_Exit1 verifies that --version with a version
// not in the manifest exits 1 and lists available versions.
func TestUpdate_VersionNotInManifest_Exit1(t *testing.T) {
	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes", "--version", "9.9.9"}, deps)
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.exitCode)
	}
	if !strings.Contains(res.stderr, "9.9.9") {
		t.Errorf("stderr should mention the invalid version, got: %q", res.stderr)
	}
	// Should list available versions.
	if !strings.Contains(res.stderr, "1.8.4") {
		t.Errorf("stderr should list available versions including '1.8.4', got: %q", res.stderr)
	}
}

// TestUpdate_Preflight_Exit3 verifies that an engine preflight error exits 3.
func TestUpdate_Preflight_Exit3(t *testing.T) {
	preflightErr := cnerr.New("engine.update.preflight",
		"re-run as root: sudo ./crenein-agent update")

	cleanup := updateTestSetup(nil, preflightErr, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 3 {
		t.Errorf("exit code = %d, want 3; stderr: %q", res.exitCode, res.stderr)
	}
}

// TestUpdate_Decline_Exit4 verifies that declining the confirmation prompt
// exits 4.
func TestUpdate_Decline_Exit4(t *testing.T) {
	// The engine should never be called.
	cleanup := updateTestSetup(nil, errors.New("engine should not be called"), nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
		stdin:       bufio.NewReader(strings.NewReader("n\n")),
	}

	// No --yes flag, TTY simulated, user types "n".
	res := runUpdateCmd(t, []string{}, deps)
	if res.exitCode != 4 {
		t.Errorf("exit code = %d, want 4; stderr: %q; stdout: %q", res.exitCode, res.stderr, res.stdout)
	}
}

// TestUpdate_RolledBack_Exit5 verifies that when the engine returns
// RolledBack=true and RollbackFailed=false, the command exits 5.
func TestUpdate_RolledBack_Exit5(t *testing.T) {
	engineErr := cnerr.Wrap("engine.update.recreate",
		errors.New("compose up failed"),
		"rollback completed")

	cleanup := updateTestSetup(&engine.UpdateResult{
		RolledBack: true,
		BackupPath: "/install/.backups/20260614_120000",
	}, engineErr, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 5 {
		t.Errorf("exit code = %d, want 5; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stderr, "rolled back") {
		t.Errorf("stderr should mention rollback, got: %q", res.stderr)
	}
	// Spec: on exit 5, stderr MUST state which backup was used.
	if !strings.Contains(res.stderr, "/install/.backups/20260614_120000") {
		t.Errorf("stderr should state the backup path used, got: %q", res.stderr)
	}
}

// TestUpdate_RollbackFailed_Exit6 verifies that when the engine returns
// RolledBack=true and RollbackFailed=true, the command exits 6 and prints
// manual recovery steps.
func TestUpdate_RollbackFailed_Exit6(t *testing.T) {
	cleanup := updateTestSetup(&engine.UpdateResult{
		RolledBack:     true,
		RollbackFailed: true,
		BackupPath:     "/install/.backups/20260614_120000",
	}, errors.New("recreate failed"), nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 6 {
		t.Errorf("exit code = %d, want 6; stderr: %q", res.exitCode, res.stderr)
	}
	// Manual recovery instructions must appear.
	if !strings.Contains(res.stderr, "docker compose ps") {
		t.Errorf("stderr should contain 'docker compose ps', got: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "docker compose logs") {
		t.Errorf("stderr should contain 'docker compose logs', got: %q", res.stderr)
	}
}

// TestUpdate_NoTTY_NoYes_Exit64 verifies that without TTY and without --yes
// the command exits 64 (usage error).
func TestUpdate_NoTTY_NoYes_Exit64(t *testing.T) {
	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
		// No --yes flag, no TTY → exit 64.
	}

	res := runUpdateCmd(t, []string{}, deps)
	if res.exitCode != 64 {
		t.Errorf("exit code = %d, want 64; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stderr, "--yes") {
		t.Errorf("stderr should suggest '--yes', got: %q", res.stderr)
	}
}

// TestUpdate_SkipFrontend_PassThrough verifies that --skip-frontend is passed
// to the engine's UpdateOptions.
func TestUpdate_SkipFrontend_PassThrough(t *testing.T) {
	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	runUpdateCmd(t, []string{"--yes", "--skip-frontend"}, deps)
	if !cap.opts.SkipFrontend {
		t.Error("expected SkipFrontend=true to be passed to engine")
	}
}

// TestUpdate_NoCleanup_PassThrough verifies that --no-cleanup is passed to the
// engine's UpdateOptions.
func TestUpdate_NoCleanup_PassThrough(t *testing.T) {
	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	runUpdateCmd(t, []string{"--yes", "--no-cleanup"}, deps)
	if !cap.opts.NoCleanup {
		t.Error("expected NoCleanup=true to be passed to engine")
	}
}

// TestUpdate_Force_PassThrough verifies that --force is passed to the engine's
// UpdateOptions.
func TestUpdate_Force_PassThrough(t *testing.T) {
	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	runUpdateCmd(t, []string{"--yes", "--force"}, deps)
	if !cap.opts.Force {
		t.Error("expected Force=true to be passed to engine")
	}
}

// TestUpdate_Force_ManifestUnreachable_UsesLatest verifies that --force with
// an unreachable manifest bypasses validation and uses "latest" as version.
func TestUpdate_Force_ManifestUnreachable_UsesLatest(t *testing.T) {
	fetchErr := cnerr.New("release.FetchManifest: network error", "check connectivity")

	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:latest-new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			fetchErr:     fetchErr,
			agentVersion: "unknown",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes", "--force"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if cap.opts.Version != "latest" {
		t.Errorf("engine Version = %q, want %q", cap.opts.Version, "latest")
	}
	if !strings.Contains(res.stderr, "warning") {
		t.Errorf("stderr should contain a warning about manifest unreachable, got: %q", res.stderr)
	}
}

// TestUpdate_VersionPin_PassThrough verifies that --version resolves and passes
// the correct version to the engine.
func TestUpdate_VersionPin_PassThrough(t *testing.T) {
	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes", "--version", "1.8.3"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if cap.opts.Version != "1.8.3" {
		t.Errorf("engine Version = %q, want %q", cap.opts.Version, "1.8.3")
	}
}

// TestUpdate_Confirm_Yes_NoPrompt verifies that --yes skips the confirmation
// prompt and the engine is called.
func TestUpdate_Confirm_Yes_NoPrompt(t *testing.T) {
	var cap capturedUpdateOpts
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, &cap)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		// Even without TTY, --yes should succeed.
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	res := runUpdateCmd(t, []string{"--yes"}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	// Engine must have been called.
	if cap.opts.Version == "" {
		t.Error("engine was not called (Version is empty)")
	}
}

// TestUpdate_VersionBadFormat_Exit64 verifies that --version with a non-X.Y.Z
// value exits 64 (usage error) without a manifest lookup.
func TestUpdate_VersionBadFormat_Exit64(t *testing.T) {
	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	for _, badVersion := range []string{"latest", "v1.8.4", "1.8", "1.8.4.5", "notaversion"} {
		res := runUpdateCmd(t, []string{"--yes", "--version", badVersion}, deps)
		if res.exitCode != 64 {
			t.Errorf("--version %q: exit code = %d, want 64; stderr: %q",
				badVersion, res.exitCode, res.stderr)
		}
	}
}

// TestUpdate_Confirm_TTY_Accept verifies that with a TTY and user typing "y"
// the update proceeds (exit 0).
func TestUpdate_Confirm_TTY_Accept(t *testing.T) {
	cleanup := updateTestSetup(&engine.UpdateResult{
		NewAgentImageID: "sha256:new",
	}, nil, nil)
	defer cleanup()

	deps := updateDeps{
		manifestClient: &fakeUpdateManifestClient{
			manifest:     testAgentManifest(),
			agentVersion: "1.8.3",
		},
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
		stdin:       bufio.NewReader(strings.NewReader("y\n")),
	}

	res := runUpdateCmd(t, []string{}, deps)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
}
