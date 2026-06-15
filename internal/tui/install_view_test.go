package tui

// install_view_test.go — Install Wizard tests (task 8.2)
//
// Tests are driven by direct Update() calls to avoid async test races.
// These cover:
//   1. TestInstallWizard_FullRun          — scripted events → access summary
//   2. TestInstallWizard_ExistingGuard    — guard blocks execution
//   3. TestInstallWizard_FatalCheckBlocks — checks with Docker unavailable
//   4. TestInstallWizard_ExecutionStepFails — step failure path

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// ─── Fake deps constructors ───────────────────────────────────────────────────

// noInstallReadFile returns not-found for all paths (no existing installation).
func noInstallReadFile(_ string) ([]byte, error) { return nil, errNotFound }

// noInstallReadDir returns not-found for all paths.
func noInstallReadDir(_ string) ([]string, error) { return nil, errNotFound }

// existingInstallReadFile returns a docker-compose.yml with the agent image
// when asked for any path ending in docker-compose.yml.
func existingInstallReadFile(name string) ([]byte, error) {
	if strings.HasSuffix(name, "docker-compose.yml") {
		return []byte(`services:
  agent:
    image: crenein/c-network-agent-back:1.8.3
`), nil
	}
	return nil, errNotFound
}

// fakeInstallSuccess is a fake installFn that immediately returns a successful result
// with an access summary, without touching real engine or Docker.
func fakeInstallSuccess(_ context.Context, _ engine.Deps, _ engine.InstallOptions) (*engine.InstallResult, error) {
	return &engine.InstallResult{
		Steps: []engine.StepResult{
			{Name: "preflight"},
			{Name: "system-prep"},
			{Name: "directories"},
			{Name: "stack-up"},
		},
		AccessSummary: []engine.AccessEntry{
			{Label: "Backend API (HTTPS)", Value: "https://<VM_IP>:8000"},
			{Label: "Frontend (HTTPS)", Value: "https://<VM_IP>:443"},
			{Label: "Admin credentials", Value: "admin@example.com / **** (see .env)"},
		},
		Warnings: []string{},
	}, nil
}

// fakeInstallWithScriptedEvents is a fake installFn that emits scripted events
// through the reporter before returning. It drives the listen loop in real time.
func fakeInstallWithScriptedEvents(events []engine.Event) func(context.Context, engine.Deps, engine.InstallOptions) (*engine.InstallResult, error) {
	return func(_ context.Context, deps engine.Deps, _ engine.InstallOptions) (*engine.InstallResult, error) {
		for _, ev := range events {
			deps.Reporter.Report(ev)
		}
		return &engine.InstallResult{
			AccessSummary: []engine.AccessEntry{
				{Label: "Backend API (HTTPS)", Value: "https://<VM_IP>:8000"},
			},
		}, nil
	}
}

// fakeInstallError returns a fake installFn that fails at the given step.
func fakeInstallError(failStep string) func(context.Context, engine.Deps, engine.InstallOptions) (*engine.InstallResult, error) {
	return func(_ context.Context, deps engine.Deps, _ engine.InstallOptions) (*engine.InstallResult, error) {
		deps.Reporter.Report(engine.Event{Kind: engine.EventStepStarted, Step: failStep})
		deps.Reporter.Report(engine.Event{
			Kind: engine.EventStepFinished,
			Step: failStep,
			Err:  errors.New("stack-up failed: docker compose error"),
		})
		return nil, errors.New("stack-up failed: docker compose error")
	}
}

// ─── helper: build a fresh installView in mono profile ───────────────────────

func newTestInstallView(t *testing.T, deps installViewDeps) *installView {
	t.Helper()
	return newInstallView("v0.1.0", styles.NewProfile(true), deps)
}

// ─── helper: drive installView through all wizard steps to stepConfig ─────────

// driveToChecksComplete drives an installView from Init to having checks complete
// (using fake sysChecksResultMsg injection).
func driveToChecksComplete(t *testing.T, iv *installView, checksOK bool) *installView {
	t.Helper()
	// Inject guard check result: no existing installation
	m, _ := iv.Update(installGuardCheckedMsg{installDir: ""})
	iv = m.(*installView)

	// Inject checks result
	checks := []checkResult{
		{name: "Distro/OS", ok: checksOK, fatal: !checksOK, message: "Ubuntu 22.04 LTS"},
		{name: "AVX", ok: true, warn: false, message: "AVX supported → mongodb/mongodb-community-server:7.0-ubuntu2204"},
		{name: "Docker installed", ok: checksOK, fatal: !checksOK, message: "Docker binary found"},
		{name: "Docker daemon", ok: checksOK, fatal: !checksOK, message: "Docker daemon is running"},
		{name: "Docker Compose", ok: checksOK, fatal: !checksOK, message: "Compose variant: compose-v2"},
		{name: "Disk space", ok: checksOK, fatal: !checksOK, message: "10240 MB free"},
		{name: "Connectivity", ok: checksOK, fatal: !checksOK, message: "All endpoints reachable"},
		{name: "Root permissions", ok: checksOK, fatal: !checksOK, message: "Running as root"},
	}
	if !checksOK {
		checks[2].message = "Docker is not installed"
		checks[2].fix = "Install Docker: apt-get install docker-ce"
		checks[3].ok = false
		checks[3].fatal = true
		checks[3].message = "Docker daemon is not running"
		checks[3].fix = "Start the daemon: systemctl start docker"
	}
	m, _ = iv.Update(sysChecksResultMsg{checks: checks})
	iv = m.(*installView)
	return iv
}

// driveToConfigComplete drives to stepConfig and advances past it by injecting enter.
func driveToConfigComplete(t *testing.T, iv *installView) *installView {
	t.Helper()
	// Press enter to advance from stepChecks → stepConfig
	m, _ := iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepConfig {
		t.Fatalf("expected stepConfig, got %v", iv.step)
	}

	// Press enter to advance from stepConfig → stepPreview (inputs have valid defaults)
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepPreview {
		t.Fatalf("expected stepPreview, got %v", iv.step)
	}
	return iv
}

// ─── Test 1: Full Run ─────────────────────────────────────────────────────────

// TestInstallWizard_FullRun drives the wizard through all steps using injected
// messages and verifies the access summary is shown.
func TestInstallWizard_FullRun(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallSuccess,
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}

	iv := newTestInstallView(t, deps)

	// ── Step 1: checks pass ──
	iv = driveToChecksComplete(t, iv, true)
	if iv.step != stepChecks {
		t.Fatalf("expected stepChecks after checks, got %v", iv.step)
	}
	if iv.checksHaveFatal {
		t.Fatal("expected no fatal check failures")
	}

	// Verify checks render correctly
	view := iv.View()
	if !strings.Contains(view, "[OK]") {
		t.Errorf("checks view should show [OK] glyphs:\n%s", view)
	}
	if !strings.Contains(view, "MongoDB image") {
		t.Errorf("checks view should mention MongoDB image:\n%s", view)
	}

	// ── Step 2: navigate to config ──
	iv = driveToConfigComplete(t, iv)
	if iv.step != stepPreview {
		t.Fatalf("expected stepPreview, got %v", iv.step)
	}

	// Verify preview renders correctly
	view = iv.View()
	if !strings.Contains(view, "Preview") {
		t.Errorf("preview view missing 'Preview':\n%s", view)
	}
	if !strings.Contains(view, ".env") {
		t.Errorf("preview view missing .env mention:\n%s", view)
	}
	if !strings.Contains(view, "INFLUXDB_TOKEN") {
		t.Errorf("preview view missing .env keys:\n%s", view)
	}

	// ── Step 3: confirm preview → stepExecution ──
	m, cmd := iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	_ = cmd // cmd includes the engine goroutine + listen loop
	if iv.step != stepExecution {
		t.Fatalf("expected stepExecution after confirm, got %v", iv.step)
	}

	// Verify execution view renders step list
	view = iv.View()
	if !strings.Contains(view, "preflight") {
		t.Errorf("execution view missing 'preflight' step:\n%s", view)
	}

	// ── Step 4: inject engine events ──
	// Simulate steps arriving via TUI messages (as ListenEngine would deliver them)
	steps := []string{"preflight", "system-prep", "directories", "stack-up"}
	for _, s := range steps {
		m, _ = iv.Update(StepStartedMsg{Step: s})
		iv = m.(*installView)
		m, _ = iv.Update(StepDoneMsg{Step: s})
		iv = m.(*installView)
	}

	// ── Step 5: inject OperationFinished + installFinished ──
	m, _ = iv.Update(OperationFinishedMsg{})
	iv = m.(*installView)

	m, _ = iv.Update(installFinishedMsg{res: &engine.InstallResult{
		AccessSummary: []engine.AccessEntry{
			{Label: "Backend API (HTTPS)", Value: "https://<VM_IP>:8000"},
			{Label: "Admin credentials", Value: "admin@example.com / **** (see .env)"},
		},
		Warnings: nil,
	}})
	iv = m.(*installView)

	if iv.step != stepSummary {
		t.Fatalf("expected stepSummary after success, got %v", iv.step)
	}

	// Verify summary renders access summary
	view = iv.View()
	if !strings.Contains(view, "Backend API") {
		t.Errorf("summary view missing 'Backend API':\n%s", view)
	}
	if !strings.Contains(view, "https://<VM_IP>:8000") {
		t.Errorf("summary view missing backend URL:\n%s", view)
	}
	if !strings.Contains(view, "Installation Complete") {
		t.Errorf("summary view missing completion message:\n%s", view)
	}
}

// ─── Test 2: Existing-Installation Guard ─────────────────────────────────────

// TestInstallWizard_ExistingGuard verifies that when an existing installation
// is detected, the wizard shows a guard message and offers no execution path.
func TestInstallWizard_ExistingGuard(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallSuccess,
		readFile:  existingInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newTestInstallView(t, deps)

	// Inject guard result: existing install found
	m, _ := iv.Update(installGuardCheckedMsg{installDir: "."})
	iv = m.(*installView)

	if iv.step != stepExistingGuard {
		t.Fatalf("expected stepExistingGuard, got %v", iv.step)
	}
	if iv.existingDir == "" {
		t.Fatal("existingDir should be set")
	}

	// View should show guard message
	view := iv.View()
	if !strings.Contains(view, "Existing Installation") {
		t.Errorf("guard view should mention existing installation:\n%s", view)
	}
	if !strings.Contains(view, "NOT permitted") {
		t.Errorf("guard view should say installing is NOT permitted:\n%s", view)
	}

	// Should NOT contain any "confirm" or "begin installation" prompt
	if strings.Contains(view, "begin installation") {
		t.Errorf("guard view should NOT offer execution path:\n%s", view)
	}

	// Pressing enter should NOT advance to checks
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepExistingGuard {
		t.Errorf("guard: enter should not change step, got %v", iv.step)
	}
}

// ─── Test 3: Fatal Check Blocks ──────────────────────────────────────────────

// TestInstallWizard_FatalCheckBlocks verifies that when Docker is unavailable,
// the checks show ❌/[FAIL], fix suggestion is shown, and advance is blocked.
func TestInstallWizard_FatalCheckBlocks(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallSuccess,
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newTestInstallView(t, deps)

	// No existing install
	m, _ := iv.Update(installGuardCheckedMsg{installDir: ""})
	iv = m.(*installView)

	// Inject failing checks (Docker not installed)
	checks := []checkResult{
		{name: "Distro/OS", ok: true, message: "Ubuntu 22.04 LTS"},
		{name: "AVX", ok: false, warn: true, message: "No AVX → mongo:4.4 will be used"},
		{name: "Docker installed", ok: false, fatal: true,
			message: "Docker is not installed",
			fix:     "Install Docker: apt-get install docker-ce docker-ce-cli containerd.io docker-compose-plugin",
		},
		{name: "Docker daemon", ok: false, fatal: true,
			message: "Docker daemon is not running",
			fix:     "Start the daemon: systemctl start docker",
		},
		{name: "Disk space", ok: true, message: "10240 MB free"},
		{name: "Connectivity", ok: true, message: "All endpoints reachable"},
		{name: "Root permissions", ok: true, message: "Running as root"},
	}
	m, _ = iv.Update(sysChecksResultMsg{checks: checks})
	iv = m.(*installView)

	if !iv.checksHaveFatal {
		t.Fatal("expected checksHaveFatal=true when Docker is not installed")
	}

	// View should show FAIL glyph and fix suggestion
	view := iv.View()
	if !strings.Contains(view, "[FAIL]") {
		t.Errorf("checks view should show [FAIL] glyph:\n%s", view)
	}
	if !strings.Contains(view, "Docker is not installed") {
		t.Errorf("checks view should mention Docker not installed:\n%s", view)
	}
	if !strings.Contains(view, "Install Docker") {
		t.Errorf("checks view should show fix suggestion:\n%s", view)
	}
	if !strings.Contains(view, "FATAL") {
		t.Errorf("checks view should mention fatal failures:\n%s", view)
	}

	// Pressing enter should NOT advance to config
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepChecks {
		t.Errorf("fatal check: enter should not advance step, got %v", iv.step)
	}

	// Re-run (press r) should restart checks
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	iv = m.(*installView)
	if !iv.checksRunning {
		t.Error("after 'r', checksRunning should be true")
	}
	if iv.checks != nil {
		t.Error("after 'r', checks should be reset to nil")
	}
}

// ─── Test 4: Execution Step Fails ────────────────────────────────────────────

// TestInstallWizard_ExecutionStepFails verifies that when a step fails during
// execution, the failed step shows ❌, remaining steps show as "not executed",
// and no success message is shown.
func TestInstallWizard_ExecutionStepFails(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallError("stack-up"),
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newTestInstallView(t, deps)

	// Drive to execution
	iv = driveToChecksComplete(t, iv, true)
	iv = driveToConfigComplete(t, iv)

	// Confirm preview
	m, _ := iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepExecution {
		t.Fatalf("expected stepExecution, got %v", iv.step)
	}

	// Simulate the steps up to failure
	m, _ = iv.Update(StepStartedMsg{Step: "preflight"})
	iv = m.(*installView)
	m, _ = iv.Update(StepDoneMsg{Step: "preflight"})
	iv = m.(*installView)

	// stack-up step fails
	m, _ = iv.Update(StepStartedMsg{Step: "stack-up"})
	iv = m.(*installView)
	m, _ = iv.Update(StepFailedMsg{Step: "stack-up", Err: errors.New("stack-up failed: docker compose error")})
	iv = m.(*installView)

	// Steps after failure should be marked skipped
	for _, s := range iv.execSteps {
		if s.name == "stack-up" {
			if s.status != "failed" {
				t.Errorf("stack-up step should be 'failed', got %q", s.status)
			}
		}
		// Steps after stack-up should be skipped (not "pending")
		afterStackUp := false
		for _, n := range orderedExecSteps {
			if n == "stack-up" {
				afterStackUp = true
				continue
			}
			if afterStackUp && s.name == n {
				if s.status != "skipped" {
					t.Errorf("step %q after failure should be 'skipped', got %q", s.name, s.status)
				}
			}
		}
	}

	// Inject install failure
	m, _ = iv.Update(OperationFinishedMsg{})
	iv = m.(*installView)
	m, _ = iv.Update(installFinishedMsg{
		res: nil,
		err: errors.New("stack-up failed: docker compose error"),
	})
	iv = m.(*installView)

	// Should NOT have transitioned to stepSummary
	if iv.step == stepSummary {
		t.Error("should not advance to stepSummary when install fails")
	}
	if iv.step != stepExecution {
		t.Errorf("on failure should stay at stepExecution, got %v", iv.step)
	}

	// View should show failure notice
	view := iv.View()
	if !strings.Contains(view, "[FAIL]") {
		t.Errorf("execution fail view should show [FAIL]:\n%s", view)
	}
	if !strings.Contains(view, "Installation failed") {
		t.Errorf("execution fail view should mention failure:\n%s", view)
	}

	// Should NOT claim success
	if strings.Contains(view, "Installation Complete") || strings.Contains(view, "successful") {
		t.Errorf("failure view should NOT claim success:\n%s", view)
	}
}

// ─── Async teatest test for FullRun ──────────────────────────────────────────

// TestInstallWizard_FullRunAsync uses teatest to drive the install wizard
// through a scripted events sequence with the real bubbletea runtime.
func TestInstallWizard_FullRunAsync(t *testing.T) {
	scriptedEvents := []engine.Event{
		{Kind: engine.EventStepStarted, Step: "preflight"},
		{Kind: engine.EventStepFinished, Step: "preflight"},
		{Kind: engine.EventStepStarted, Step: "stack-up"},
		{Kind: engine.EventStepFinished, Step: "stack-up"},
	}

	deps := installViewDeps{
		installFn: fakeInstallWithScriptedEvents(scriptedEvents),
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newInstallView("v0.1.0", styles.NewProfile(true), deps)

	tm := teatest.NewTestModel(t, iv, teatest.WithInitialTermSize(80, 24))

	// Wait for wizard to load (guard check fires)
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		s := string(bts)
		return strings.Contains(s, "System Checks") || strings.Contains(s, "Checking for existing")
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}

// ─── Helper: Test config form validation ─────────────────────────────────────

// TestInstallWizard_ConfigValidation verifies that invalid email blocks advance.
func TestInstallWizard_ConfigValidation(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallSuccess,
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newTestInstallView(t, deps)
	iv = driveToChecksComplete(t, iv, true)

	// Navigate to config
	m, _ := iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)
	if iv.step != stepConfig {
		t.Fatalf("expected stepConfig, got %v", iv.step)
	}

	// Set an invalid email
	iv.inputs[fieldAdminEmail].SetValue("not-an-email")

	// Try to advance — should be blocked due to validation error
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)

	if iv.step == stepPreview {
		t.Error("config with invalid email should NOT advance to preview")
	}
	if iv.configErrors[fieldAdminEmail] == "" {
		t.Error("should have a validation error for invalid email")
	}

	// Fix the email and try again
	iv.inputs[fieldAdminEmail].SetValue("valid@example.com")
	m, _ = iv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	iv = m.(*installView)

	if iv.step != stepPreview {
		t.Errorf("with valid email should advance to preview, got %v", iv.step)
	}
}

// TestInstallWizard_RenderAllSteps verifies that all expected engine steps
// appear in the execution view.
func TestInstallWizard_RenderAllSteps(t *testing.T) {
	deps := installViewDeps{
		installFn: fakeInstallSuccess,
		readFile:  noInstallReadFile,
		readDir:   noInstallReadDir,
	}
	iv := newTestInstallView(t, deps)
	iv = driveToChecksComplete(t, iv, true)
	iv = driveToConfigComplete(t, iv)

	m, _ := iv.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm preview
	iv = m.(*installView)

	view := iv.View()
	for _, step := range orderedExecSteps {
		if !strings.Contains(view, step) {
			t.Errorf("execution view missing step %q:\n%s", step, view)
		}
	}
}
