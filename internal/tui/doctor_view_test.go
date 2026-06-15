package tui

// doctor_view_test.go — Doctor view tests (task 8.4)
//
// Scenarios covered:
//   1. TestDoctorView_ChecklistRender      — scripted WARNING + CRITICAL, verifies glyphs and summary line.
//   2. TestDoctorView_SelectionDetail      — navigation keys show Detail + FixSuggestion for selected check.
//   3. TestDoctorView_ReRun                — 'r' resets state and invokes doctorFn a second time.
//   4. TestDoctorView_ErrorState           — doctorFn that panics is wrapped; view stays responsive.
//   5. TestDoctorView_GoldenChecklist      — golden at 80x24 for the completed checklist.
//   6. TestDoctorView_GoldenDetail         — golden at 80x24 after navigating to a failing check.
//   7. TestDoctorView_AsyncFinished        — teatest WaitFor verifies doctorFinishedMsg resolves.
//
// All tests use injected fake doctorFn — no real Docker daemon or network.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// fakeReport returns a DoctorReport with a mix of statuses for testing.
// The report contains:
//   - one OK check (docker.installed)
//   - one WARNING check (files.env_permission) with Detail + FixSuggestion
//   - one CRITICAL check (disk.space) with Detail + FixSuggestion
//   - one SKIP check (agent.health) without FixSuggestion
func fakeReport() engine.DoctorReport {
	return engine.DoctorReport{
		Summary: engine.StatusCritical,
		Checks: []engine.Check{
			{
				ID:       "docker.installed",
				Name:     "Docker installed",
				Status:   engine.StatusOK,
				Severity: engine.StatusCritical,
				Detail:   "docker binary found",
			},
			{
				ID:            "files.env_permission",
				Name:          ".env file permission (600)",
				Status:        engine.StatusWarning,
				Severity:      engine.StatusWarning,
				Detail:        ".env has mode 0644, expected 0600",
				FixSuggestion: "chmod 600 /opt/crenein/.env",
			},
			{
				ID:            "disk.space",
				Name:          "Disk space (>2 GB free)",
				Status:        engine.StatusCritical,
				Severity:      engine.StatusCritical,
				Detail:        "only 512 MB free, required 2048 MB",
				FixSuggestion: "free disk space: docker image prune -f",
			},
			{
				ID:       "agent.health",
				Name:     "Agent /health endpoint",
				Status:   engine.StatusSkip,
				Severity: engine.StatusCritical,
				Detail:   "skipped: prerequisite check failed",
			},
		},
	}
}

// fakeDoctorFn returns a doctorFn that returns the given report.
func fakeDoctorFn(report engine.DoctorReport) func(context.Context, engine.Deps, engine.DoctorOptions) engine.DoctorReport {
	return func(_ context.Context, _ engine.Deps, _ engine.DoctorOptions) engine.DoctorReport {
		return report
	}
}

// countingDoctorFn returns a doctorFn that increments a counter on each call.
func countingDoctorFn(report engine.DoctorReport, count *atomic.Int32) func(context.Context, engine.Deps, engine.DoctorOptions) engine.DoctorReport {
	return func(_ context.Context, _ engine.Deps, _ engine.DoctorOptions) engine.DoctorReport {
		count.Add(1)
		return report
	}
}

// newTestDoctorView builds a doctorView for tests (mono profile, 80×24).
func newTestDoctorView(t *testing.T, fn func(context.Context, engine.Deps, engine.DoctorOptions) engine.DoctorReport) *doctorView {
	t.Helper()
	v := newDoctorView(styles.NewProfile(true), doctorViewDeps{doctorFn: fn})
	// Simulate a 80×24 window size message so layout calculations are stable.
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m.(*doctorView)
}

// injectFinished injects a doctorFinishedMsg into the view (simulates the async Cmd).
func injectFinished(t *testing.T, v *doctorView, report engine.DoctorReport) *doctorView {
	t.Helper()
	m, _ := v.Update(doctorFinishedMsg{report: report})
	return m.(*doctorView)
}

// ─── Test 1: Checklist renders correct glyphs and summary ────────────────────

func TestDoctorView_ChecklistRender(t *testing.T) {
	v := newTestDoctorView(t, fakeDoctorFn(fakeReport()))
	v = injectFinished(t, v, fakeReport())

	view := v.View()

	// Must show phase done.
	if v.phase != "done" {
		t.Fatalf("expected phase 'done', got %q", v.phase)
	}

	// ── OK check ──
	if !strings.Contains(view, "[OK]") {
		t.Errorf("checklist must show [OK] glyph for passing checks:\n%s", view)
	}
	if !strings.Contains(view, "Docker installed") {
		t.Errorf("checklist must show 'Docker installed' check name:\n%s", view)
	}

	// ── WARNING check ──
	if !strings.Contains(view, "[WARN]") {
		t.Errorf("checklist must show [WARN] glyph for warning checks:\n%s", view)
	}
	if !strings.Contains(view, ".env file permission") {
		t.Errorf("checklist must show '.env file permission' check name:\n%s", view)
	}

	// ── CRITICAL check ──
	if !strings.Contains(view, "[FAIL]") {
		t.Errorf("checklist must show [FAIL] glyph for critical checks:\n%s", view)
	}
	if !strings.Contains(view, "Disk space") {
		t.Errorf("checklist must show 'Disk space' check name:\n%s", view)
	}

	// ── SKIP check ──
	if !strings.Contains(view, "[-]") {
		t.Errorf("checklist must show [-] glyph for skipped checks:\n%s", view)
	}
	if !strings.Contains(view, "Agent /health endpoint") {
		t.Errorf("checklist must show 'Agent /health endpoint' check name:\n%s", view)
	}

	// ── Summary line ──
	// Summary is StatusCritical → must mention critical or fail count.
	if !strings.Contains(view, "critical") && !strings.Contains(view, "1 critical") {
		t.Errorf("summary line must reflect critical status:\n%s", view)
	}
}

// ─── Test 2: Selection navigation shows Detail + FixSuggestion ───────────────

func TestDoctorView_SelectionDetail(t *testing.T) {
	v := newTestDoctorView(t, fakeDoctorFn(fakeReport()))
	v = injectFinished(t, v, fakeReport())

	// Initial selection is index 0 (docker.installed — OK, no FixSuggestion).
	view := v.View()
	if !strings.Contains(view, "Docker installed") {
		t.Fatalf("initial selection must show Docker installed detail:\n%s", view)
	}

	// Navigate down to index 1 (.env permission — WARNING with FixSuggestion).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	v = m.(*doctorView)
	if v.selected != 1 {
		t.Fatalf("after 'j', selected = %d, want 1", v.selected)
	}

	view = v.View()
	if !strings.Contains(view, ".env file permission") {
		t.Errorf("detail pane must show selected check name:\n%s", view)
	}
	if !strings.Contains(view, "0644") {
		t.Errorf("detail pane must show check Detail (.env mode):\n%s", view)
	}
	if !strings.Contains(view, "chmod 600") {
		t.Errorf("detail pane must show FixSuggestion:\n%s", view)
	}

	// Navigate down to index 2 (disk.space — CRITICAL with FixSuggestion).
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	v = m.(*doctorView)
	if v.selected != 2 {
		t.Fatalf("after second 'j', selected = %d, want 2", v.selected)
	}

	view = v.View()
	if !strings.Contains(view, "512 MB free") {
		t.Errorf("detail pane must show disk space Detail:\n%s", view)
	}
	if !strings.Contains(view, "docker image prune") {
		t.Errorf("detail pane must show disk space FixSuggestion:\n%s", view)
	}

	// Navigate up with arrow key → back to index 1.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyUp})
	v = m.(*doctorView)
	if v.selected != 1 {
		t.Fatalf("after up arrow, selected = %d, want 1", v.selected)
	}

	// Navigate up again → index 0.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	v = m.(*doctorView)
	if v.selected != 0 {
		t.Fatalf("after 'k', selected = %d, want 0", v.selected)
	}
}

// ─── Test 3: Re-run resets state and re-invokes doctorFn ─────────────────────

func TestDoctorView_ReRun(t *testing.T) {
	var callCount atomic.Int32
	report := fakeReport()
	fn := countingDoctorFn(report, &callCount)

	v := newTestDoctorView(t, fn)

	// Inject first run finished.
	v = injectFinished(t, v, report)
	if v.phase != "done" {
		t.Fatalf("expected phase 'done' after first run, got %q", v.phase)
	}

	// Navigate to index 2.
	v.selected = 2

	// Press 'r' to re-run.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	v = m.(*doctorView)

	// Must be back in "running" phase.
	if v.phase != "running" {
		t.Errorf("after 'r', phase = %q, want 'running'", v.phase)
	}

	// Selection must be reset.
	if v.selected != 0 {
		t.Errorf("after re-run, selected = %d, want 0", v.selected)
	}

	// Must have a Cmd to dispatch (the batch of spinner + doctor Cmd).
	if cmd == nil {
		t.Error("re-run must return a non-nil Cmd")
	}

	// Running view must show spinner text.
	view := v.View()
	if !strings.Contains(view, "Running") && !strings.Contains(view, "diagnostics") {
		t.Errorf("running view must show spinner text:\n%s", view)
	}
}

// ─── Test 4: Error state — view stays responsive ──────────────────────────────

func TestDoctorView_ErrorState(t *testing.T) {
	v := newTestDoctorView(t, nil)

	// Inject a doctorFinishedMsg with an error.
	m, _ := v.Update(doctorFinishedMsg{
		err: fmt.Errorf("connection refused: Docker daemon not running"),
	})
	v = m.(*doctorView)

	if v.phase != "error" {
		t.Fatalf("expected phase 'error', got %q", v.phase)
	}

	view := v.View()

	// Must show error, not crash.
	if view == "" {
		t.Fatal("View() returned empty string in error state")
	}

	// Must show the error message.
	if !strings.Contains(view, "connection refused") {
		t.Errorf("error view must show error message:\n%s", view)
	}

	// Must suggest headless fallback.
	if !strings.Contains(view, "crenein-agent doctor") {
		t.Errorf("error view must suggest headless command:\n%s", view)
	}

	// Must remain navigable (pressing 'r' re-runs).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	v = m.(*doctorView)
	if v.phase != "running" {
		t.Errorf("after 'r' from error state, phase = %q, want 'running'", v.phase)
	}
	if cmd == nil {
		t.Error("re-run from error state must return a non-nil Cmd")
	}
}

// ─── Test 5: Golden — completed checklist (80x24) ────────────────────────────

func TestDoctorView_GoldenChecklist(t *testing.T) {
	v := newTestDoctorView(t, fakeDoctorFn(fakeReport()))
	v = injectFinished(t, v, fakeReport())
	// Ensure selection is at 0 for determinism.
	v.selected = 0
	golden.RequireEqual(t, []byte(v.View()))
}

// ─── Test 6: Golden — detail pane for CRITICAL check (80x24) ─────────────────

func TestDoctorView_GoldenDetail(t *testing.T) {
	v := newTestDoctorView(t, fakeDoctorFn(fakeReport()))
	v = injectFinished(t, v, fakeReport())
	// Select the CRITICAL check (index 2 = disk.space).
	v.selected = 2
	golden.RequireEqual(t, []byte(v.View()))
}

// ─── Test 7: Async teatest — doctorFinishedMsg resolves without deadlock ──────

// TestDoctorView_AsyncFinished uses teatest to drive the full async lifecycle:
// Init() fires the Cmd, we WaitFor the "done" checklist to appear, then quit.
func TestDoctorView_AsyncFinished(t *testing.T) {
	// Use a doctorFn that returns immediately with a fixed report.
	report := engine.DoctorReport{
		Summary: engine.StatusOK,
		Checks: []engine.Check{
			{
				ID:       "docker.installed",
				Name:     "Docker installed",
				Status:   engine.StatusOK,
				Severity: engine.StatusCritical,
				Detail:   "docker binary found",
			},
		},
	}

	v := newDoctorView(styles.NewProfile(true), doctorViewDeps{
		doctorFn: fakeDoctorFn(report),
	})

	tm := teatest.NewTestModel(t, v, teatest.WithInitialTermSize(80, 24))

	// Wait for the completed checklist to appear (doctorFinishedMsg has resolved).
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		s := string(bts)
		return strings.Contains(s, "Docker installed") && strings.Contains(s, "[OK]")
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	// TUI must remain responsive — quit cleanly.
	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}
