package tui

// update_view.go — Update Wizard (Lote 4a: tasks 4.1–4.4)
//
// State machine:
//   updateStepPreview   — version preview + release notes (loaded via manifest)
//   updateStepConfirm   — explicit confirmation before starting
//   updateStepExecution — live engine events (backup → pull → recreate → health-*)
//   updateStepResult    — success, rollback, or error result

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── wizard step constants ────────────────────────────────────────────────────

type updateStep int

const (
	updateStepPreview   updateStep = iota
	updateStepConfirm              // never actually rendered separately — preview advances directly
	updateStepExecution            // live progress
	updateStepResult               // final screen
)

// ─── internal messages ────────────────────────────────────────────────────────

// updatePreviewLoadedMsg is sent when the version preview data has been resolved.
type updatePreviewLoadedMsg struct {
	currentVersion  string
	targetVersion   string
	releaseNotes    string
	alreadyUpToDate bool
	err             error // manifest unavailable
}

// updateFinishedMsg is sent when the engine.Update goroutine returns.
type updateFinishedMsg struct {
	res *engine.UpdateResult
	err error
}

// ─── updateViewDeps — injection seam ─────────────────────────────────────────

// updateViewDeps holds injectable dependencies for the update wizard.
type updateViewDeps struct {
	// updateFn calls the real or fake engine.Update.
	updateFn func(ctx context.Context, deps engine.Deps, opts engine.UpdateOptions) (*engine.UpdateResult, error)

	// manifestClient is used for version preview (FetchManifest + DetectAgentVersion).
	// When nil a real client is built at construction time.
	manifestClient release.Client
}

// ─── updateView ──────────────────────────────────────────────────────────────

// updateView is the Update Wizard Bubbletea sub-model.
type updateView struct {
	version string
	profile styles.Profile
	deps    updateViewDeps
	base    baseWizard

	step updateStep

	// ── updateStepPreview ──
	previewLoading  bool
	currentVersion  string
	targetVersion   string
	releaseNotes    string
	alreadyUpToDate bool
	previewErr      error // manifest unavailable

	// ── updateStepExecution ──
	execSteps  []execStepState // reuses execStepState from install_view.go
	execCtx    context.Context //nolint:containedctx
	execCancel context.CancelFunc
	execDone   bool

	// ── updateStepResult ──
	execResult *engine.UpdateResult
	execError  error

	width  int
	height int
}

// orderedUpdateSteps is the canonical display order for engine update steps.
var orderedUpdateSteps = []string{
	"preflight",
	"detect-state",
	"backup",
	"pull",
	"recreate",
	"health-backend",
	"health-frontend",
	"health-databases",
	"cleanup",
	"rollback",
}

// ─── constructors ─────────────────────────────────────────────────────────────

// newUpdateView constructs an updateView with injectable deps.
func newUpdateView(version string, profile styles.Profile, deps updateViewDeps) *updateView {
	v := &updateView{
		version: version,
		profile: profile,
		deps:    deps,
		step:    updateStepPreview,
	}
	v.initExecSteps()
	return v
}

func (v *updateView) initExecSteps() {
	v.execSteps = make([]execStepState, len(orderedUpdateSteps))
	for i, name := range orderedUpdateSteps {
		v.execSteps[i] = execStepState{name: name, status: "pending"}
	}
}

// ─── tea.Model implementation ─────────────────────────────────────────────────

// Init kicks off the version-preview load.
func (v *updateView) Init() tea.Cmd {
	v.previewLoading = true
	return v.loadPreviewCmd()
}

// Update handles all messages for the update wizard.
func (v *updateView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil

	case spinnerTickMsg:
		if v.step == updateStepExecution && !v.execDone {
			v.base.spinnerTick++
			return v, baseSpinnerTickCmd()
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)

	// ── preview loaded ──
	case updatePreviewLoadedMsg:
		v.previewLoading = false
		v.currentVersion = msg.currentVersion
		v.targetVersion = msg.targetVersion
		v.releaseNotes = msg.releaseNotes
		v.alreadyUpToDate = msg.alreadyUpToDate
		v.previewErr = msg.err
		return v, nil

	// ── engine step events ──
	case StepStartedMsg:
		v.markExecStep(msg.Step, "running", "", "")
		return v, v.base.listenCmd()

	case StepDoneMsg:
		v.markExecStep(msg.Step, "done", "", "")
		return v, v.base.listenCmd()

	case StepFailedMsg:
		fix := ""
		var cnerErr *cnerr.Error
		if errors.As(msg.Err, &cnerErr) {
			fix = cnerErr.FixSuggestion
		}
		v.markExecStep(msg.Step, "failed", msg.Err.Error(), fix)
		v.markRemainingSkipped(msg.Step)
		return v, v.base.listenCmd()

	case StepProgressMsg:
		return v, v.base.listenCmd()

	case OperationFinishedMsg:
		// Engine channel closed — wait for updateFinishedMsg.
		return v, nil

	// ── engine goroutine finished ──
	case updateFinishedMsg:
		v.execDone = true
		v.execResult = msg.res
		v.execError = msg.err
		if v.execCancel != nil {
			v.execCancel()
		}
		v.base.opRunning = false
		v.step = updateStepResult
		return v, signalDone()
	}

	return v, nil
}

// View renders the current wizard step.
func (v *updateView) View() string {
	switch v.step {
	case updateStepPreview, updateStepConfirm:
		return v.renderPreview()
	case updateStepExecution:
		return v.renderExecution()
	case updateStepResult:
		return v.renderResult()
	}
	return v.profile.ActiveViewStyle().Render("Update Wizard")
}

// ─── key handling ─────────────────────────────────────────────────────────────

func (v *updateView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.step {

	case updateStepPreview, updateStepConfirm:
		switch msg.String() {
		case "enter", "y":
			// Already up-to-date or preview error → no action, allow navigation back.
			if v.alreadyUpToDate || v.previewErr != nil {
				return v, func() tea.Msg { return NavigateBackMsg{} }
			}
			// Manifest is still loading → ignore confirm.
			if v.previewLoading {
				return v, nil
			}
			// Start execution.
			v.step = updateStepExecution
			return v, v.startExecutionCmd()
		case "esc", "q":
			return v, func() tea.Msg { return NavigateBackMsg{} }
		}

	case updateStepResult:
		// Any key navigates back to Status.
		switch msg.String() {
		case "esc", "q", "enter":
			return v, func() tea.Msg { return NavigateBackMsg{} }
		}

	case updateStepExecution:
		// Keys handled by root during execution.
	}

	return v, nil
}

// ─── commands ─────────────────────────────────────────────────────────────────

// loadPreviewCmd fetches manifest and detects current version out-of-band.
func (v *updateView) loadPreviewCmd() tea.Cmd {
	mc := v.deps.manifestClient
	return func() tea.Msg {
		ctx := context.Background()

		currentVersion := "unknown"
		if mc != nil {
			currentVersion = mc.DetectAgentVersion(ctx)
		}

		if mc == nil {
			return updatePreviewLoadedMsg{
				currentVersion: currentVersion,
				err:            fmt.Errorf("manifest client not configured"),
			}
		}

		m, fetchErr := mc.FetchManifest(ctx, false)
		if fetchErr != nil {
			return updatePreviewLoadedMsg{
				currentVersion: currentVersion,
				err:            fmt.Errorf("manifest unavailable: %w", fetchErr),
			}
		}

		targetVersion := m.Agent.Latest
		releaseNotes := release.ResolveReleaseNotes(m, targetVersion)

		// already-up-to-date: strip leading "v" from currentVersion for comparison.
		cur := strings.TrimPrefix(currentVersion, "v")
		alreadyUpToDate := (cur != "" && cur != "unknown" && !strings.HasPrefix(cur, "unknown") &&
			release.CompareSemver(cur, targetVersion) >= 0)

		return updatePreviewLoadedMsg{
			currentVersion:  currentVersion,
			targetVersion:   targetVersion,
			releaseNotes:    releaseNotes,
			alreadyUpToDate: alreadyUpToDate,
		}
	}
}

// startExecutionCmd starts the engine update goroutine and the listen loop.
func (v *updateView) startExecutionCmd() tea.Cmd {
	v.initExecSteps()

	ctx, cancel := context.WithCancel(context.Background())
	v.execCtx = ctx
	v.execCancel = cancel

	targetVersion := v.targetVersion
	updateFn := v.deps.updateFn
	if updateFn == nil {
		updateFn = engine.Update
	}

	return v.base.startOp(func(reporter *ChanReporter) tea.Cmd {
		return func() tea.Msg {
			deps := engine.Deps{
				Client:   dockerx.NewCLIClient(dockerx.ComposeV2),
				Runner:   dockerx.NewOSCommandRunner(),
				FS:       dockerx.NewOSFS(),
				Prober:   defaultHTTPProber(),
				Reporter: reporter,
			}
			opts := engine.UpdateOptions{
				Version: targetVersion,
			}
			res, err := updateFn(ctx, deps, opts)
			reporter.Close()
			return updateFinishedMsg{res: res, err: err}
		}
	})
}

// ─── step state helpers ───────────────────────────────────────────────────────

func (v *updateView) markExecStep(name, st, errMsg, fix string) {
	for i := range v.execSteps {
		if v.execSteps[i].name == name {
			v.execSteps[i].status = st
			v.execSteps[i].errMsg = errMsg
			v.execSteps[i].fix = fix
			return
		}
	}
}

func (v *updateView) markRemainingSkipped(failedStep string) {
	past := false
	for i := range v.execSteps {
		if v.execSteps[i].name == failedStep {
			past = true
			continue
		}
		if past && v.execSteps[i].status == "pending" {
			v.execSteps[i].status = "skipped"
		}
	}
}

// ─── renderers ───────────────────────────────────────────────────────────────

func (v *updateView) renderPreview() string {
	p := v.profile
	title := p.TitleStyle().Render("Update Wizard — Version Preview")
	lines := []string{title, ""}

	if v.previewLoading {
		lines = append(lines, "Loading version information…")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	if v.previewErr != nil {
		lines = append(lines, p.Glyph(styles.GlyphWarn)+" Version check unavailable: "+v.previewErr.Error())
		lines = append(lines, "")
		lines = append(lines, "Cannot determine update status. Please check your network connection.")
		lines = append(lines, "")
		lines = append(lines, "Press  esc  to return to Status.")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	if v.alreadyUpToDate {
		lines = append(lines, p.Glyph(styles.GlyphOK)+" Agent is already up to date")
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("  Current version:   %s", v.currentVersion))
		lines = append(lines, fmt.Sprintf("  Latest available:  %s", v.targetVersion))
		lines = append(lines, "")
		lines = append(lines, "No update action is available.")
		lines = append(lines, "")
		lines = append(lines, "Press  esc  to return to Status.")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	// Normal preview with update available.
	lines = append(lines, fmt.Sprintf("  %s  →  %s", v.currentVersion, v.targetVersion))
	lines = append(lines, "")

	if v.releaseNotes != "" {
		lines = append(lines, "Release notes:")
		for _, note := range strings.Split(v.releaseNotes, "\n") {
			lines = append(lines, "  "+note)
		}
		lines = append(lines, "")
	}

	lines = append(lines, "What will be updated:")
	lines = append(lines, "  "+p.Glyph(styles.GlyphOK)+"  agent  (crenein/c-network-agent-back:"+v.targetVersion+")")
	lines = append(lines, "  "+p.Glyph(styles.GlyphOK)+"  frontend  (crenein/c-network-agent-front:"+v.targetVersion+")")
	lines = append(lines, "")
	lines = append(lines, "What will NOT be touched:")
	lines = append(lines, "  —  mongodb  (database image unchanged)")
	lines = append(lines, "  —  influxdb")
	lines = append(lines, "  —  redis")
	lines = append(lines, "  —  /data/*  (all persistent data is preserved)")
	lines = append(lines, "")
	lines = append(lines, "A backup of docker-compose.yml, .env, and image IDs will be created before update.")
	lines = append(lines, "")
	lines = append(lines, "Press  enter  or  y  to confirm and begin the update.")
	lines = append(lines, "Press  esc  to cancel.")

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *updateView) renderExecution() string {
	p := v.profile
	title := p.TitleStyle().Render("Update — In Progress…")
	lines := []string{title, ""}

	spinner := []string{"|", "/", "-", "\\"}[v.base.spinnerTick%4]

	for _, s := range v.execSteps {
		var marker string
		switch s.status {
		case "done":
			marker = p.Glyph(styles.GlyphOK)
		case "failed":
			marker = p.Glyph(styles.GlyphFail)
		case "running":
			marker = spinner
		case "skipped":
			marker = "  —  "
		default: // pending
			marker = "     "
		}
		lines = append(lines, fmt.Sprintf("  %s  %s", marker, s.name))
		if s.status == "failed" && s.errMsg != "" {
			lines = append(lines, fmt.Sprintf("       Error: %s", s.errMsg))
			if s.fix != "" {
				lines = append(lines, fmt.Sprintf("       Fix:   %s", s.fix))
			}
		}
	}

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *updateView) renderResult() string {
	p := v.profile
	res := v.execResult
	err := v.execError

	lines := []string{""}

	// ── Rollback case ──
	if res != nil && res.RolledBack {
		title := p.TitleStyle().Render("Update Result — Rollback")
		lines[0] = title

		lines = append(lines, "")
		lines = append(lines, p.Glyph(styles.GlyphFail)+" The update failed. Automatic rollback was triggered.")
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("  Target version:    %s", v.targetVersion))
		if res.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("  Backup location:   %s", res.BackupPath))
		}
		lines = append(lines, "")

		if res.RollbackFailed {
			lines = append(lines, p.Glyph(styles.GlyphFail)+" Rollback ALSO FAILED — system may be in an inconsistent state.")
			lines = append(lines, "")
			lines = append(lines, "Manual recovery steps:")
			lines = append(lines, "  1. Check container state:  docker compose ps")
			lines = append(lines, "  2. Check agent logs:       docker compose logs agent")
			if res.BackupPath != "" {
				lines = append(lines, "  3. Restore from backup:")
				lines = append(lines, fmt.Sprintf("       cp %s/.env . && cp %s/docker-compose.yml .", res.BackupPath, res.BackupPath))
				lines = append(lines, "       docker compose up -d")
			}
			lines = append(lines, "  4. Or pull the previous image manually and recreate")
			if err != nil {
				lines = append(lines, "")
				lines = append(lines, "  Error detail: "+err.Error())
				var cnerErr *cnerr.Error
				if errors.As(err, &cnerErr) && cnerErr.FixSuggestion != "" {
					lines = append(lines, "  Fix:          "+cnerErr.FixSuggestion)
				}
			}
		} else {
			lines = append(lines, p.Glyph(styles.GlyphWarn)+" Rollback completed — previous version restored.")
			if res.PreviousAgentImageID != "" {
				lines = append(lines, fmt.Sprintf("  Restored agent image: %s", res.PreviousAgentImageID))
			}
			if err != nil {
				lines = append(lines, "")
				lines = append(lines, "  Update failure cause: "+err.Error())
				var cnerErr *cnerr.Error
				if errors.As(err, &cnerErr) && cnerErr.FixSuggestion != "" {
					lines = append(lines, "  Fix:                  "+cnerErr.FixSuggestion)
				}
			}
		}
		lines = append(lines, "")
		lines = append(lines, "Press  esc  or  enter  to return to Status.")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	// ── Error without rollback ──
	if err != nil {
		title := p.TitleStyle().Render("Update Result — Failed")
		lines[0] = title
		lines = append(lines, "")
		lines = append(lines, p.Glyph(styles.GlyphFail)+" Update failed.")
		lines = append(lines, "")
		lines = append(lines, "  Error: "+err.Error())
		var cnerErr *cnerr.Error
		if errors.As(err, &cnerErr) && cnerErr.FixSuggestion != "" {
			lines = append(lines, "  Fix:   "+cnerErr.FixSuggestion)
		}
		lines = append(lines, "")
		lines = append(lines, "Press  esc  or  enter  to return to Status.")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	// ── Success ──
	title := p.TitleStyle().Render("Update Complete")
	lines[0] = title
	lines = append(lines, "")
	lines = append(lines, p.Glyph(styles.GlyphOK)+" Update successful!")
	lines = append(lines, "")

	if res != nil {
		newVersion := v.targetVersion
		if res.NewAgentImageID != "" {
			lines = append(lines, fmt.Sprintf("  New agent version:    %s", newVersion))
		}
		if res.BackupPath != "" {
			lines = append(lines, fmt.Sprintf("  Backup location:      %s", res.BackupPath))
		}
		if len(res.Warnings) > 0 {
			lines = append(lines, "")
			lines = append(lines, "Warnings:")
			for _, w := range res.Warnings {
				lines = append(lines, "  "+p.Glyph(styles.GlyphWarn)+"  "+w)
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, "Press  esc  or  enter  to return to Status.")
	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}
