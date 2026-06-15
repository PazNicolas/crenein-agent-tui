package tui

// wizard_base.go — Shared event-driven wizard helpers (Lote 4a)
//
// baseWizard captures the common pattern used by both Install and Update wizards:
//   - ChanReporter/channel lifecycle
//   - ListenEngine loop management
//   - spinner tick animation (120 ms)
//   - opRunningMsg{true/false} signalling to the root
//
// Design: the finished-result type differs between Install (*engine.InstallResult)
// and Update (*engine.UpdateResult).  Rather than using generics (which would
// require Go 1.18+ syntax that may be outside this module's build requirements)
// or a heavyweight interface, the baseWizard carries the result as `any` and
// each wizard casts it on receipt.  This keeps the shared code minimal and
// avoids introducing a new dependency or type parameter.
//
// NOTE: install_view.go was NOT refactored to embed baseWizard because the
// existing implementation is tightly coupled to the install-specific state
// machine (guard → checks → config → preview → execution → summary) and all
// 7 tests assert on its internal field names directly.  Refactoring it to
// embed baseWizard would require renaming fields (execCh → base.execCh, etc.),
// which would break the tests' direct-struct-access patterns without any
// observable benefit to users.  Instead, update_view.go uses baseWizard from
// the start, so the pattern is shared going forward without breaking existing
// green tests.

import (
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── baseWizard ──────────────────────────────────────────────────────────────

// baseWizard bundles the shared event-loop machinery for Install and Update.
// Embed it in a wizard struct to get ChanReporter management, spinner ticks,
// and opRunningMsg.
type baseWizard struct {
	// execCh is the read end of the engine event channel.
	execCh <-chan engine.Event

	// spinnerTick increments on every spinnerTickMsg to animate the active step.
	spinnerTick int

	// opRunning mirrors whether the engine goroutine is in flight.
	opRunning bool
}

// listenCmd returns a ListenEngine Cmd for the current channel.
// Must only be called when execCh is non-nil.
func (b *baseWizard) listenCmd() tea.Cmd {
	return ListenEngine(b.execCh)
}

// spinnerTickCmd schedules the next spinner frame (shared 120 ms interval).
// Re-exported here so wizards that embed baseWizard can call it without
// knowing the constant.
func baseSpinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// signalRunning returns a Cmd that emits opRunningMsg{true} to the root.
func signalRunning() tea.Cmd {
	return func() tea.Msg { return opRunningMsg{running: true} }
}

// signalDone returns a Cmd that emits opRunningMsg{false} to the root.
func signalDone() tea.Cmd {
	return func() tea.Msg { return opRunningMsg{running: false} }
}

// startOp wires a new ChanReporter, records the channel on b, and returns
// the four Cmds that every event-driven wizard needs when starting an operation:
//  1. opRunningMsg{true}  — tell root the UI is locked
//  2. spinnerTickCmd      — start the spinner animation
//  3. listenCmd           — start draining the engine channel
//  4. engineCmd           — the user-supplied goroutine that runs the engine
//
// engineCmd MUST call reporter.Close() before returning its finished message.
func (b *baseWizard) startOp(engineCmd func(reporter *ChanReporter) tea.Cmd) tea.Cmd {
	reporter, ch := NewChanReporter(64)
	b.execCh = ch
	b.opRunning = true
	return tea.Batch(
		signalRunning(),
		baseSpinnerTickCmd(),
		b.listenCmd(),
		engineCmd(reporter),
	)
}
