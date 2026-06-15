package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/fatih/color"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// ─── HumanPresenter ──────────────────────────────────────────────────────────

// HumanPresenter writes colored/plain progress to stderr. It implements
// engine.Reporter for use with interactive (non-JSON) invocations.
type HumanPresenter struct {
	stderr io.Writer
	policy DecorPolicy
}

// NewHumanPresenter constructs a HumanPresenter that writes to stderr
// according to the given decoration policy.
func NewHumanPresenter(stderr io.Writer, policy DecorPolicy) *HumanPresenter {
	return &HumanPresenter{stderr: stderr, policy: policy}
}

// Report dispatches the event to the appropriate rendering path.
func (p *HumanPresenter) Report(ev engine.Event) {
	switch ev.Kind {
	case engine.EventStepStarted:
		if p.policy.ShowProgress {
			p.fprintf("  » %s\n", ev.Step)
		}

	case engine.EventStepFinished:
		if ev.Err != nil {
			// Errors are always shown, even in quiet mode.
			p.fprintfErr("  ✗ %s: %v\n", ev.Step, ev.Err)
		} else if p.policy.ShowProgress {
			p.fprintfOK("  ✓ %s\n", ev.Step)
		}

	case engine.EventWarning:
		// Warnings are always shown, even in quiet mode.
		p.fprintfWarn("  ! %s: %s\n", ev.Step, ev.Message)

	case engine.EventInfo:
		if p.policy.ShowProgress {
			p.fprintf("  · %s\n", ev.Message)
		}
	}
}

// fprintf writes a plain or colored message using the neutral (default) style.
func (p *HumanPresenter) fprintf(format string, args ...any) {
	if p.policy.UseColor {
		color.New(color.FgCyan).Fprintf(p.stderr, format, args...) //nolint:errcheck
	} else {
		fmt.Fprintf(p.stderr, format, args...) //nolint:errcheck
	}
}

// fprintfOK writes a success-styled message (green when color enabled).
func (p *HumanPresenter) fprintfOK(format string, args ...any) {
	if p.policy.UseColor {
		color.New(color.FgGreen).Fprintf(p.stderr, format, args...) //nolint:errcheck
	} else {
		fmt.Fprintf(p.stderr, format, args...) //nolint:errcheck
	}
}

// fprintfErr writes an error-styled message (red when color enabled).
func (p *HumanPresenter) fprintfErr(format string, args ...any) {
	if p.policy.UseColor {
		color.New(color.FgRed).Fprintf(p.stderr, format, args...) //nolint:errcheck
	} else {
		fmt.Fprintf(p.stderr, format, args...) //nolint:errcheck
	}
}

// fprintfWarn writes a warning-styled message (yellow when color enabled).
func (p *HumanPresenter) fprintfWarn(format string, args ...any) {
	if p.policy.UseColor {
		color.New(color.FgYellow).Fprintf(p.stderr, format, args...) //nolint:errcheck
	} else {
		fmt.Fprintf(p.stderr, format, args...) //nolint:errcheck
	}
}

// ─── MachinePresenter ────────────────────────────────────────────────────────

// MachinePresenter is silent during execution; only errors and warnings are
// written to stderr. Subcommands call EmitJSON to write the final structured
// result to stdout.
type MachinePresenter struct {
	stdout io.Writer
	stderr io.Writer
}

// NewMachinePresenter constructs a MachinePresenter with the given streams.
func NewMachinePresenter(stdout, stderr io.Writer) *MachinePresenter {
	return &MachinePresenter{stdout: stdout, stderr: stderr}
}

// Report forwards only errors and warnings to stderr; all progress events are
// silently discarded.
func (p *MachinePresenter) Report(ev engine.Event) {
	switch ev.Kind {
	case engine.EventStepFinished:
		if ev.Err != nil {
			fmt.Fprintf(p.stderr, "error: %s: %v\n", ev.Step, ev.Err) //nolint:errcheck
		}
	case engine.EventWarning:
		fmt.Fprintf(p.stderr, "warning: %s: %s\n", ev.Step, ev.Message) //nolint:errcheck
	}
}

// EmitJSON writes v as a single JSON document to stdout (one document per
// invocation). Returns an error if JSON marshalling or writing fails.
func (p *MachinePresenter) EmitJSON(v any) error {
	return json.NewEncoder(p.stdout).Encode(v)
}

// WriteError writes a human-facing error message to stderr.
func (p *MachinePresenter) WriteError(msg string) {
	fmt.Fprintln(p.stderr, msg) //nolint:errcheck
}

// ─── Stream helpers ───────────────────────────────────────────────────────────

// WriteData writes machine-consumable data to stdout. All subcommands MUST use
// this for final result output (data goes to stdout, decoration to stderr).
func WriteData(stdout io.Writer, format string, args ...any) {
	fmt.Fprintf(stdout, format, args...) //nolint:errcheck
}

// WriteError writes a human-facing error message to stderr.
func WriteError(stderr io.Writer, format string, args ...any) {
	fmt.Fprintf(stderr, format, args...) //nolint:errcheck
}
