// Package engine defines the shared types, interfaces, and dependency
// container used by all three sub-engines: install, update, and doctor.
//
// Design (AD-1): engines are pure and UI-agnostic. Progress flows through the
// Reporter event sink. Interactive decisions are resolved before the engine
// runs, via typed Options structs.
//
// Design (AD-2): all external effects go through the narrow seams defined in
// internal/dockerx: Client (docker/compose), CommandRunner (apt-get, systemctl,
// etc.), FS (filesystem reads/writes), and HTTPProber (network probes).
// Engines never call exec.Command or os.WriteFile directly.
package engine

import (
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Reporter (AD-1) ─────────────────────────────────────────────────────────

// EventKind classifies a Reporter event.
type EventKind string

const (
	// EventStepStarted is emitted when a named step begins.
	EventStepStarted EventKind = "step_started"
	// EventStepFinished is emitted when a named step completes (with or without
	// an error).
	EventStepFinished EventKind = "step_finished"
	// EventWarning is emitted for non-fatal conditions that the operator should
	// review.
	EventWarning EventKind = "warning"
	// EventInfo is emitted for informational messages (e.g. log entries).
	EventInfo EventKind = "info"
)

// Event carries a single progress notification from an engine operation.
type Event struct {
	// Kind classifies the event.
	Kind EventKind
	// Step is the human-readable name of the current operation step
	// (e.g. "preflight", "apt-update", "docker-pull").
	Step string
	// Message is an optional detail string.
	Message string
	// Err is set when Kind == EventStepFinished and the step failed, or when
	// Kind == EventWarning.
	Err error
}

// Reporter is the event sink that engines use to communicate progress to their
// callers (TUI, headless CLI, tests). All methods must be safe to call
// concurrently.
//
// Implementations MUST NOT block the engine (buffer or drop events if the
// consumer is slow). A nil Reporter is always valid — DiscardReporter handles
// it.
type Reporter interface {
	// Report emits an event to the consumer. The engine calls this
	// synchronously during the operation.
	Report(ev Event)
}

// DiscardReporter is a Reporter that silently drops every event. Useful when
// no progress reporting is desired (e.g. in unit tests that only assert the
// return value).
type DiscardReporter struct{}

// Report discards the event.
func (DiscardReporter) Report(_ Event) {}

// ReporterFunc adapts a plain function to the Reporter interface.
type ReporterFunc func(ev Event)

// Report calls f(ev).
func (f ReporterFunc) Report(ev Event) { f(ev) }

// ─── Deps (AD-2) ─────────────────────────────────────────────────────────────

// Deps bundles every injected dependency that install, update, and doctor
// share. Constructing the struct with all fields set ensures that tests can
// replace any dependency without touching unrelated fields.
type Deps struct {
	// Client is the Docker/compose client.
	Client dockerx.Client

	// Runner is the system command runner (apt-get, systemctl, etc.).
	Runner dockerx.CommandRunner

	// FS is the filesystem seam for all path-based reads and writes.
	FS dockerx.FS

	// Prober is the HTTP client seam for connectivity checks and API calls.
	Prober dockerx.HTTPProber

	// Reporter receives progress events during engine operations. When nil,
	// events are discarded.
	Reporter Reporter
}

// report is a convenience helper that no-ops when deps.Reporter is nil.
func (d *Deps) report(ev Event) {
	if d.Reporter != nil {
		d.Reporter.Report(ev)
	}
}

// StepStarted emits an EventStepStarted event.
func (d *Deps) StepStarted(step string) {
	d.report(Event{Kind: EventStepStarted, Step: step})
}

// StepFinished emits an EventStepFinished event with an optional error.
func (d *Deps) StepFinished(step string, err error) {
	d.report(Event{Kind: EventStepFinished, Step: step, Err: err})
}

// Warn emits an EventWarning event.
func (d *Deps) Warn(step, message string) {
	d.report(Event{Kind: EventWarning, Step: step, Message: message})
}

// Info emits an EventInfo event.
func (d *Deps) Info(step, message string) {
	d.report(Event{Kind: EventInfo, Step: step, Message: message})
}
