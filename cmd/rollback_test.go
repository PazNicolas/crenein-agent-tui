package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

// rollbackTestSetup wires fake listBackupsFn and rollbackFn. Returns cleanup.
func rollbackTestSetup(
	backups []engine.BackupInfo, listErr error,
	result *engine.RollbackResult, rollErr error,
) func() {
	origList := listBackupsFn
	origRollback := rollbackFn

	listBackupsFn = func(_ engine.Deps, _ string) ([]engine.BackupInfo, error) {
		return backups, listErr
	}
	rollbackFn = func(_ context.Context, _ engine.Deps, _ engine.RollbackOptions) (*engine.RollbackResult, error) {
		return result, rollErr
	}
	return func() {
		listBackupsFn = origList
		rollbackFn = origRollback
	}
}

// twoBackups returns a standard pair of BackupInfo for tests.
func twoBackups() []engine.BackupInfo {
	return []engine.BackupInfo{
		{
			Timestamp:       "20240201_120000",
			Path:            "./.backups/20240201_120000",
			AgentImageID:    "sha256:agent2",
			FrontendImageID: "sha256:front2",
			MongoImage:      "mongo:4.4",
		},
		{
			Timestamp:       "20240101_120000",
			Path:            "./.backups/20240101_120000",
			AgentImageID:    "sha256:agent1",
			FrontendImageID: "sha256:front1",
			MongoImage:      "mongo:4.4",
		},
	}
}

func successResult() *engine.RollbackResult {
	return &engine.RollbackResult{
		Timestamp:               "20240201_120000",
		RestoredAgentImageID:    "sha256:agent2",
		RestoredFrontendImageID: "sha256:front2",
		HealthOK:                true,
	}
}

// runRollbackCmd runs the rollback command and returns stdout/stderr/exitCode.
func runRollbackCmd(t *testing.T, args []string, deps rollbackDeps) cmdResult {
	t.Helper()

	root := newRootCmd()
	for _, sub := range root.Commands() {
		if sub.Use == "rollback" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newRollbackCmdWithDeps(deps))

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true

	root.SetArgs(append([]string{"rollback"}, args...))
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

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestRollbackCmd_List_Exit0 verifies --list prints snapshots and exits 0.
func TestRollbackCmd_List_Exit0(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{installDir: "."}
	res := runRollbackCmd(t, []string{"--list"}, deps)

	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "20240201_120000") {
		t.Errorf("stdout missing newest backup; got: %s", res.stdout)
	}
	if !strings.Contains(res.stdout, "20240101_120000") {
		t.Errorf("stdout missing older backup; got: %s", res.stdout)
	}
}

// TestRollbackCmd_List_NoBackups_Exit3 verifies --list exits 3 when no backups.
func TestRollbackCmd_List_NoBackups_Exit3(t *testing.T) {
	cleanup := rollbackTestSetup(nil, nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{installDir: "."}
	res := runRollbackCmd(t, []string{"--list"}, deps)

	if res.exitCode != ExitPreflight {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitPreflight, res.stderr)
	}
}

// TestRollbackCmd_NoInstall_Exit3 verifies that a missing install returns exit 3.
//
// The resolution is made hermetic by injecting readFile/readDir that always
// report "no file / empty dir", so resolveInstallDir returns "" regardless of
// the host filesystem. This forces the no-installation preflight path
// deterministically.
func TestRollbackCmd_NoInstall_Exit3(t *testing.T) {
	cleanup := rollbackTestSetup(nil, nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir: "", // force resolution via injected readFile/readDir
		readFile: func(string) ([]byte, error) {
			return nil, errors.New("no such file")
		},
		readDir: func(string) ([]string, error) {
			return nil, errors.New("no such directory")
		},
	}

	res := runRollbackCmd(t, []string{"--yes"}, deps)
	if res.exitCode != ExitPreflight {
		t.Fatalf("exit code = %d, want %d (no installation); stderr: %s",
			res.exitCode, ExitPreflight, res.stderr)
	}
	if !strings.Contains(res.stderr, "no CRENEIN installation found") {
		t.Errorf("stderr should mention no installation found; got: %s", res.stderr)
	}
}

// TestRollbackCmd_LatestSelection_Success verifies default latest selection exits 0.
func TestRollbackCmd_LatestSelection_Success(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, successResult(), nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes"}, deps)

	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "rollback complete") {
		t.Errorf("stdout missing success message; got: %s", res.stdout)
	}
}

// TestRollbackCmd_ExplicitBackup_Valid_Success verifies --backup with a valid timestamp.
func TestRollbackCmd_ExplicitBackup_Valid_Success(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, &engine.RollbackResult{
		Timestamp:            "20240101_120000",
		RestoredAgentImageID: "sha256:agent1",
		HealthOK:             true,
	}, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes", "--backup", "20240101_120000"}, deps)

	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", res.exitCode, res.stderr)
	}
}

// TestRollbackCmd_ExplicitBackup_Invalid_Exit64 verifies unknown --backup exits 64.
func TestRollbackCmd_ExplicitBackup_Invalid_Exit64(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes", "--backup", "19990101_000000"}, deps)

	if res.exitCode != ExitUsage {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitUsage, res.stderr)
	}
	// stderr should list the available timestamps.
	if !strings.Contains(res.stderr, "20240201_120000") {
		t.Errorf("stderr should list available backups; got: %s", res.stderr)
	}
}

// TestRollbackCmd_Decline_Exit4 verifies user declining confirmation returns exit 4.
func TestRollbackCmd_Decline_Exit4(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, nil, nil)
	defer cleanup()

	stdinBuf := bufio.NewReader(strings.NewReader("n\n"))
	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
		stdin:       stdinBuf,
	}
	res := runRollbackCmd(t, []string{}, deps)

	if res.exitCode != ExitAborted {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitAborted, res.stderr)
	}
}

// TestRollbackCmd_NoTTY_NoYes_Exit64 verifies no TTY without --yes returns exit 64.
func TestRollbackCmd_NoTTY_NoYes_Exit64(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{}, deps)

	if res.exitCode != ExitUsage {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitUsage, res.stderr)
	}
}

// TestRollbackCmd_HealthFail_Exit1 verifies HealthOK=false maps to exit 1.
func TestRollbackCmd_HealthFail_Exit1(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, &engine.RollbackResult{
		Timestamp:            "20240201_120000",
		RestoredAgentImageID: "sha256:agent2",
		HealthOK:             false,
	}, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes"}, deps)

	if res.exitCode != ExitOpFailure {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitOpFailure, res.stderr)
	}
	if !strings.Contains(res.stderr, "health checks failed") {
		t.Errorf("stderr should mention health checks; got: %s", res.stderr)
	}
}

// TestRollbackCmd_EngineError_Exit1 verifies engine error maps to exit 1.
func TestRollbackCmd_EngineError_Exit1(t *testing.T) {
	engErr := fmt.Errorf("recreate failed")
	cleanup := rollbackTestSetup(twoBackups(), nil, nil, engErr)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes"}, deps)

	if res.exitCode != ExitOpFailure {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitOpFailure, res.stderr)
	}
}

// TestRollbackCmd_NoBackups_Exit3 verifies empty backup list returns exit 3 (not --list).
func TestRollbackCmd_NoBackups_Exit3(t *testing.T) {
	cleanup := rollbackTestSetup(nil, nil, nil, nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes"}, deps)

	if res.exitCode != ExitPreflight {
		t.Errorf("exit code = %d, want %d; stderr: %s", res.exitCode, ExitPreflight, res.stderr)
	}
}

// TestRollbackCmd_Success_Exit0 verifies the full success path output.
func TestRollbackCmd_Success_Exit0(t *testing.T) {
	cleanup := rollbackTestSetup(twoBackups(), nil, successResult(), nil)
	defer cleanup()

	deps := rollbackDeps{
		installDir:  ".",
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}
	res := runRollbackCmd(t, []string{"--yes"}, deps)

	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "rollback complete") {
		t.Errorf("stdout missing 'rollback complete'; got: %s", res.stdout)
	}
	if !strings.Contains(res.stdout, "20240201_120000") {
		t.Errorf("stdout missing timestamp; got: %s", res.stdout)
	}
}
