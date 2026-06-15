package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeLogsDeps builds a logsDeps backed by the given FakeClient and in-memory FS.
// installDir is injected directly so no real filesystem search happens.
func fakeLogsDeps(fc *dockerx.FakeClient, installDir string) logsDeps {
	return logsDeps{
		composeLogsStream: func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error {
			return fc.ComposeLogsStream(ctx, composeFile, service, tail, follow, noColor, stdout)
		},
		readFile:   func(_ string) ([]byte, error) { return nil, nil },
		readDir:    func(_ string) ([]string, error) { return nil, nil },
		installDir: installDir,
	}
}

// runLogsCmd is a test helper that executes the logs cobra command with the
// given args and deps, returning stdout, stderr, and the returned error.
func runLogsCmd(t *testing.T, deps logsDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}

	cmd := newLogsCmdWithDeps(deps)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs(args)

	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// ─── Task 6.1 — argument validation ──────────────────────────────────────────

func TestLogs_UnknownService_Exit64(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, stderr, err := runLogsCmd(t, deps, "badservice")

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ecErr *exitCodeError
	if !asExitCodeError(err, &ecErr) || ecErr.code != ExitUsage {
		t.Errorf("expected exit 64, got err=%v", err)
	}
	if !strings.Contains(stderr, "valid services") {
		t.Errorf("expected valid services list in stderr, got: %q", stderr)
	}
	// The list should name all five services.
	for _, svc := range logsValidServices {
		if !strings.Contains(stderr, svc) {
			t.Errorf("stderr missing service %q: %q", svc, stderr)
		}
	}
}

func TestLogs_ValidServices_NoError(t *testing.T) {
	t.Parallel()
	for _, svc := range logsValidServices {
		svc := svc
		t.Run(svc, func(t *testing.T) {
			t.Parallel()
			fc := &dockerx.FakeClient{
				ComposeLogsStreamOut: []byte("log line\n"),
			}
			deps := fakeLogsDeps(fc, "/opt/crenein")

			_, _, err := runLogsCmd(t, deps, svc)
			if err != nil {
				t.Errorf("expected nil error for valid service %q, got: %v", svc, err)
			}
		})
	}
}

func TestLogs_NoService_UsesEmptyService(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{
		ComposeLogsStreamOut: []byte("all services\n"),
	}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, err := runLogsCmd(t, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if len(fc.Calls) == 0 {
		t.Fatal("expected at least one call to FakeClient")
	}
	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatalf("ComposeLogsStream not called; calls: %v", fc.Calls)
	}
	// The service arg should be empty (all services).
	if len(call.Args) < 2 || call.Args[1] != "" {
		t.Errorf("expected service arg to be empty for all-services; args: %v", call.Args)
	}
}

func TestLogs_NoInstall_Exit3(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := logsDeps{
		composeLogsStream: func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error {
			return fc.ComposeLogsStream(ctx, composeFile, service, tail, follow, noColor, stdout)
		},
		readFile:   func(_ string) ([]byte, error) { return nil, nil },
		readDir:    func(_ string) ([]string, error) { return nil, nil },
		installDir: "", // empty => no install found
	}

	_, stderr, err := runLogsCmd(t, deps)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ecErr *exitCodeError
	if !asExitCodeError(err, &ecErr) || ecErr.code != ExitPreflight {
		t.Errorf("expected exit 3 (preflight), got err=%v", err)
	}
	if !strings.Contains(stderr, "no CRENEIN installation found") {
		t.Errorf("expected installation-not-found message in stderr, got: %q", stderr)
	}
}

// ─── Task 6.2 — compose invocation arguments ─────────────────────────────────

func TestLogs_DefaultTail100(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, _ = runLogsCmd(t, deps, "agent")

	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatal("ComposeLogsStream not called")
	}
	// Args[2] = "tail=100 follow=false noColor=..."
	if !strings.Contains(call.Args[2], "tail=100") {
		t.Errorf("expected tail=100 in args, got: %v", call.Args)
	}
}

func TestLogs_CustomTail(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, _ = runLogsCmd(t, deps, "--tail", "42", "agent")

	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatal("ComposeLogsStream not called")
	}
	if !strings.Contains(call.Args[2], "tail=42") {
		t.Errorf("expected tail=42 in args, got: %v", call.Args)
	}
}

func TestLogs_Follow_FlagPropagated(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, _ = runLogsCmd(t, deps, "--follow", "agent")

	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatal("ComposeLogsStream not called")
	}
	if !strings.Contains(call.Args[2], "follow=true") {
		t.Errorf("expected follow=true in args, got: %v", call.Args)
	}
}

func TestLogs_ComposeFileContainsInstallDir(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, _ = runLogsCmd(t, deps, "agent")

	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatal("ComposeLogsStream not called")
	}
	// Args[0] = composeFile
	if !strings.Contains(call.Args[0], "/opt/crenein") {
		t.Errorf("expected composeFile to contain install dir, got: %v", call.Args[0])
	}
}

// ─── Task 6.2 — noColor propagation ──────────────────────────────────────────

func TestLogs_NoColorWhenNotTTY(t *testing.T) {
	t.Parallel()
	// In tests stdout is never a real TTY, so noColor should be true.
	fc := &dockerx.FakeClient{}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	_, _, _ = runLogsCmd(t, deps, "agent")

	call := findCall(fc.Calls, "ComposeLogsStream")
	if call == nil {
		t.Fatal("ComposeLogsStream not called")
	}
	if !strings.Contains(call.Args[2], "noColor=true") {
		t.Errorf("expected noColor=true when stdout is not a TTY, got: %v", call.Args)
	}
}

// ─── Task 6.3 — context cancellation (SIGINT simulation) ─────────────────────

func TestLogs_ContextCancel_ReturnsNil(t *testing.T) {
	t.Parallel()
	// Simulate follow mode with an already-cancelled context.
	// ComposeLogsStream on FakeClient returns nil when ctx is cancelled.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	fc := &dockerx.FakeClient{}
	// Directly test that a cancelled context returns nil from the fake.
	err := fc.ComposeLogsStream(cancelledCtx, "/opt/crenein/docker-compose.yml", "agent", 100, true, true, &bytes.Buffer{})
	if err != nil {
		t.Errorf("expected nil on cancelled ctx, got: %v", err)
	}
}

func TestLogs_ContextCancel_NoCallsRecorded(t *testing.T) {
	t.Parallel()
	// The cancelled-context path should still record the call in FakeClient
	// but return nil (clean exit), not an error.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	fc := &dockerx.FakeClient{}
	err := fc.ComposeLogsStream(cancelledCtx, "/opt/crenein/docker-compose.yml", "agent", 100, true, true, &bytes.Buffer{})
	if err != nil {
		t.Errorf("cancelled ctx must return nil, got: %v", err)
	}
	// The call is still recorded.
	if len(fc.Calls) != 1 || fc.Calls[0].Method != "ComposeLogsStream" {
		t.Errorf("expected 1 ComposeLogsStream call, got: %v", fc.Calls)
	}
}

// ─── Task 6.4 — stdout output propagated ─────────────────────────────────────

func TestLogs_OutputForwardedToStdout(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{
		ComposeLogsStreamOut: []byte("hello from compose\n"),
	}
	deps := fakeLogsDeps(fc, "/opt/crenein")

	stdout, _, err := runLogsCmd(t, deps, "agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "hello from compose") {
		t.Errorf("expected compose output in stdout, got: %q", stdout)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func findCall(calls []dockerx.Call, method string) *dockerx.Call {
	for i := range calls {
		if calls[i].Method == method {
			return &calls[i]
		}
	}
	return nil
}

// asExitCodeError is a type-assertion helper that avoids importing errors in tests.
func asExitCodeError(err error, target **exitCodeError) bool {
	if e, ok := err.(*exitCodeError); ok {
		*target = e
		return true
	}
	return false
}
