package tui

import (
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel() Model {
	// mono profile → deterministic, no terminal detection
	m := NewModel("v0.1.0", styles.NewProfile(true))
	// Give it a valid terminal size so View() doesn't short-circuit.
	m.width = 120
	m.height = 40
	return m
}

func activeViewID(m tea.Model) ViewID {
	return m.(Model).activeView()
}

func TestRootModelNavigation(t *testing.T) {
	m := newTestModel()

	t.Run("initial view is status", func(t *testing.T) {
		if got := m.activeView(); got != ViewStatus {
			t.Errorf("initial activeView = %v, want ViewStatus", got)
		}
	})

	t.Run("press d navigates to doctor", func(t *testing.T) {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
		if got := activeViewID(m2); got != ViewDoctor {
			t.Errorf("after 'd', activeView = %v, want ViewDoctor", got)
		}
	})

	t.Run("press i navigates to install", func(t *testing.T) {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
		if got := activeViewID(m2); got != ViewInstall {
			t.Errorf("after 'i', activeView = %v, want ViewInstall", got)
		}
	})

	t.Run("press u navigates to update", func(t *testing.T) {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
		if got := activeViewID(m2); got != ViewUpdate {
			t.Errorf("after 'u', activeView = %v, want ViewUpdate", got)
		}
	})

	t.Run("press l navigates to logs", func(t *testing.T) {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
		if got := activeViewID(m2); got != ViewLogs {
			t.Errorf("after 'l', activeView = %v, want ViewLogs", got)
		}
	})

	t.Run("press esc returns to status", func(t *testing.T) {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
		m3, _ := m2.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
		if got := activeViewID(m3); got != ViewStatus {
			t.Errorf("after 'd' then esc, activeView = %v, want ViewStatus", got)
		}
	})

	t.Run("esc on root stack stays at status", func(t *testing.T) {
		// Stack already at ViewStatus with length 1 — esc should not panic.
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if got := activeViewID(m2); got != ViewStatus {
			t.Errorf("esc on root: activeView = %v, want ViewStatus", got)
		}
	})

	t.Run("navigation blocked when opRunning", func(t *testing.T) {
		blocked := newTestModel()
		blocked.opRunning = true
		m2, _ := blocked.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
		if got := activeViewID(m2); got != ViewStatus {
			t.Errorf("opRunning: after 'd', activeView = %v, want ViewStatus", got)
		}
	})

	t.Run("NavigateToMsg pushes view", func(t *testing.T) {
		m2, _ := m.Update(NavigateToMsg{View: ViewUpdate})
		if got := activeViewID(m2); got != ViewUpdate {
			t.Errorf("NavigateToMsg: activeView = %v, want ViewUpdate", got)
		}
	})

	t.Run("NavigateBackMsg pops view", func(t *testing.T) {
		m2, _ := m.Update(NavigateToMsg{View: ViewDoctor})
		m3, _ := m2.(Model).Update(NavigateBackMsg{})
		if got := activeViewID(m3); got != ViewStatus {
			t.Errorf("NavigateBackMsg: activeView = %v, want ViewStatus", got)
		}
	})
}

func TestRootModelQuitConfirm(t *testing.T) {
	t.Run("q while opRunning sets quitting", func(t *testing.T) {
		m := newTestModel()
		m.opRunning = true
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
		if !m2.(Model).quitting {
			t.Error("expected quitting=true after 'q' with opRunning")
		}
	})

	t.Run("n cancels quit confirmation", func(t *testing.T) {
		m := newTestModel()
		m.opRunning = true
		m.quitting = true
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		if m2.(Model).quitting {
			t.Error("expected quitting=false after 'n'")
		}
	})
}

func TestRootModelWindowSize(t *testing.T) {
	m := newTestModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	mm := m2.(Model)
	if mm.width != 200 || mm.height != 50 {
		t.Errorf("width/height not updated: got %dx%d", mm.width, mm.height)
	}
}

func TestRootModelViewTooSmall(t *testing.T) {
	m := newTestModel()
	m.width = 60
	m.height = 20
	out := m.View()
	if out == "" {
		t.Fatal("View() returned empty string for small terminal")
	}
	// Should contain the "too small" notice.
	if len(out) < 10 {
		t.Errorf("View() for small terminal too short: %q", out)
	}
}
