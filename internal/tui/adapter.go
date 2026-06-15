package tui

import (
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// Msg types — TUI messages produced by the engine adapter.
// ---------------------------------------------------------------------------

// StepStartedMsg signals that a named engine step has begun.
type StepStartedMsg struct{ Step string }

// StepProgressMsg carries an informational or warning message for an in-flight
// step.
//
// EventWarning is mapped here (prefixed "WARN: ") rather than to a dedicated
// msg type. The distinction is presentational: views that want to render
// warnings differently can inspect the Message prefix. Keeping a single
// progress type avoids combinatorial growth in Update match arms.
type StepProgressMsg struct {
	Step    string
	Message string
}

// StepDoneMsg signals successful completion of a step.
type StepDoneMsg struct{ Step string }

// StepFailedMsg signals that a step completed with an error.
type StepFailedMsg struct {
	Step string
	Err  error
}

// OperationFinishedMsg signals that the engine channel has closed (all steps
// done). Err is always nil when derived from channel close; callers that need
// to surface an overall operation error should send a final EventStepFinished
// with Err set before closing the channel.
type OperationFinishedMsg struct{ Err error }

// ---------------------------------------------------------------------------
// ListenEngine — standard Bubbletea listen-loop pattern for external streams.
// ---------------------------------------------------------------------------

// ListenEngine returns a tea.Cmd that reads ONE event from ch and converts it
// to the corresponding tea.Msg. The Update handler that receives the msg is
// responsible for re-issuing ListenEngine(ch) to continue draining the channel
// (except when OperationFinishedMsg is received, which signals channel close).
func ListenEngine(ch <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return OperationFinishedMsg{}
		}
		switch ev.Kind {
		case engine.EventStepStarted:
			return StepStartedMsg{Step: ev.Step}
		case engine.EventStepFinished:
			if ev.Err != nil {
				return StepFailedMsg{Step: ev.Step, Err: ev.Err}
			}
			return StepDoneMsg{Step: ev.Step}
		case engine.EventWarning:
			// EventWarning maps to StepProgressMsg with a "WARN: " prefix so
			// views can render it differently without a separate msg type.
			return StepProgressMsg{Step: ev.Step, Message: "WARN: " + ev.Message}
		case engine.EventInfo:
			return StepProgressMsg{Step: ev.Step, Message: ev.Message}
		default:
			return StepProgressMsg{Step: ev.Step, Message: ev.Message}
		}
	}
}

// ---------------------------------------------------------------------------
// ChanReporter — engine.Reporter that pushes events to a buffered channel.
// ---------------------------------------------------------------------------

// ChanReporter wraps a send-only channel and implements engine.Reporter.
// Report is non-blocking: if the buffer is full the event is dropped so that
// the engine goroutine never blocks on a slow TUI.
type ChanReporter struct {
	ch chan<- engine.Event
}

// NewChanReporter creates a ChanReporter and returns the matching read channel.
// bufSize controls how many events can queue before drops occur.
func NewChanReporter(bufSize int) (*ChanReporter, <-chan engine.Event) {
	ch := make(chan engine.Event, bufSize)
	return &ChanReporter{ch: ch}, ch
}

// Report sends ev to the internal channel. Drops the event silently if the
// buffer is full — the engine must not block.
func (r *ChanReporter) Report(ev engine.Event) {
	select {
	case r.ch <- ev:
	default:
		// drop — buffer full
	}
}

// Close signals to consumers that no more events will arrive.
func (r *ChanReporter) Close() { close(r.ch) }

// ---------------------------------------------------------------------------
// FakeEngine — scripted event source for tests.
// ---------------------------------------------------------------------------

// FakeEngine emits a pre-configured sequence of events then returns, allowing
// callers to close the channel and trigger OperationFinishedMsg.
type FakeEngine struct {
	Events []engine.Event
}

// Run replays all scripted events through the given reporter. The caller is
// responsible for closing the underlying channel after Run returns.
func (f *FakeEngine) Run(reporter engine.Reporter) {
	for _, ev := range f.Events {
		reporter.Report(ev)
	}
}
