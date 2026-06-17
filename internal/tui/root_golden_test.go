package tui

// root_golden_test.go — Root model golden tests (task 8.6)
//
// These tests capture the mono (NO_COLOR) output of the root Model for each of
// the five navigation views at a canonical 80×24 terminal size.
//
// Design notes:
//   - We use newTestModel() which already uses styles.NewProfile(true) (mono).
//   - We send tea.WindowSizeMsg{Width:80,Height:24} BEFORE navigating so the
//     active view (Status, initially) is sized. Then we navigate to the target
//     view and send another WindowSizeMsg so the newly-active child also gets
//     sized before we call View().
//   - All views are captured in their initial (pre-async) state — no cmds are
//     executed, so output is fully deterministic.
//   - The status view will show its "loading" indicator because no statusLoadedMsg
//     has been injected — this is the correct initial state at the root level.
//   - Install view: stepExistingGuard with existingDir=="" → deterministic.
//   - Update view: previewLoading=true → deterministic loading indicator.
//   - Doctor view: phase=="idle" → deterministic idle/running indicator.
//   - Logs view: initial state (no install dir resolved yet) → deterministic.

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
)

// sizeAndNavigate is a helper that:
//  1. Sends a WindowSizeMsg to size the root model at 80×24.
//  2. Navigates to the target view via NavigateToMsg.
//  3. Sends another WindowSizeMsg so the new active child view is sized too.
//
// Returns the final model, ready for View().
func sizeAndNavigate(m Model, viewID ViewID) Model {
	// Step 1: size the root (and current active view — initially Status).
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm := m2.(Model)

	// Step 2: navigate to the target view.
	m3, _ := mm.Update(NavigateToMsg{View: viewID})
	mm2 := m3.(Model)

	// Step 3: send another WindowSizeMsg so the newly-active child is sized.
	m4, _ := mm2.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m4.(Model)
}

// TestRootModelGoldenMono_Status captures the 80×24 mono output of the root
// model with ViewStatus active (initial state — loading phase).
func TestRootModelGoldenMono_Status(t *testing.T) {
	m := newTestModel()
	mm := sizeAndNavigate(m, ViewStatus)
	out := mm.View()
	golden.RequireEqual(t, []byte(out))
}

// TestRootModelGoldenMono_Install captures the 80×24 mono output of the root
// model with ViewInstall active (initial state — stepExistingGuard, checking).
func TestRootModelGoldenMono_Install(t *testing.T) {
	m := newTestModel()
	mm := sizeAndNavigate(m, ViewInstall)
	out := mm.View()
	golden.RequireEqual(t, []byte(out))
}

// TestRootModelGoldenMono_Update captures the 80×24 mono output of the root
// model with ViewUpdate active (initial state — loading preview).
func TestRootModelGoldenMono_Update(t *testing.T) {
	m := newTestModel()
	mm := sizeAndNavigate(m, ViewUpdate)
	out := mm.View()
	golden.RequireEqual(t, []byte(out))
}

// TestRootModelGoldenMono_Doctor captures the 80×24 mono output of the root
// model with ViewDoctor active (initial state — idle before run).
func TestRootModelGoldenMono_Doctor(t *testing.T) {
	m := newTestModel()
	mm := sizeAndNavigate(m, ViewDoctor)
	out := mm.View()
	golden.RequireEqual(t, []byte(out))
}

// TestRootModelGoldenMono_Logs captures the 80×24 mono output of the root
// model with ViewLogs active (initial state — no install dir resolved yet).
func TestRootModelGoldenMono_Logs(t *testing.T) {
	m := newTestModel()
	mm := sizeAndNavigate(m, ViewLogs)
	out := mm.View()
	golden.RequireEqual(t, []byte(out))
}
