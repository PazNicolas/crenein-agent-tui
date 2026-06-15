package tui

// update_view_test.go — Update Wizard tests (task 8.3)
//
// Scenarios covered:
//   1. TestUpdateWizard_VersionPreview      — 1.8.3 → 1.8.4 with release notes,
//                                             confirm message, update/no-touch lists
//   2. TestUpdateWizard_AlreadyUpToDate     — installed == latest → at-date indicator
//   3. TestUpdateWizard_ManifestUnavailable — manifest error → degraded state, no crash
//   4. TestUpdateWizard_SuccessfulRun       — full scripted events → success result
//   5. TestUpdateWizard_HealthFailRollback  — health step fails → rollback shown, no success
//   6. TestUpdateWizard_RollbackFailed      — rollback also fails → inconsistent state shown
//
// All tests use injected fakes — no real Docker daemon or network.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
)

// ─── Fake manifest client ─────────────────────────────────────────────────────

// fakeUpdateManifestClient implements release.Client for update wizard tests.
type fakeUpdateManifestClient struct {
	manifest     *release.Manifest
	fetchErr     error
	agentVersion string // returned by DetectAgentVersion
}

func (f *fakeUpdateManifestClient) FetchManifest(_ context.Context, _ bool) (*release.Manifest, *cnerr.Error) {
	if f.fetchErr != nil {
		return nil, cnerr.Wrap("fake.FetchManifest", f.fetchErr, "fake error")
	}
	return f.manifest, nil
}

func (f *fakeUpdateManifestClient) DetectAgentVersion(_ context.Context) string {
	return f.agentVersion
}

// buildFakeManifest builds a valid manifest with current and target versions.
func buildFakeManifest(latestVersion string) *release.Manifest {
	return &release.Manifest{
		Agent: release.AgentSection{
			Latest: latestVersion,
			Releases: map[string]release.AgentRelease{
				"1.8.3": {
					Image: "crenein/c-network-agent-back:1.8.3",
					Date:  "2026-06-01",
					Notes: "Bug fixes and performance improvements.",
					Mongo: map[string]string{"7": "mongodb:7.0", "4": "mongo:4.4"},
				},
				"1.8.4": {
					Image: "crenein/c-network-agent-back:1.8.4",
					Date:  "2026-06-15",
					Notes: "New dashboard features and security patches.",
					Mongo: map[string]string{"7": "mongodb:7.0", "4": "mongo:4.4"},
				},
			},
		},
		CLI: release.CLISection{
			Latest: "0.1.0",
			Releases: map[string]release.CLIRelease{
				"0.1.0": {Date: "2026-06-01"},
			},
		},
		FetchedAt: time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
	}
}

// ─── Fake updateFn helpers ────────────────────────────────────────────────────

// fakeUpdateSuccess is an updateFn that emits a scripted event sequence and
// returns a successful UpdateResult (no rollback).
func fakeUpdateSuccess(events []engine.Event, backupPath, newAgentID string) func(context.Context, engine.Deps, engine.UpdateOptions) (*engine.UpdateResult, error) {
	return func(_ context.Context, deps engine.Deps, _ engine.UpdateOptions) (*engine.UpdateResult, error) {
		for _, ev := range events {
			deps.Reporter.Report(ev)
		}
		return &engine.UpdateResult{
			NewAgentImageID:      newAgentID,
			PreviousAgentImageID: "sha256:old",
			BackupPath:           backupPath,
			RolledBack:           false,
			RollbackFailed:       false,
		}, nil
	}
}

// ─── Helper: build a fresh updateView in mono profile ─────────────────────────

func newTestUpdateView(t *testing.T, mc release.Client, updateFn func(context.Context, engine.Deps, engine.UpdateOptions) (*engine.UpdateResult, error)) *updateView {
	t.Helper()
	return newUpdateView("v0.1.0", styles.NewProfile(true), updateViewDeps{
		updateFn:       updateFn,
		manifestClient: mc,
	})
}

// ─── Helper: inject preview loaded message directly ───────────────────────────

func injectPreviewLoaded(t *testing.T, uv *updateView, msg updatePreviewLoadedMsg) *updateView {
	t.Helper()
	m, _ := uv.Update(msg)
	return m.(*updateView)
}

// ─── Test 1: Version Preview with release notes ───────────────────────────────

func TestUpdateWizard_VersionPreview(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.4"),
		agentVersion: "1.8.3",
	}
	uv := newTestUpdateView(t, mc, nil)

	// Inject preview loaded message (simulates what loadPreviewCmd returns).
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.4",
		releaseNotes:    "New dashboard features and security patches.",
		alreadyUpToDate: false,
	})

	view := uv.View()

	// Must show current → target version.
	if !strings.Contains(view, "1.8.3") {
		t.Errorf("preview must show current version 1.8.3:\n%s", view)
	}
	if !strings.Contains(view, "1.8.4") {
		t.Errorf("preview must show target version 1.8.4:\n%s", view)
	}
	if !strings.Contains(view, "→") {
		t.Errorf("preview must show upgrade arrow →:\n%s", view)
	}

	// Must show release notes.
	if !strings.Contains(view, "New dashboard features") {
		t.Errorf("preview must show release notes:\n%s", view)
	}

	// Must list what is updated (agent, frontend).
	if !strings.Contains(view, "agent") {
		t.Errorf("preview must list agent as updated:\n%s", view)
	}
	if !strings.Contains(view, "frontend") {
		t.Errorf("preview must list frontend as updated:\n%s", view)
	}

	// Must list what will NOT be touched.
	if !strings.Contains(view, "mongodb") {
		t.Errorf("preview must mention mongodb as NOT updated:\n%s", view)
	}
	if !strings.Contains(view, "influxdb") {
		t.Errorf("preview must mention influxdb as NOT updated:\n%s", view)
	}
	if !strings.Contains(view, "redis") {
		t.Errorf("preview must mention redis as NOT updated:\n%s", view)
	}
	if !strings.Contains(view, "/data") {
		t.Errorf("preview must mention /data/* as NOT updated:\n%s", view)
	}

	// Must NOT have started the update yet.
	if uv.step != updateStepPreview {
		t.Errorf("step should still be preview, got %v", uv.step)
	}

	// Pressing enter should advance to execution.
	m, cmd := uv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	uv = m.(*updateView)
	_ = cmd
	if uv.step != updateStepExecution {
		t.Errorf("after confirm, step should be execution, got %v", uv.step)
	}
}

// ─── Test 2: Already Up-to-Date ───────────────────────────────────────────────

func TestUpdateWizard_AlreadyUpToDate(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.3"), // same as installed
		agentVersion: "1.8.3",
	}
	uv := newTestUpdateView(t, mc, nil)

	// Inject preview: already up to date.
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.3",
		alreadyUpToDate: true,
	})

	view := uv.View()

	// Must state up-to-date.
	if !strings.Contains(view, "up to date") && !strings.Contains(view, "up-to-date") {
		t.Errorf("view must state agent is up to date:\n%s", view)
	}

	// Must NOT offer an update action (no confirm/enter prompt for update).
	if strings.Contains(view, "confirm and begin") {
		t.Errorf("view must NOT offer update action when already up to date:\n%s", view)
	}

	// Pressing enter should NOT start execution — it should navigate back.
	m, _ := uv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	uv = m.(*updateView)
	if uv.step == updateStepExecution {
		t.Error("pressing enter when up-to-date should NOT start execution")
	}
}

// ─── Test 3: Manifest Unavailable ────────────────────────────────────────────

func TestUpdateWizard_ManifestUnavailable(t *testing.T) {
	uv := newTestUpdateView(t, nil, nil)

	// Inject preview error.
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion: "unknown",
		err:            errors.New("network error: connection refused"),
	})

	view := uv.View()

	// Must show a clear degraded state without crashing.
	if !strings.Contains(view, "unavailable") {
		t.Errorf("view must indicate manifest unavailable:\n%s", view)
	}

	// Must NOT claim success or start update.
	if strings.Contains(view, "begin the update") {
		t.Errorf("view must NOT offer update when manifest unavailable:\n%s", view)
	}
	if uv.step == updateStepExecution {
		t.Error("step must not be execution when manifest unavailable")
	}
}

// ─── Test 4: Successful Run (direct message injection) ───────────────────────

func TestUpdateWizard_SuccessfulRun(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.4"),
		agentVersion: "1.8.3",
	}
	uv := newTestUpdateView(t, mc, nil)

	// Load preview.
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.4",
		releaseNotes:    "New dashboard features.",
		alreadyUpToDate: false,
	})

	// Confirm to advance to execution.
	m, _ := uv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	uv = m.(*updateView)
	if uv.step != updateStepExecution {
		t.Fatalf("expected updateStepExecution, got %v", uv.step)
	}

	// Inject engine step events.
	steps := []struct{ started, done string }{
		{"backup", "backup"},
		{"pull", "pull"},
		{"recreate", "recreate"},
		{"health-backend", "health-backend"},
		{"health-frontend", "health-frontend"},
		{"health-databases", "health-databases"},
	}
	for _, s := range steps {
		m, _ = uv.Update(StepStartedMsg{Step: s.started})
		uv = m.(*updateView)
		m, _ = uv.Update(StepDoneMsg{Step: s.done})
		uv = m.(*updateView)
	}

	// Check execution view shows steps.
	execView := uv.View()
	if !strings.Contains(execView, "backup") {
		t.Errorf("execution view must show backup step:\n%s", execView)
	}
	if !strings.Contains(execView, "pull") {
		t.Errorf("execution view must show pull step:\n%s", execView)
	}

	// Inject OperationFinished then result.
	m, _ = uv.Update(OperationFinishedMsg{})
	uv = m.(*updateView)
	m, _ = uv.Update(updateFinishedMsg{
		res: &engine.UpdateResult{
			NewAgentImageID:      "sha256:new",
			PreviousAgentImageID: "sha256:old",
			BackupPath:           "/opt/crenein/.backups/20260615_120000",
			RolledBack:           false,
			RollbackFailed:       false,
		},
	})
	uv = m.(*updateView)

	if uv.step != updateStepResult {
		t.Fatalf("expected updateStepResult, got %v", uv.step)
	}

	resultView := uv.View()

	// Must show success indicators.
	if !strings.Contains(resultView, "Update Complete") && !strings.Contains(resultView, "successful") {
		t.Errorf("result view must show success:\n%s", resultView)
	}

	// Must show new version.
	if !strings.Contains(resultView, "1.8.4") {
		t.Errorf("result view must show target version 1.8.4:\n%s", resultView)
	}

	// Must show backup path.
	if !strings.Contains(resultView, ".backups") {
		t.Errorf("result view must show backup path:\n%s", resultView)
	}

	// Must NOT claim rollback or failure.
	if strings.Contains(resultView, "Rollback") || strings.Contains(resultView, "rollback") {
		t.Errorf("success result must NOT mention rollback:\n%s", resultView)
	}
	if strings.Contains(resultView, "[FAIL]") {
		t.Errorf("success result must NOT show [FAIL]:\n%s", resultView)
	}
}

// ─── Test 5: Health Check Failure → Rollback (rollback OK) ───────────────────

func TestUpdateWizard_HealthFailRollback(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.4"),
		agentVersion: "1.8.3",
	}
	uv := newTestUpdateView(t, mc, nil)

	// Load preview and confirm.
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.4",
		alreadyUpToDate: false,
	})
	m, _ := uv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	uv = m.(*updateView)

	// Inject events: health-backend fails → rollback.
	events := []tea.Msg{
		StepStartedMsg{Step: "backup"},
		StepDoneMsg{Step: "backup"},
		StepStartedMsg{Step: "pull"},
		StepDoneMsg{Step: "pull"},
		StepStartedMsg{Step: "recreate"},
		StepDoneMsg{Step: "recreate"},
		StepStartedMsg{Step: "health-backend"},
		StepFailedMsg{Step: "health-backend", Err: errors.New("backend did not become healthy within timeout")},
		StepStartedMsg{Step: "rollback"},
		StepDoneMsg{Step: "rollback"},
		OperationFinishedMsg{},
	}
	for _, ev := range events {
		m, _ = uv.Update(ev)
		uv = m.(*updateView)
	}

	// Inject final result: RolledBack=true, RollbackFailed=false.
	m, _ = uv.Update(updateFinishedMsg{
		res: &engine.UpdateResult{
			PreviousAgentImageID: "sha256:old",
			BackupPath:           "/opt/crenein/.backups/20260615_120000",
			RolledBack:           true,
			RollbackFailed:       false,
		},
	})
	uv = m.(*updateView)

	if uv.step != updateStepResult {
		t.Fatalf("expected updateStepResult after rollback, got %v", uv.step)
	}

	resultView := uv.View()

	// Must NOT claim success.
	if strings.Contains(resultView, "Update Complete") || strings.Contains(resultView, "successful!") {
		t.Errorf("rollback result must NOT claim success:\n%s", resultView)
	}

	// Must mention rollback.
	if !strings.Contains(resultView, "rollback") && !strings.Contains(resultView, "Rollback") {
		t.Errorf("rollback result must mention rollback:\n%s", resultView)
	}

	// Must show [FAIL] glyph.
	if !strings.Contains(resultView, "[FAIL]") {
		t.Errorf("rollback result must show [FAIL]:\n%s", resultView)
	}

	// Must show backup path.
	if !strings.Contains(resultView, ".backups") {
		t.Errorf("rollback result must show backup path:\n%s", resultView)
	}

	// Must indicate rollback completed (not failed).
	if strings.Contains(resultView, "ALSO FAILED") {
		t.Errorf("should NOT say rollback ALSO FAILED when rollback succeeded:\n%s", resultView)
	}
	if !strings.Contains(resultView, "Rollback") {
		t.Errorf("must mention rollback result:\n%s", resultView)
	}
}

// ─── Test 6: Health Check Failure → Rollback ALSO Failed ─────────────────────

func TestUpdateWizard_RollbackFailed(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.4"),
		agentVersion: "1.8.3",
	}
	uv := newTestUpdateView(t, mc, nil)

	// Load preview and confirm.
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.4",
		alreadyUpToDate: false,
	})
	m, _ := uv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	uv = m.(*updateView)

	// Inject events including rollback failure.
	events := []tea.Msg{
		StepStartedMsg{Step: "backup"},
		StepDoneMsg{Step: "backup"},
		StepStartedMsg{Step: "health-backend"},
		StepFailedMsg{Step: "health-backend", Err: errors.New("backend did not become healthy within timeout")},
		StepStartedMsg{Step: "rollback"},
		StepFailedMsg{Step: "rollback", Err: errors.New("previous agent image ID is unknown")},
		OperationFinishedMsg{},
	}
	for _, ev := range events {
		m, _ = uv.Update(ev)
		uv = m.(*updateView)
	}

	rbErr := cnerr.New("engine.update.rollback failed", "manual recovery required: docker tag <previous> crenein/c-network-agent-back:latest")
	m, _ = uv.Update(updateFinishedMsg{
		res: &engine.UpdateResult{
			BackupPath:     "/opt/crenein/.backups/20260615_120000",
			RolledBack:     true,
			RollbackFailed: true,
		},
		err: rbErr,
	})
	uv = m.(*updateView)

	resultView := uv.View()

	// Must NOT claim success.
	if strings.Contains(resultView, "successful!") || strings.Contains(resultView, "Update Complete") {
		t.Errorf("rollback-failed result must NOT claim success:\n%s", resultView)
	}

	// Must say rollback ALSO failed / inconsistent state.
	if !strings.Contains(resultView, "ALSO FAILED") && !strings.Contains(resultView, "inconsistent") {
		t.Errorf("must indicate rollback also failed:\n%s", resultView)
	}

	// Must show manual recovery instructions.
	if !strings.Contains(resultView, "Manual recovery") && !strings.Contains(resultView, "docker compose") {
		t.Errorf("must show manual recovery instructions:\n%s", resultView)
	}
}

// ─── Test 7: Async teatest — version preview renders without deadlock ─────────

// TestUpdateWizard_AsyncPreview uses teatest to verify the wizard starts
// loading the preview without deadlock.
func TestUpdateWizard_AsyncPreview(t *testing.T) {
	mc := &fakeUpdateManifestClient{
		manifest:     buildFakeManifest("1.8.4"),
		agentVersion: "1.8.3",
	}
	uv := newUpdateView("v0.1.0", styles.NewProfile(true), updateViewDeps{
		manifestClient: mc,
		updateFn: fakeUpdateSuccess(
			[]engine.Event{
				{Kind: engine.EventStepStarted, Step: "backup"},
				{Kind: engine.EventStepFinished, Step: "backup"},
			},
			"/opt/crenein/.backups/ts",
			"sha256:new",
		),
	})

	tm := teatest.NewTestModel(t, uv, teatest.WithInitialTermSize(80, 24))

	// Wait for preview to load (manifest is synchronous in fake).
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		s := string(bts)
		return strings.Contains(s, "1.8.3") || strings.Contains(s, "1.8.4") || strings.Contains(s, "Loading")
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}

// ─── Test 8: Golden — preview screen (80×24) ─────────────────────────────────

// TestUpdateWizard_GoldenPreview captures a golden at 80x24 for the preview state.
func TestUpdateWizard_GoldenPreview(t *testing.T) {
	uv := newTestUpdateView(t, nil, nil)
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.4",
		releaseNotes:    "New dashboard features and security patches.",
		alreadyUpToDate: false,
	})
	golden.RequireEqual(t, []byte(uv.View()))
}

// ─── Test 9: Golden — already up-to-date screen ───────────────────────────────

func TestUpdateWizard_GoldenAlreadyUpToDate(t *testing.T) {
	uv := newTestUpdateView(t, nil, nil)
	uv = injectPreviewLoaded(t, uv, updatePreviewLoadedMsg{
		currentVersion:  "1.8.3",
		targetVersion:   "1.8.3",
		alreadyUpToDate: true,
	})
	golden.RequireEqual(t, []byte(uv.View()))
}

// ─── Test 10: Golden — success result screen ─────────────────────────────────

func TestUpdateWizard_GoldenSuccess(t *testing.T) {
	uv := newTestUpdateView(t, nil, nil)
	// Manually advance to result state with a successful result.
	uv.step = updateStepResult
	uv.targetVersion = "1.8.4"
	uv.execResult = &engine.UpdateResult{
		NewAgentImageID:      "sha256:new",
		PreviousAgentImageID: "sha256:old",
		BackupPath:           "/opt/crenein/.backups/20260615_120000",
		RolledBack:           false,
		RollbackFailed:       false,
	}
	uv.execDone = true
	golden.RequireEqual(t, []byte(uv.View()))
}

// ─── Test 11: Golden — rollback result screen ────────────────────────────────

func TestUpdateWizard_GoldenRollback(t *testing.T) {
	uv := newTestUpdateView(t, nil, nil)
	uv.step = updateStepResult
	uv.targetVersion = "1.8.4"
	uv.execResult = &engine.UpdateResult{
		PreviousAgentImageID: "sha256:old",
		BackupPath:           "/opt/crenein/.backups/20260615_120000",
		RolledBack:           true,
		RollbackFailed:       false,
	}
	uv.execDone = true
	golden.RequireEqual(t, []byte(uv.View()))
}
