package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/status"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
)

// ─── Fake docker-compose content ─────────────────────────────────────────────

const minimalDockerCompose = `version: "3"
services:
  agent:
    image: crenein/c-network-agent-back:1.8.3
  frontend:
    image: crenein/c-network-frontend:1.8.3
  mongodb:
    image: mongo:4.4
  influxdb:
    image: influxdb:2.7
  redis:
    image: redis:7
`

// ─── Fake file-system functions ───────────────────────────────────────────────

func fakeReadFile(name string) ([]byte, error) {
	if name == "/testinstall/docker-compose.yml" {
		return []byte(minimalDockerCompose), nil
	}
	return nil, errNotFound
}

func fakeReadDir(_ string) ([]string, error) { return nil, errNotFound }

func notInstalledReadFile(_ string) ([]byte, error) { return nil, errNotFound }

var errNotFound = &notFoundErr{}

type notFoundErr struct{}

func (e *notFoundErr) Error() string { return "not found" }

// ─── Fake compose / detect fns ───────────────────────────────────────────────

func fakeComposePs(_ context.Context, _ string, services []string) ([]dockerx.ContainerState, error) {
	result := make([]dockerx.ContainerState, 0, len(services))
	for _, svc := range services {
		result = append(result, dockerx.ContainerState{
			Service: svc,
			Running: true,
			Status:  "Up 2 hours",
		})
	}
	return result, nil
}

func fakeDetectAgentVersion(_ context.Context) (string, string) { return "1.8.3", "health" }

// fixedNow provides a deterministic timestamp for all test output.
var fixedNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

// ─── Deps factories ───────────────────────────────────────────────────────────

func makeInstalledDeps() status.Deps {
	return status.Deps{
		ComposePs:          fakeComposePs,
		DetectAgentVersion: fakeDetectAgentVersion,
		ReadFile:           fakeReadFile,
		ReadDir:            fakeReadDir,
		InstallDir:         "/testinstall",
		Now:                func() time.Time { return fixedNow },
		ManifestClient:     nil,
	}
}

func makeNotInstalledDeps() status.Deps {
	return status.Deps{
		ComposePs:          nil,
		DetectAgentVersion: nil,
		ReadFile:           notInstalledReadFile,
		ReadDir:            fakeReadDir,
		InstallDir:         "",
		Now:                func() time.Time { return fixedNow },
		ManifestClient:     nil,
	}
}

// ─── Fake release.Client ──────────────────────────────────────────────────────

type fakeManifestClient struct {
	manifest *release.Manifest
	fetchErr *cnerr.Error
}

func (f *fakeManifestClient) FetchManifest(_ context.Context, _ bool) (*release.Manifest, *cnerr.Error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.manifest, nil
}

func (f *fakeManifestClient) DetectAgentVersion(_ context.Context) string { return "1.8.3" }

func makeManifestUpToDate() *fakeManifestClient {
	return &fakeManifestClient{manifest: &release.Manifest{
		Agent: release.AgentSection{
			Latest: "1.8.3",
			Releases: map[string]release.AgentRelease{
				"1.8.3": {Image: "crenein/c-network-agent-back:1.8.3", Mongo: map[string]string{"7": "mongo:7", "4": "mongo:4.4"}},
			},
		},
		CLI: release.CLISection{
			Latest:   "0.1.0",
			Releases: map[string]release.CLIRelease{"0.1.0": {}},
		},
		FetchedAt: fixedNow,
	}}
}

// ─── Model-driving helpers ────────────────────────────────────────────────────

// buildInstalledDoc calls status.Collect with fake deps to build a realistic Doc.
func buildInstalledDoc(t *testing.T) (status.Doc, bool, string) {
	t.Helper()
	deps := makeInstalledDeps()
	doc, allRunning, warning := status.Collect(
		context.Background(), deps, "v0.1.0", "/testinstall", io.Discard,
	)
	return doc, allRunning, warning
}

// fixedLastChecked is a deterministic "last checked" timestamp used by the
// golden fixtures so the rendered timestamp stays stable across runs.
const fixedLastChecked = "2026-06-17T12:00:00Z"

// upToDateUpdatesInfo returns UpdatesInfo where both components are current.
func upToDateUpdatesInfo() *status.UpdatesInfo {
	lastChecked := fixedLastChecked
	return &status.UpdatesInfo{
		CLIVersion: "v0.1.0", CLILatest: "0.1.0", CLIUpdateAvailable: false,
		AgentVersion: "1.8.3", AgentLatest: "1.8.3", AgentUpdateAvailable: false,
		LastChecked: &lastChecked,
	}
}

// updateAvailableInfo returns UpdatesInfo where agent 1.8.4 is available.
func updateAvailableInfo() *status.UpdatesInfo {
	lastChecked := fixedLastChecked
	return &status.UpdatesInfo{
		CLIVersion: "v0.1.0", CLILatest: "0.1.0", CLIUpdateAvailable: false,
		AgentVersion: "1.8.3", AgentLatest: "1.8.4", AgentUpdateAvailable: true,
		LastChecked: &lastChecked,
	}
}

// driveToLoaded drives a statusView through the two-phase load by injecting
// messages directly (no async cmds involved).
func driveToLoaded(t *testing.T, sv statusView, updatesInfo *status.UpdatesInfo) statusView {
	t.Helper()
	doc, allRunning, warning := buildInstalledDoc(t)
	m2, _ := sv.Update(statusLoadedMsg{doc: doc, allRunning: allRunning, warning: warning})
	m3, _ := m2.(statusView).Update(updatesLoadedMsg{info: updatesInfo})
	return m3.(statusView)
}

// driveToNotInstalled drives a statusView to the not-installed state.
func driveToNotInstalled(sv statusView) statusView {
	// Empty statusLoadedMsg triggers not-installed.
	m2, _ := sv.Update(statusLoadedMsg{})
	return m2.(statusView)
}

// renderAt sets width/height on the view and calls View().
func renderAt(sv statusView, w, h int) string {
	sv.width = w
	sv.height = h
	return sv.View()
}

// ─── Golden tests (static, no async runtime) ─────────────────────────────────
//
// We drive the statusView model directly via Update calls rather than through
// the bubbletea async runtime. This avoids races between injected messages and
// Init commands, gives deterministic output, and keeps golden files stable.
// Goldens are captured at 80x24 (canonical narrow layout, mono profile).

// TestStatusView_GoldenInstalled is the primary golden: installed stack, all
// services running, up-to-date indicators.
func TestStatusView_GoldenInstalled(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeInstalledDeps(), nil)
	loaded := driveToLoaded(t, sv, upToDateUpdatesInfo())
	out := renderAt(loaded, 80, 24)
	golden.RequireEqual(t, []byte(out))
}

// TestStatusView_GoldenNotInstalled is the golden for the not-installed state.
func TestStatusView_GoldenNotInstalled(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeNotInstalledDeps(), nil)
	notInstalled := driveToNotInstalled(sv)
	out := renderAt(notInstalled, 80, 24)
	golden.RequireEqual(t, []byte(out))
}

// TestStatusView_GoldenUpdateAvailable is the golden for the update-available indicator.
func TestStatusView_GoldenUpdateAvailable(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeInstalledDeps(), nil)
	loaded := driveToLoaded(t, sv, updateAvailableInfo())
	out := renderAt(loaded, 80, 24)
	golden.RequireEqual(t, []byte(out))
}

// TestStatusView_GoldenVersionCheckUnavailable is the golden for the manifest-fetch-fail case.
func TestStatusView_GoldenVersionCheckUnavailable(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeInstalledDeps(), nil)
	loaded := driveToLoaded(t, sv, nil) // nil info = version check unavailable
	out := renderAt(loaded, 80, 24)
	golden.RequireEqual(t, []byte(out))
}

// ─── Teatest-based integration tests (async, with real Init cycle) ────────────
//
// These tests use teatest but wait for specific content without capturing goldens,
// to verify the async loading cycle works end-to-end.

// TestStatusView_AsyncInstalled verifies the full async two-phase load cycle:
// Init fires, services appear, then update indicators resolve.
func TestStatusView_AsyncInstalled(t *testing.T) {
	// Use a manifest client that produces up-to-date results.
	deps := makeInstalledDeps()
	deps.ManifestClient = makeManifestUpToDate()
	// Also set mc on the view (the mc field).
	sv := newStatusView("v0.1.0", styles.NewProfile(true), deps, makeManifestUpToDate())
	tm := teatest.NewTestModel(t, sv, teatest.WithInitialTermSize(80, 24))

	// Wait for the services table to appear (phase 1 done).
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "agent")
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}

// TestStatusView_AsyncNotInstalled verifies the not-installed async path.
func TestStatusView_AsyncNotInstalled(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeNotInstalledDeps(), nil)
	tm := teatest.NewTestModel(t, sv, teatest.WithInitialTermSize(80, 24))

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "not installed")
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}

// ─── Behavioral unit tests (model-only, no async runtime) ────────────────────

// TestStatusView_ManualRefresh verifies that pressing 'r' resets update
// indicators and issues a reload cmd.
func TestStatusView_ManualRefresh(t *testing.T) {
	deps := makeInstalledDeps()
	sv := newStatusView("v0.1.0", styles.NewProfile(true), deps, makeManifestUpToDate())
	loaded := driveToLoaded(t, sv, upToDateUpdatesInfo())
	if loaded.phase != "loaded" {
		t.Fatalf("phase = %q, want 'loaded'", loaded.phase)
	}

	m2, cmd := loaded.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	refreshed := m2.(statusView)

	if refreshed.updatesPhase != "waiting" {
		t.Errorf("updatesPhase after 'r' = %q, want 'waiting'", refreshed.updatesPhase)
	}
	if cmd == nil {
		t.Error("cmd after 'r' must be non-nil (reload cycle)")
	}
}

// TestStatusView_AllMissingServices verifies [FAIL] glyphs when all containers absent.
func TestStatusView_AllMissingServices(t *testing.T) {
	deps := makeInstalledDeps()
	deps.ComposePs = func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
		return []dockerx.ContainerState{}, nil
	}
	sv := newStatusView("v0.1.0", styles.NewProfile(true), deps, nil)

	doc, allRunning, warning := status.Collect(
		context.Background(), deps, "v0.1.0", "/testinstall", io.Discard,
	)
	m2, _ := sv.Update(statusLoadedMsg{doc: doc, allRunning: allRunning, warning: warning})
	m3, _ := m2.(statusView).Update(updatesLoadedMsg{info: nil})
	loaded := m3.(statusView)

	view := renderAt(loaded, 80, 24)
	failCount := strings.Count(view, "[FAIL]")
	if failCount < 5 {
		t.Errorf("expected 5+ [FAIL] glyphs for missing services, got %d\n%s", failCount, view)
	}
}

// TestStatusView_IndicatorStates table-driven test for update indicator text.
func TestStatusView_IndicatorStates(t *testing.T) {
	tests := []struct {
		name        string
		updatesInfo *status.UpdatesInfo
		wantText    string
	}{
		{"up-to-date", upToDateUpdatesInfo(), "up-to-date"},
		{"update-available", updateAvailableInfo(), "update available"},
		{"unavailable-nil", nil, "version check unavailable"},
		{
			"unavailable-empty-latest",
			&status.UpdatesInfo{AgentLatest: "", CLILatest: ""},
			"version check unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := makeInstalledDeps()
			sv := newStatusView("v0.1.0", styles.NewProfile(true), deps, nil)
			loaded := driveToLoaded(t, sv, tt.updatesInfo)
			view := renderAt(loaded, 80, 24)
			if !strings.Contains(view, tt.wantText) {
				t.Errorf("expected %q in view:\n%s", tt.wantText, view)
			}
		})
	}
}

// TestStatusView_LayoutBreakpoint verifies narrow vs wide layout rendering.
func TestStatusView_LayoutBreakpoint(t *testing.T) {
	deps := makeInstalledDeps()
	sv := newStatusView("v0.1.0", styles.NewProfile(true), deps, makeManifestUpToDate())
	loaded := driveToLoaded(t, sv, upToDateUpdatesInfo())

	narrow := renderAt(loaded, 80, 24)
	if !strings.Contains(narrow, "Services") {
		t.Error("narrow: missing 'Services' section")
	}
	if !strings.Contains(narrow, "Versions") {
		t.Error("narrow: missing 'Versions & Updates' section")
	}

	wide := renderAt(loaded, 110, 40)
	if !strings.Contains(wide, "Services") {
		t.Error("wide: missing 'Services' section")
	}
	if !strings.Contains(wide, "Versions") {
		t.Error("wide: missing 'Versions & Updates' section")
	}

	t.Logf("narrow (80 cols):\n%s", narrow)
	t.Logf("wide (110 cols):\n%s", wide)
}

// TestStatusView_CheckingState verifies that "checking…" appears before
// updatesLoadedMsg arrives.
func TestStatusView_CheckingState(t *testing.T) {
	sv := newStatusView("v0.1.0", styles.NewProfile(true), makeInstalledDeps(), nil)

	doc, allRunning, warning := buildInstalledDoc(t)
	m2, _ := sv.Update(statusLoadedMsg{doc: doc, allRunning: allRunning, warning: warning})
	checking := m2.(statusView) // updatesPhase == "checking" at this point

	view := renderAt(checking, 80, 24)
	if !strings.Contains(view, "checking") {
		t.Errorf("expected 'checking' indicator before updates resolve:\n%s", view)
	}
}
