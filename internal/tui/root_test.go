package tui

import (
	"strings"
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
	// Should contain the "too small" notice with have/need dimensions.
	if !strings.Contains(out, "Terminal too small") {
		t.Errorf("View() missing 'Terminal too small': %q", out)
	}
	if !strings.Contains(out, "60x20") {
		t.Errorf("View() missing current dimensions '60x20': %q", out)
	}
	if !strings.Contains(out, "80x24") {
		t.Errorf("View() missing required dimensions '80x24': %q", out)
	}
}

func TestRootModelViewTooSmallRecovery(t *testing.T) {
	// Start with an undersized terminal.
	m := newTestModel()
	m.width = 60
	m.height = 20
	out := m.View()
	if !strings.Contains(out, "Terminal too small") {
		t.Fatalf("expected too-small message, got: %q", out)
	}

	// Resize to valid dimensions — previous view (Status) should be restored.
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm := m2.(Model)
	if mm.width != 80 || mm.height != 24 {
		t.Errorf("width/height not updated after resize: %dx%d", mm.width, mm.height)
	}
	out2 := mm.View()
	if strings.Contains(out2, "Terminal too small") {
		t.Errorf("still showing too-small message after resize to 80x24: %q", out2)
	}
	// Active view should still be Status.
	if mm.activeView() != ViewStatus {
		t.Errorf("after resize, activeView = %v, want ViewStatus", mm.activeView())
	}
}

func TestRootModelLogoResponsive(t *testing.T) {
	// The wordmark tagline is identical across profiles, so it's a stable probe
	// for whether the banner is rendered.
	const taglineFragment = "your network, your business"

	m := newTestModel()
	m.width = 100

	// Just below the threshold → banner hidden so the view keeps its room.
	m.height = logoMinHeight - 1
	if out := m.View(); strings.Contains(out, taglineFragment) {
		t.Errorf("at height %d the logo banner should be hidden, but it was shown", m.height)
	}

	// At the threshold → banner visible.
	m.height = logoMinHeight
	if out := m.View(); !strings.Contains(out, taglineFragment) {
		t.Errorf("at height %d the logo banner should be visible, but it was not", m.height)
	}
}

func TestRootModelViewMonoProfile(t *testing.T) {
	// 7.3: NO_COLOR / mono profile — verify no ANSI color sequences in output
	// and that status glyphs use text fallbacks.
	m := newTestModel() // newTestModel already uses mono profile
	// Navigate through all views and verify mono rendering.
	views := []ViewID{ViewStatus, ViewInstall, ViewUpdate, ViewDoctor, ViewLogs}
	viewKeys := []string{"s", "i", "u", "d", "l"}
	for i, key := range viewKeys {
		m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		mm := m2.(Model)
		out := mm.View()

		// Should not contain ANSI escape sequences (color codes).
		if strings.Contains(out, "\x1b[") {
			t.Errorf("view %v: output contains ANSI escape sequences in mono mode:\n%s",
				views[i], out)
		}
		// Should not contain raw color emoji glyphs (✅ ⚠️ ❌) in mono mode.
		for _, forbidden := range []string{"✅", "⚠️", "❌"} {
			if strings.Contains(out, forbidden) {
				t.Errorf("view %v: output contains color emoji %q in mono mode", views[i], forbidden)
			}
		}
	}
}
