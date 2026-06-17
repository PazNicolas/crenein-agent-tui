// Package styles defines the visual profile and shared lipgloss styles for the
// crenein-agent TUI. Color decisions are injected via a Profile type — they
// are NOT read from the environment at style-call time — so test code can
// construct a deterministic mono or color profile without side effects.
package styles

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Profile controls whether the TUI renders in full ANSI color or plain mono.
type Profile int

const (
	// ProfileColor enables full ANSI color output.
	ProfileColor Profile = iota
	// ProfileMono disables all color; suitable for dumb terminals and tests.
	ProfileMono
)

// NewProfile creates a Profile from a boolean noColor flag.
func NewProfile(noColor bool) Profile {
	if noColor {
		return ProfileMono
	}
	return ProfileColor
}

// DetectProfile reads the environment to choose a profile:
//   - NO_COLOR set to any value → mono
//   - lipgloss (via termenv) detects ASCII/no-color terminal → mono
//   - otherwise → color
func DetectProfile() Profile {
	if os.Getenv("NO_COLOR") != "" {
		return ProfileMono
	}
	if lipgloss.ColorProfile() == termenv.Ascii {
		return ProfileMono
	}
	return ProfileColor
}

// GlyphKind identifies which status glyph to render.
type GlyphKind int

const (
	GlyphOK   GlyphKind = iota
	GlyphWarn GlyphKind = iota
	GlyphFail GlyphKind = iota
)

// Glyph returns the appropriate status glyph for the profile.
// Color profile uses Unicode emoji; mono profile uses ASCII text fallbacks.
func (p Profile) Glyph(kind GlyphKind) string {
	if p == ProfileColor {
		switch kind {
		case GlyphOK:
			return "✅"
		case GlyphWarn:
			return "⚠️"
		case GlyphFail:
			return "❌"
		}
	}
	// Mono fallbacks.
	switch kind {
	case GlyphOK:
		return "[OK]"
	case GlyphWarn:
		return "[WARN]"
	case GlyphFail:
		return "[FAIL]"
	}
	return ""
}

// HeaderStyle returns a lipgloss style for the TUI header line.
// Color profile: bold white on dark blue background.
// Mono profile: plain, no colors.
func (p Profile) HeaderStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	if p == ProfileColor {
		s = s.Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#1A1A2E"))
	}
	return s
}

// FooterStyle returns a lipgloss style for the TUI footer/key-hints line.
// Color profile: faint foreground.
// Mono profile: plain.
func (p Profile) FooterStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Padding(0, 1)
	if p == ProfileColor {
		s = s.Foreground(lipgloss.Color("#888888"))
	}
	return s
}

// ActiveViewStyle returns a lipgloss style for the main content area.
func (p Profile) ActiveViewStyle() lipgloss.Style {
	return lipgloss.NewStyle().Padding(0, 1)
}

// TitleStyle returns a lipgloss style for section titles inside views.
// Color profile: bold cyan.
// Mono profile: bold only.
func (p Profile) TitleStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	if p == ProfileColor {
		s = s.Foreground(lipgloss.Color("#00BFFF"))
	}
	return s
}

// logoWordmark is the CRENEIN wordmark rendered with a compact pure-ASCII figlet
// font ("cybermedium"): three rows, so it fits smaller terminals and renders
// cleanly everywhere (color, mono, NO_COLOR, dumb terminals). The brand color is
// applied by LogoStyle, not baked into the glyphs.
const logoWordmark = `____ ____ ____ _  _ ____ _ _  _
|    |__/ |___ |\ | |___ | |\ |
|___ |  \ |___ | \| |___ | | \|`

// LogoLines returns the CRENEIN wordmark banner as individual lines.
func (p Profile) LogoLines() []string {
	return strings.Split(logoWordmark, "\n")
}

// LogoTagline returns the brand tagline shown beneath the wordmark.
func (p Profile) LogoTagline() string {
	return "your network, your business, our mission"
}

// LogoStyle returns the lipgloss style for the wordmark banner.
// Color profile: bold cyan brand accent; mono profile: plain (bold only).
func (p Profile) LogoStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Bold(true)
	if p == ProfileColor {
		s = s.Foreground(lipgloss.Color("#00BFFF"))
	}
	return s
}
