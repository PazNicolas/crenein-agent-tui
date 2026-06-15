package tui

// doctor_view.go — Doctor view (Lote 4b: tasks 5.1–5.3)
//
// Design note on "pending → running → result" rendering:
//
//	engine.Run (doctor) is ATOMIC — it runs all checks and returns a complete
//	DoctorReport in one call. It does NOT emit per-check events through a
//	Reporter channel (cmd/doctor.go drives it with engine.DiscardReporter{}).
//	Therefore the "live per-check" progress described in the spec ("pending →
//	running → result as its event arrives") is not achievable without modifying
//	the engine, which is a Non-Goal of this change.
//
//	Justified divergence: we show a single collective "running diagnostics…"
//	spinner while the atomic Cmd executes, then render the full checklist with
//	final status when doctorFinishedMsg arrives.  The spec's intent — that
//	progress is visible and the UI stays responsive — is fully satisfied.
//
// Layout (AD-5):
//
//	Width ≥ 100 → checklist on the left, detail pane on the right (side-by-side).
//	Width  < 100 → checklist on top, detail pane below.
//
//	bubbles/viewport is NOT used for the detail pane here because, at 80x24 with
//	the header/footer occupying 2 lines, the detail text is short and wraps fine
//	in a plain lipgloss block.  A viewport could be added in a follow-up if
//	long FixSuggestion text requires it.

import (
	"context"
	"fmt"
	"strings"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── messages ────────────────────────────────────────────────────────────────

// doctorFinishedMsg is returned by the Cmd goroutine that runs engine.Run.
type doctorFinishedMsg struct {
	report engine.DoctorReport
	err    error
}

// ─── doctorViewDeps — injection seam ─────────────────────────────────────────

// doctorViewDeps holds injectable dependencies for the Doctor view.
// The doctorFn seam allows tests to supply a fake doctor without touching the
// engine or any real Docker daemon.
type doctorViewDeps struct {
	// doctorFn calls the real or fake engine.Run.
	// Default (nil) is resolved to engine.Run at runtime.
	doctorFn func(ctx context.Context, deps engine.Deps, opts engine.DoctorOptions) engine.DoctorReport

	// engineDeps are the real engine.Deps wired for production.
	// Tests may leave this zero-value; doctorFn will receive a zero Deps.
	engineDeps engine.Deps
}

// ─── doctorView ──────────────────────────────────────────────────────────────

// doctorView is the Doctor screen Bubbletea sub-model.
// It replaces the stub doctorView{} defined in views.go.
type doctorView struct {
	profile styles.Profile
	deps    doctorViewDeps

	// layout
	width  int
	height int

	// phase: "idle" → "running" → "done" | "error"
	phase string

	// spinner tick counter (shared spinnerTickMsg / baseSpinnerTickCmd pattern).
	spinnerTick int

	// result state (phase == "done")
	report engine.DoctorReport

	// error state (phase == "error")
	runErr error

	// selection: index into report.Checks (phase == "done")
	selected int
}

// newDoctorView constructs a doctorView with injectable deps.
func newDoctorView(profile styles.Profile, deps doctorViewDeps) *doctorView {
	return &doctorView{
		profile: profile,
		deps:    deps,
		phase:   "idle",
	}
}

// ─── tea.Model implementation ─────────────────────────────────────────────────

// Init kicks off the initial doctor run as soon as the view becomes active.
func (v *doctorView) Init() tea.Cmd {
	return v.startRunCmd()
}

// Update handles all messages for the Doctor view.
func (v *doctorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil

	// Spinner animation while running.
	case spinnerTickMsg:
		if v.phase == "running" {
			v.spinnerTick++
			return v, baseSpinnerTickCmd()
		}
		return v, nil

	// Doctor run completed (success or error).
	case doctorFinishedMsg:
		if msg.err != nil {
			v.phase = "error"
			v.runErr = msg.err
		} else {
			v.phase = "done"
			v.report = msg.report
			v.selected = 0
		}
		return v, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "r":
			// Re-run: reset state and start a fresh run.
			return v.rerun()
		}

		if v.phase == "done" {
			n := len(v.report.Checks)
			switch msg.String() {
			case "up", "k":
				if v.selected > 0 {
					v.selected--
				}
			case "down", "j":
				if v.selected < n-1 {
					v.selected++
				}
			}
		}
	}

	return v, nil
}

// View renders the doctor view body.
func (v *doctorView) View() string {
	switch v.phase {
	case "idle":
		return v.profile.ActiveViewStyle().Render("Doctor — initialising…")
	case "running":
		return v.renderRunning()
	case "done":
		return v.renderDone()
	case "error":
		return v.renderError()
	default:
		return v.profile.ActiveViewStyle().Render("Doctor")
	}
}

// ─── commands ─────────────────────────────────────────────────────────────────

// startRunCmd transitions to "running" and dispatches the atomic doctor Cmd.
func (v *doctorView) startRunCmd() tea.Cmd {
	v.phase = "running"
	v.spinnerTick = 0
	v.runErr = nil
	v.report = engine.DoctorReport{}
	v.selected = 0

	doctorFn := v.deps.doctorFn
	if doctorFn == nil {
		doctorFn = engine.Run
	}
	engineDeps := v.deps.engineDeps

	return tea.Batch(
		baseSpinnerTickCmd(),
		func() tea.Msg {
			ctx := context.Background()
			report := doctorFn(ctx, engineDeps, engine.DoctorOptions{})
			return doctorFinishedMsg{report: report}
		},
	)
}

// rerun resets all state and starts a new run.
func (v *doctorView) rerun() (*doctorView, tea.Cmd) {
	v.phase = "idle"
	v.selected = 0
	v.report = engine.DoctorReport{}
	v.runErr = nil
	return v, v.startRunCmd()
}

// ─── renderers ────────────────────────────────────────────────────────────────

func (v *doctorView) renderRunning() string {
	p := v.profile
	spinner := []string{"|", "/", "-", "\\"}[v.spinnerTick%4]
	title := p.TitleStyle().Render("Doctor — Running diagnostics…")
	return p.ActiveViewStyle().Render(title + "\n\n  " + spinner + "  Running all checks, please wait…")
}

func (v *doctorView) renderDone() string {
	p := v.profile
	checks := v.report.Checks

	// Build checklist lines.
	checklistLines := make([]string, 0, len(checks)+3)
	checklistLines = append(checklistLines, p.TitleStyle().Render("Doctor — Diagnostics"))
	checklistLines = append(checklistLines, "")

	for i, c := range checks {
		glyph := checkGlyph(p, c.Status)
		cursor := "  "
		if i == v.selected {
			cursor = "▶ "
		}
		checklistLines = append(checklistLines, fmt.Sprintf("%s%s  %s", cursor, glyph, c.Name))
	}

	// Summary line.
	checklistLines = append(checklistLines, "")
	checklistLines = append(checklistLines, summaryLine(p, v.report))

	// Key hints.
	checklistLines = append(checklistLines, "")
	checklistLines = append(checklistLines, p.FooterStyle().Render("↑/↓ or j/k: select  r: re-run  esc: back"))

	checklist := strings.Join(checklistLines, "\n")

	// Detail pane for selected check.
	detail := v.renderDetail()

	// AD-5: side-by-side at width ≥ 100, stacked below.
	if v.width >= 100 {
		leftStyle := lipgloss.NewStyle().Width(v.width/2-2).Padding(0, 1)
		rightStyle := lipgloss.NewStyle().Width(v.width/2-2).Padding(0, 1)
		return lipgloss.JoinHorizontal(lipgloss.Top,
			leftStyle.Render(checklist),
			rightStyle.Render(detail),
		)
	}

	// Narrow: checklist + detail stacked.
	return p.ActiveViewStyle().Render(checklist + "\n\n" + detail)
}

func (v *doctorView) renderDetail() string {
	p := v.profile
	checks := v.report.Checks
	if len(checks) == 0 {
		return ""
	}
	if v.selected < 0 || v.selected >= len(checks) {
		return ""
	}

	c := checks[v.selected]

	lines := []string{
		p.TitleStyle().Render("Detail: " + c.Name),
		"",
		fmt.Sprintf("Status  : %s %s", checkGlyph(p, c.Status), string(c.Status)),
		fmt.Sprintf("Severity: %s", string(c.Severity)),
		"",
	}

	if c.Detail != "" {
		lines = append(lines, "Output:")
		for _, l := range strings.Split(c.Detail, "\n") {
			lines = append(lines, "  "+l)
		}
		lines = append(lines, "")
	}

	if c.FixSuggestion != "" {
		lines = append(lines, "Fix:")
		for _, l := range strings.Split(c.FixSuggestion, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	return strings.Join(lines, "\n")
}

func (v *doctorView) renderError() string {
	p := v.profile
	lines := []string{
		p.TitleStyle().Render("Doctor — Error"),
		"",
		p.Glyph(styles.GlyphFail) + "  Failed to run diagnostics.",
		"",
	}
	if v.runErr != nil {
		lines = append(lines, "  Error: "+v.runErr.Error())
		lines = append(lines, "")
	}
	lines = append(lines, "  Suggestion: run  crenein-agent doctor  headless for raw output.")
	lines = append(lines, "")
	lines = append(lines, p.FooterStyle().Render("r: retry  esc: back"))
	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// checkGlyph returns a profile-appropriate glyph for a check status.
func checkGlyph(p styles.Profile, s engine.Status) string {
	switch s {
	case engine.StatusOK:
		return p.Glyph(styles.GlyphOK)
	case engine.StatusWarning:
		return p.Glyph(styles.GlyphWarn)
	case engine.StatusCritical:
		return p.Glyph(styles.GlyphFail)
	case engine.StatusSkip:
		return "[-]"
	default:
		return "[ ]"
	}
}

// summaryLine builds the human-readable summary line from the report.
func summaryLine(p styles.Profile, report engine.DoctorReport) string {
	var passed, warnings, critical, skipped int
	for _, c := range report.Checks {
		switch c.Status {
		case engine.StatusOK:
			passed++
		case engine.StatusWarning:
			warnings++
		case engine.StatusCritical:
			critical++
		case engine.StatusSkip:
			skipped++
		}
	}

	switch report.Summary {
	case engine.StatusOK:
		return p.Glyph(styles.GlyphOK) + fmt.Sprintf("  All checks passed (%d passed, %d skipped)", passed, skipped)
	case engine.StatusWarning:
		return p.Glyph(styles.GlyphWarn) + fmt.Sprintf("  %d warning(s), %d passed, %d skipped", warnings, passed, skipped)
	case engine.StatusCritical:
		return p.Glyph(styles.GlyphFail) + fmt.Sprintf("  %d critical issue(s), %d warning(s), %d passed, %d skipped",
			critical, warnings, passed, skipped)
	default:
		return fmt.Sprintf("Total: %d  Passed: %d  Warnings: %d  Critical: %d  Skipped: %d",
			len(report.Checks), passed, warnings, critical, skipped)
	}
}

// ─── production wiring helpers ───────────────────────────────────────────────

// newDoctorViewReal returns a *doctorView wired with real production OS-level deps.
// Used by NewModelWithStatusDeps to replace the stub doctorView{}.
func newDoctorViewReal(profile styles.Profile) *doctorView {
	osDeps := engine.Deps{
		Client:   dockerx.NewCLIClient(dockerx.ComposeV2),
		Runner:   dockerx.NewOSCommandRunner(),
		FS:       dockerx.NewOSFS(),
		Prober:   defaultHTTPProber(),
		Reporter: engine.DiscardReporter{},
	}
	return newDoctorView(profile, doctorViewDeps{
		doctorFn:   engine.Run,
		engineDeps: osDeps,
	})
}
