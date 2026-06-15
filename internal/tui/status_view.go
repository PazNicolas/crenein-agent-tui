package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/status"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Refresh interval ─────────────────────────────────────────────────────────

const statusRefreshInterval = 5 * time.Second

// ─── Internal messages ────────────────────────────────────────────────────────

// statusLoadedMsg is sent when status.Collect finishes.
type statusLoadedMsg struct {
	doc        status.Doc
	allRunning bool
	warning    string
}

// updatesLoadedMsg is sent when status.FetchUpdatesInfo finishes.
type updatesLoadedMsg struct {
	info *status.UpdatesInfo
}

// statusRefreshTickMsg is sent by the periodic ticker.
type statusRefreshTickMsg struct{}

// ─── statusView ───────────────────────────────────────────────────────────────

// statusView is the home screen of the TUI: shows service table, versions, and
// update indicators. It implements tea.Model.
type statusView struct {
	// injected at construction
	cliVersion string
	profile    styles.Profile
	deps       status.Deps
	mc         release.Client // nil until resolved

	// layout
	width  int
	height int

	// loading state machine
	// phase: "init" → "loading" → "loaded" / "not-installed"
	phase string

	// not-installed: installDir resolved to ""
	notInstalled bool

	// installed state
	installDir string
	doc        status.Doc
	allRunning bool
	warning    string

	// updates sub-phase: "waiting" | "checking" | "done"
	updatesPhase string
	updates      *status.UpdatesInfo
}

// newStatusView constructs a statusView. mc may be nil (real production code
// resolves it from deps after building it with NewDepsReal).
func newStatusView(cliVersion string, profile styles.Profile, deps status.Deps, mc release.Client) statusView {
	return statusView{
		cliVersion:   cliVersion,
		profile:      profile,
		deps:         deps,
		mc:           mc,
		phase:        "init",
		updatesPhase: "waiting",
	}
}

// Init kicks off the initial load cycle.
func (v statusView) Init() tea.Cmd {
	return v.startLoadCmd()
}

// Update processes incoming messages.
func (v statusView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil

	case statusLoadedMsg:
		if msg.doc.InstallDir == "" && !v.notInstalled {
			// Collect was called but returned an empty installDir — treat as not installed.
			v.phase = "not-installed"
			v.notInstalled = true
			return v, v.scheduleRefreshTick()
		}
		v.phase = "loaded"
		v.doc = msg.doc
		v.allRunning = msg.allRunning
		v.warning = msg.warning
		v.installDir = msg.doc.InstallDir
		v.updatesPhase = "checking"
		return v, tea.Batch(v.loadUpdatesCmd(), v.scheduleRefreshTick())

	case updatesLoadedMsg:
		v.updatesPhase = "done"
		v.updates = msg.info
		return v, nil

	case statusRefreshTickMsg:
		// Re-run the two-phase load cycle.
		v.updatesPhase = "waiting"
		v.updates = nil
		return v, v.startLoadCmd()

	case tea.KeyMsg:
		if msg.String() == "r" {
			// Manual refresh: same as tick.
			v.updatesPhase = "waiting"
			v.updates = nil
			return v, v.startLoadCmd()
		}
	}
	return v, nil
}

// View renders the status view body.
func (v statusView) View() string {
	switch v.phase {
	case "init", "loading":
		return v.profile.ActiveViewStyle().Render("Loading status…")
	case "not-installed":
		return v.renderNotInstalled()
	case "loaded":
		return v.renderLoaded()
	default:
		return v.profile.ActiveViewStyle().Render("Loading status…")
	}
}

// ─── Load commands ────────────────────────────────────────────────────────────

// startLoadCmd resolves the install dir and, if found, fires loadStatusCmd.
func (v statusView) startLoadCmd() tea.Cmd {
	deps := v.deps
	cliVersion := v.cliVersion
	return func() tea.Msg {
		installDir := status.ResolveInstallDir(deps.ReadFile, deps.ReadDir, deps.InstallDir)
		if installDir == "" {
			// Return a statusLoadedMsg with an empty InstallDir so Update detects not-installed.
			return statusLoadedMsg{}
		}
		// Build deps without the manifest client so Collect returns quickly.
		fastDeps := deps
		fastDeps.ManifestClient = nil
		doc, allRunning, warning := status.Collect(context.Background(), fastDeps, cliVersion, installDir, io.Discard)
		return statusLoadedMsg{doc: doc, allRunning: allRunning, warning: warning}
	}
}

// loadUpdatesCmd fetches update info in the background (phase 2 of two-phase load).
func (v statusView) loadUpdatesCmd() tea.Cmd {
	mc := v.mc
	cliVersion := v.cliVersion
	agentVersion := v.doc.Agent.Version
	return func() tea.Msg {
		if mc == nil {
			// No manifest client available → version check unavailable.
			return updatesLoadedMsg{info: nil}
		}
		info := status.FetchUpdatesInfo(context.Background(), mc, cliVersion, agentVersion, io.Discard)
		return updatesLoadedMsg{info: info}
	}
}

// scheduleRefreshTick returns a Cmd that fires statusRefreshTickMsg after the
// refresh interval.
func (v statusView) scheduleRefreshTick() tea.Cmd {
	return tea.Tick(statusRefreshInterval, func(_ time.Time) tea.Msg {
		return statusRefreshTickMsg{}
	})
}

// ─── Renderers ────────────────────────────────────────────────────────────────

func (v statusView) renderNotInstalled() string {
	p := v.profile
	title := p.TitleStyle().Render("Agent not installed")
	msg := "No CRENEIN agent installation was found on this host."
	hint := "Press  i  to open the Install wizard and set it up."
	return p.ActiveViewStyle().Render(
		title + "\n\n" + msg + "\n" + hint,
	)
}

func (v statusView) renderLoaded() string {
	serviceTable := v.renderServiceTable()
	versionsPanel := v.renderVersionsPanel()

	// AD-5: side-by-side at ≥100, stacked below.
	if v.width >= 100 {
		return lipgloss.JoinHorizontal(lipgloss.Top, serviceTable, "  ", versionsPanel)
	}
	return serviceTable + "\n\n" + versionsPanel
}

// renderServiceTable builds the 5-row services table.
// Column layout at 80 cols (stacked / canonical):
//
//	SERVICE(10)  ST(6)  STATE(9)  VERSION/IMAGE(28)  UPTIME(8)
//
// "ST" is a fixed 6-char glyph column that holds "[OK]", "[WARN]", "[FAIL]"
// (mono) or "✅ ", "⚠️ ", "❌ " (color, padded to 6 bytes).
func (v statusView) renderServiceTable() string {
	p := v.profile

	const (
		colNameW    = 10
		colStateW   = 9
		colVersionW = 28
		colUptimeW  = 8
	)

	header := fmt.Sprintf("%-*s  %-6s  %-*s  %-*s  %-*s",
		colNameW, "SERVICE",
		"ST",
		colStateW, "STATE",
		colVersionW, "VERSION/IMAGE",
		colUptimeW, "UPTIME",
	)
	sep := strings.Repeat("-", colNameW+6+colStateW+colVersionW+colUptimeW+8)

	rows := []string{
		p.TitleStyle().Render("Services"),
		header,
		sep,
	}

	for _, svc := range v.doc.Services {
		glyph := stateGlyph(p, svc.State, svc.Health)
		state := svc.State
		if len(state) > colStateW {
			state = state[:colStateW]
		}
		img := svc.Image
		if len(img) > colVersionW {
			img = "…" + img[len(img)-(colVersionW-1):]
		}
		uptime := formatUptime(svc.UptimeSeconds)
		if svc.State != "running" {
			uptime = "-"
		}
		row := fmt.Sprintf("%-*s  %-6s  %-*s  %-*s  %-*s",
			colNameW, svc.Name,
			glyph,
			colStateW, state,
			colVersionW, img,
			colUptimeW, uptime,
		)
		rows = append(rows, row)
	}

	if v.warning != "" {
		rows = append(rows, "")
		rows = append(rows, p.FooterStyle().Render("warn: "+v.warning))
	}

	return p.ActiveViewStyle().Render(strings.Join(rows, "\n"))
}

// renderVersionsPanel renders CLI version, agent version, and update indicators.
func (v statusView) renderVersionsPanel() string {
	p := v.profile

	lines := []string{
		p.TitleStyle().Render("Versions & Updates"),
		"",
		fmt.Sprintf("CLI    : %s", v.cliVersion),
		fmt.Sprintf("Agent  : %s (%s)", v.doc.Agent.Version, v.doc.Agent.VersionSource),
		"",
		p.TitleStyle().Render("Update indicators"),
		"  CLI   : " + v.updateIndicator("cli"),
		"  Agent : " + v.updateIndicator("agent"),
	}

	if v.updatesPhase != "waiting" && v.updatesPhase != "checking" {
		lines = append(lines, "")
		lines = append(lines, "  Press u to open the Update wizard.")
	}

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

// updateIndicator returns the text for one update indicator.
func (v statusView) updateIndicator(component string) string {
	switch v.updatesPhase {
	case "waiting", "checking":
		return "checking…"
	}

	// updatesPhase == "done"
	if v.updates == nil {
		return "version check unavailable"
	}

	switch component {
	case "cli":
		if v.updates.CLILatest == "" {
			return "version check unavailable"
		}
		if v.updates.CLIUpdateAvailable {
			return fmt.Sprintf("update available → %s", v.updates.CLILatest)
		}
		return "up-to-date"

	case "agent":
		if v.updates.AgentLatest == "" {
			return "version check unavailable"
		}
		if v.updates.AgentUpdateAvailable {
			return fmt.Sprintf("update available → %s  (press u to update)", v.updates.AgentLatest)
		}
		return "up-to-date"
	}
	return "version check unavailable"
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// stateGlyph returns a profile-appropriate glyph for (state, health).
func stateGlyph(p styles.Profile, state, health string) string {
	if state == "running" && health != "unhealthy" {
		return p.Glyph(styles.GlyphOK)
	}
	if state == "running" && health == "unhealthy" {
		return p.Glyph(styles.GlyphWarn)
	}
	if state == "missing" || state == "exited" {
		return p.Glyph(styles.GlyphFail)
	}
	return p.Glyph(styles.GlyphWarn)
}

// formatUptime converts uptime seconds to a human-readable string.
func formatUptime(secs int64) string {
	if secs <= 0 {
		return "0s"
	}
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm", secs/60)
	}
	if secs < 86400 {
		return fmt.Sprintf("%dh", secs/3600)
	}
	return fmt.Sprintf("%dd", secs/86400)
}
