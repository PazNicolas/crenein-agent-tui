package tui

import (
	"errors"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	tea "github.com/charmbracelet/bubbletea"
)

func TestListenEngineConvertsEvents(t *testing.T) {
	reporter, ch := NewChanReporter(16)
	fake := &FakeEngine{Events: []engine.Event{
		{Kind: engine.EventStepStarted, Step: "install"},
		{Kind: engine.EventInfo, Step: "install", Message: "pulling image"},
		{Kind: engine.EventWarning, Step: "install", Message: "slow network"},
		{Kind: engine.EventStepFinished, Step: "install"},
		{Kind: engine.EventStepStarted, Step: "healthcheck"},
		{Kind: engine.EventStepFinished, Step: "healthcheck", Err: errors.New("timeout")},
	}}
	go func() {
		fake.Run(reporter)
		reporter.Close()
	}()

	type check struct {
		name string
		want tea.Msg
	}
	checks := []check{
		{"step_started_install", StepStartedMsg{Step: "install"}},
		{"info_pulling_image", StepProgressMsg{Step: "install", Message: "pulling image"}},
		{"warn_slow_network", StepProgressMsg{Step: "install", Message: "WARN: slow network"}},
		{"step_done_install", StepDoneMsg{Step: "install"}},
		{"step_started_healthcheck", StepStartedMsg{Step: "healthcheck"}},
		{"step_failed_healthcheck", StepFailedMsg{Step: "healthcheck", Err: errors.New("timeout")}},
		{"operation_finished", OperationFinishedMsg{}},
	}

	for i, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			cmd := ListenEngine(ch)
			got := cmd()

			switch want := c.want.(type) {
			case StepStartedMsg:
				g, ok := got.(StepStartedMsg)
				if !ok {
					t.Fatalf("check %d: got %T, want StepStartedMsg", i, got)
				}
				if g.Step != want.Step {
					t.Errorf("Step = %q, want %q", g.Step, want.Step)
				}

			case StepProgressMsg:
				g, ok := got.(StepProgressMsg)
				if !ok {
					t.Fatalf("check %d: got %T, want StepProgressMsg", i, got)
				}
				if g.Step != want.Step || g.Message != want.Message {
					t.Errorf("got {%q, %q}, want {%q, %q}", g.Step, g.Message, want.Step, want.Message)
				}

			case StepDoneMsg:
				g, ok := got.(StepDoneMsg)
				if !ok {
					t.Fatalf("check %d: got %T, want StepDoneMsg", i, got)
				}
				if g.Step != want.Step {
					t.Errorf("Step = %q, want %q", g.Step, want.Step)
				}

			case StepFailedMsg:
				g, ok := got.(StepFailedMsg)
				if !ok {
					t.Fatalf("check %d: got %T, want StepFailedMsg", i, got)
				}
				if g.Step != want.Step {
					t.Errorf("Step = %q, want %q", g.Step, want.Step)
				}
				if g.Err == nil || g.Err.Error() != want.Err.Error() {
					t.Errorf("Err = %v, want %v", g.Err, want.Err)
				}

			case OperationFinishedMsg:
				if _, ok := got.(OperationFinishedMsg); !ok {
					t.Fatalf("check %d: got %T, want OperationFinishedMsg", i, got)
				}
			}
		})
	}
}

func TestChanReporterDropsWhenFull(t *testing.T) {
	// Buffer size of 1 — second Report must drop rather than block.
	reporter, ch := NewChanReporter(1)

	reporter.Report(engine.Event{Kind: engine.EventInfo, Message: "first"})
	// This must return immediately (drop), not deadlock.
	reporter.Report(engine.Event{Kind: engine.EventInfo, Message: "second"})

	// Only the first event should be in the channel.
	select {
	case ev := <-ch:
		if ev.Message != "first" {
			t.Errorf("expected 'first', got %q", ev.Message)
		}
	default:
		t.Fatal("channel should have had one event")
	}

	// Channel should now be empty.
	select {
	case ev := <-ch:
		t.Errorf("unexpected event in channel: %v", ev)
	default:
		// expected — second event was dropped
	}

	reporter.Close()
}
