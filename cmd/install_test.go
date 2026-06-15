package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// ─── test helpers ────────────────────────────────────────────────────────────

// fakeInstallResult is the install result returned by the fake engine.
var fakeInstallResult = &engine.InstallResult{
	Steps: []engine.StepResult{{Name: "preflight"}, {Name: "stack-up"}},
	Services: []engine.ServiceStatus{
		{Service: "agent", Running: true},
		{Service: "frontend", Running: true},
	},
	AccessSummary: []engine.AccessEntry{
		{Label: "Backend API (HTTPS)", Value: "https://<VM_IP>:8000"},
		{Label: "Frontend (HTTPS)", Value: "https://<VM_IP>:443"},
		{Label: "InfluxDB", Value: "http://<VM_IP>:8086"},
	},
}

// capturedOpts records the options the fake engine received.
type capturedOpts struct {
	opts engine.InstallOptions
}

// installTestSetup wires a fake install engine and returns a cleanup function.
func installTestSetup(result *engine.InstallResult, err error, capture *capturedOpts) func() {
	original := installFn
	installFn = func(_ context.Context, _ engine.Deps, opts engine.InstallOptions) (*engine.InstallResult, error) {
		if capture != nil {
			capture.opts = opts
		}
		return result, err
	}
	return func() { installFn = original }
}

// boolPtr is a convenience helper for *bool values.
func boolPtr(b bool) *bool { return &b }

// ─── 2.1/2.3 Flag & env precedence ──────────────────────────────────────────

func TestInstall_FlagPrecedence(t *testing.T) {
	// Flag value must win over env var.
	t.Setenv("CRENEIN_API_URL", "http://env.example.com")
	t.Setenv("CRENEIN_API_TOKEN", "env-token")
	t.Setenv("CRENEIN_ADMIN_EMAIL", "env@example.com")
	t.Setenv("CRENEIN_ADMIN_PASSWORD", "envpassword")
	t.Setenv("CRENEIN_INSTALL_DIR", "/env/dir")
	t.Setenv("CRENEIN_MONGO_MAJOR", "auto")

	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)

	// Set flags explicitly to override env.
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "api-url", "http://flag.example.com")
	mustSetFlag(t, cmd, "api-token", "flag-token")
	mustSetFlag(t, cmd, "admin-email", "flag@example.com")
	mustSetFlag(t, cmd, "admin-password", "flagpassword")
	mustSetFlag(t, cmd, "dir", "/flag/dir")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.opts.APIURL != "http://flag.example.com" {
		t.Errorf("APIURL = %q, want flag value", cap.opts.APIURL)
	}
	if cap.opts.APIToken != "flag-token" {
		t.Errorf("APIToken = %q, want flag value", cap.opts.APIToken)
	}
	if cap.opts.AdminEmail != "flag@example.com" {
		t.Errorf("AdminEmail = %q, want flag value", cap.opts.AdminEmail)
	}
	if cap.opts.AdminPassword != "flagpassword" {
		t.Errorf("AdminPassword = %q, want flag value", cap.opts.AdminPassword)
	}
	if cap.opts.InstallDir != "/flag/dir" {
		t.Errorf("InstallDir = %q, want flag value", cap.opts.InstallDir)
	}
}

func TestInstall_EnvPrecedence(t *testing.T) {
	// Env var must win over default when flag is empty.
	t.Setenv("CRENEIN_API_URL", "http://env.example.com")
	t.Setenv("CRENEIN_API_TOKEN", "env-token")
	t.Setenv("CRENEIN_ADMIN_EMAIL", "env@example.com")
	t.Setenv("CRENEIN_ADMIN_PASSWORD", "envpassword")
	t.Setenv("CRENEIN_INSTALL_DIR", "/env/dir")
	t.Setenv("CRENEIN_MONGO_MAJOR", "4")

	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cap.opts.APIURL != "http://env.example.com" {
		t.Errorf("APIURL = %q, want env value", cap.opts.APIURL)
	}
	if cap.opts.APIToken != "env-token" {
		t.Errorf("APIToken = %q, want env value", cap.opts.APIToken)
	}
	if cap.opts.AdminEmail != "env@example.com" {
		t.Errorf("AdminEmail = %q, want env value", cap.opts.AdminEmail)
	}
	if cap.opts.InstallDir != "/env/dir" {
		t.Errorf("InstallDir = %q, want env value", cap.opts.InstallDir)
	}
	// CRENEIN_MONGO_MAJOR=4 → MongoImageOverride = mongo:4.4
	if cap.opts.MongoImageOverride != detect.MongoImage(false) {
		t.Errorf("MongoImageOverride = %q, want %q", cap.opts.MongoImageOverride, detect.MongoImage(false))
	}
}

// ─── 2.2 Prompt order ────────────────────────────────────────────────────────

func TestInstall_PromptOrder(t *testing.T) {
	// With TTY, prompts must appear in stable order:
	// dir, mongo, api-url, api-token, admin-email, admin-password.

	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	// Provide scripted answers in order, then confirm.
	stdinScript := "/tmp/install\nauto\nhttp://prompted:8000\nprompted-token\nprompted@example.com\nprompted-pass\ny\n"

	deps := installDeps{
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdinScript))

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, errBuf.String())
	}

	stderrOut := errBuf.String()

	// Check that all prompt labels appear in stable order.
	labels := []string{
		"Installation directory",
		"MongoDB version",
		"API URL",
		"API token",
		"Admin email",
		"Admin password",
	}
	lastPos := -1
	for _, label := range labels {
		pos := strings.Index(stderrOut, label)
		if pos == -1 {
			t.Errorf("prompt label %q not found in stderr:\n%s", label, stderrOut)
			continue
		}
		if pos <= lastPos {
			t.Errorf("label %q appears before previous label — stable order violated", label)
		}
		lastPos = pos
	}

	// Verify resolved values were passed to the engine.
	if cap.opts.InstallDir != "/tmp/install" {
		t.Errorf("InstallDir = %q, want /tmp/install", cap.opts.InstallDir)
	}
	if cap.opts.APIURL != "http://prompted:8000" {
		t.Errorf("APIURL = %q, want http://prompted:8000", cap.opts.APIURL)
	}
	if cap.opts.APIToken != "prompted-token" {
		t.Errorf("APIToken = %q, want prompted-token", cap.opts.APIToken)
	}
}

// ─── 2.2 Decline → exit 4 ────────────────────────────────────────────────────

func TestInstall_Decline_Exit4(t *testing.T) {
	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	// Answer all prompts then decline at "Proceed? [y/N]".
	stdinScript := "/tmp/install\nauto\nhttp://localhost:8000\ntoken\nadmin@example.com\nadmin123\nN\n"

	deps := installDeps{
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdinScript))

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error (exit 4), got nil")
	}
	var ecErr *exitCodeError
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
	}
	if ecErr.code != ExitAborted {
		t.Errorf("exit code = %d, want %d (ExitAborted), stderr:\n%s", ecErr.code, ExitAborted, errBuf.String())
	}
}

// ─── 2.3 Missing input without TTY and without --yes → exit 64 ───────────────

func TestInstall_MissingInput_NoTTY_Exit64(t *testing.T) {
	// Clear all env vars so nothing can be auto-resolved.
	for _, v := range []string{
		"CRENEIN_INSTALL_DIR", "CRENEIN_MONGO_MAJOR",
		"CRENEIN_API_URL", "CRENEIN_API_TOKEN",
		"CRENEIN_ADMIN_EMAIL", "CRENEIN_ADMIN_PASSWORD",
	} {
		t.Setenv(v, "")
	}

	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	// No --yes, no flags, no env, no TTY → should fail with exit 64.

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error (exit 64), got nil")
	}
	var ecErr *exitCodeError
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
	}
	if ecErr.code != ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", ecErr.code, ExitUsage)
	}
	// Error message should list missing flags and their env vars.
	msg := err.Error()
	for _, flag := range []string{"--dir", "--api-url", "--api-token", "--admin-email", "--admin-password"} {
		if !strings.Contains(msg, flag) {
			t.Errorf("missing input error should mention %q, got:\n%s", flag, msg)
		}
	}
	for _, env := range []string{"CRENEIN_INSTALL_DIR", "CRENEIN_API_URL", "CRENEIN_API_TOKEN", "CRENEIN_ADMIN_EMAIL", "CRENEIN_ADMIN_PASSWORD"} {
		if !strings.Contains(msg, env) {
			t.Errorf("missing input error should mention %q, got:\n%s", env, msg)
		}
	}
}

// ─── 2.4 --mongo 7 without AVX → exit 3 ─────────────────────────────────────

func TestInstall_Mongo7_NoAVX_Exit3(t *testing.T) {
	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
		// Fake AVX detection: no AVX.
		avxDetect: func(_ context.Context, _ dockerx.FS) (bool, error) {
			return false, nil
		},
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "mongo", "7")

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error (exit 3), got nil")
	}
	var ecErr *exitCodeError
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
	}
	if ecErr.code != ExitPreflight {
		t.Errorf("exit code = %d, want %d (ExitPreflight)", ecErr.code, ExitPreflight)
	}
	if !strings.Contains(err.Error(), "AVX") {
		t.Errorf("error should mention AVX, got: %s", err.Error())
	}
}

// ─── 2.4 --mongo auto does not validate AVX (engine decides) ─────────────────

func TestInstall_MongoAuto_NoImageOverride(t *testing.T) {
	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "mongo", "auto")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With --mongo auto, MongoImageOverride should be empty (engine auto-selects).
	if cap.opts.MongoImageOverride != "" {
		t.Errorf("MongoImageOverride = %q, want empty (engine auto-selects)", cap.opts.MongoImageOverride)
	}
}

// ─── 2.4 api-url/api-token reach the engine ──────────────────────────────────

func TestInstall_APIUrlToken_ReachEngine(t *testing.T) {
	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "api-url", "http://custom-api:9000")
	mustSetFlag(t, cmd, "api-token", "super-secret-token")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, errBuf.String())
	}

	if cap.opts.APIURL != "http://custom-api:9000" {
		t.Errorf("APIURL = %q, want http://custom-api:9000", cap.opts.APIURL)
	}
	if cap.opts.APIToken != "super-secret-token" {
		t.Errorf("APIToken = %q, want super-secret-token", cap.opts.APIToken)
	}
}

// ─── 2.4 Access summary to stdout ────────────────────────────────────────────

func TestInstall_AccessSummary_ToStdout(t *testing.T) {
	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stdout := outBuf.String()
	for _, entry := range fakeInstallResult.AccessSummary {
		if !strings.Contains(stdout, entry.Value) {
			t.Errorf("access summary entry %q not found on stdout:\n%s", entry.Value, stdout)
		}
	}
	// Access summary must not appear on stderr.
	stderrOut := errBuf.String()
	if strings.Contains(stderrOut, "<VM_IP>") {
		t.Errorf("access summary leaked to stderr:\n%s", stderrOut)
	}
}

// ─── 2.4 Engine op failure → exit 1 ─────────────────────────────────────────

func TestInstall_EngineFailure_Exit1(t *testing.T) {
	cleanup := installTestSetup(nil, fmt.Errorf("something broke"), nil)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ecErr *exitCodeError
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
	}
	if ecErr.code != ExitOpFailure {
		t.Errorf("exit code = %d, want %d (ExitOpFailure)", ecErr.code, ExitOpFailure)
	}
}

// ─── 2.4 Invalid --mongo value → exit 64 ─────────────────────────────────────

func TestInstall_InvalidMongo_Exit64(t *testing.T) {
	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "mongo", "invalid")

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error (exit 64), got nil")
	}
	var ecErr *exitCodeError
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
	}
	if ecErr.code != ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", ecErr.code, ExitUsage)
	}
}

// ─── 2.2 Summary masks secrets ────────────────────────────────────────────────

func TestInstall_SecretsMasked(t *testing.T) {
	cleanup := installTestSetup(fakeInstallResult, nil, nil)
	defer cleanup()

	// Script: all prompts answered, then confirm.
	stdinScript := "/tmp/test\nauto\nhttp://localhost:8000\nmy-secret-token\nadmin@test.com\nmypassword\ny\n"

	deps := installDeps{
		stdinIsTTY:  boolPtr(true),
		stderrIsTTY: boolPtr(true),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdinScript))

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, errBuf.String())
	}

	stderrOut := errBuf.String()
	// Secrets must not appear in summary on stderr.
	if strings.Contains(stderrOut, "my-secret-token") {
		t.Errorf("api-token secret leaked to stderr summary: %s", stderrOut)
	}
	if strings.Contains(stderrOut, "mypassword") {
		t.Errorf("admin-password secret leaked to stderr summary: %s", stderrOut)
	}
	// Masked values should appear.
	if !strings.Contains(stderrOut, "****") {
		t.Errorf("expected masked secrets (****) in summary, got: %s", stderrOut)
	}
}

// ─── 2.4 --mongo 7 with AVX → uses Mongo 7 image ────────────────────────────

func TestInstall_Mongo7_WithAVX(t *testing.T) {
	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
		avxDetect: func(_ context.Context, _ dockerx.FS) (bool, error) {
			return true, nil
		},
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "mongo", "7")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantImage := detect.MongoImage(true)
	if cap.opts.MongoImageOverride != wantImage {
		t.Errorf("MongoImageOverride = %q, want %q", cap.opts.MongoImageOverride, wantImage)
	}
}

// ─── 2.4 --mongo 4 → always uses Mongo 4 image ───────────────────────────────

func TestInstall_Mongo4(t *testing.T) {
	var cap capturedOpts
	cleanup := installTestSetup(fakeInstallResult, nil, &cap)
	defer cleanup()

	deps := installDeps{
		stdinIsTTY:  boolPtr(false),
		stderrIsTTY: boolPtr(false),
	}

	cmd := newInstallCmdWithDeps(deps)
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	mustSetFlag(t, cmd, "yes", "true")
	mustSetFlag(t, cmd, "mongo", "4")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantImage := detect.MongoImage(false)
	if cap.opts.MongoImageOverride != wantImage {
		t.Errorf("MongoImageOverride = %q, want %q", cap.opts.MongoImageOverride, wantImage)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func mustSetFlag(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
}
