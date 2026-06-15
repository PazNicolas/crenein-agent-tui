package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// ─── Task 1.2: Exit code mapper ──────────────────────────────────────────────

func TestExitCodes(t *testing.T) {
	sentinel := errors.New("sentinel error")

	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"usageError", usageError("x"), ExitUsage},
		{"preflightError", preflightError(sentinel), ExitPreflight},
		{"abortedError", abortedError(), ExitAborted},
		{"rolledBackError", rolledBackError(sentinel), ExitRolledBack},
		{"rollbackFailedError", rollbackFailedError(sentinel), ExitRollbackFailed},
		{"opFailureError", opFailureError(sentinel), ExitOpFailure},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ecErr *exitCodeError
			if !errors.As(tc.err, &ecErr) {
				t.Fatalf("errors.As returned false; want *exitCodeError")
			}
			if ecErr.code != tc.wantCode {
				t.Errorf("code = %d, want %d", ecErr.code, tc.wantCode)
			}
		})
	}
}

// ─── Task 1.3: UseColor ───────────────────────────────────────────────────────

func TestUseColor(t *testing.T) {
	cases := []struct {
		name        string
		streamIsTTY bool
		jsonMode    bool
		noColorFlag bool
		noColorEnv  string
		want        bool
	}{
		{
			name:        "all_good",
			streamIsTTY: true, jsonMode: false, noColorFlag: false, noColorEnv: "",
			want: true,
		},
		{
			name:        "no_tty",
			streamIsTTY: false, jsonMode: false, noColorFlag: false, noColorEnv: "",
			want: false,
		},
		{
			name:        "json_mode",
			streamIsTTY: true, jsonMode: true, noColorFlag: false, noColorEnv: "",
			want: false,
		},
		{
			name:        "no_color_flag",
			streamIsTTY: true, jsonMode: false, noColorFlag: true, noColorEnv: "",
			want: false,
		},
		{
			name:        "no_color_env_set_to_1",
			streamIsTTY: true, jsonMode: false, noColorFlag: false, noColorEnv: "1",
			want: false,
		},
		{
			name:        "no_color_env_non_empty",
			streamIsTTY: true, jsonMode: false, noColorFlag: false, noColorEnv: "true",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UseColor(tc.streamIsTTY, tc.jsonMode, tc.noColorFlag, tc.noColorEnv)
			if got != tc.want {
				t.Errorf("UseColor(%v, %v, %v, %q) = %v, want %v",
					tc.streamIsTTY, tc.jsonMode, tc.noColorFlag, tc.noColorEnv, got, tc.want)
			}
		})
	}
}

// ─── Task 1.4a: HumanPresenter ───────────────────────────────────────────────

func makeHuman(showProgress, useColor bool) (*HumanPresenter, *bytes.Buffer) {
	var buf bytes.Buffer
	p := NewHumanPresenter(&buf, DecorPolicy{
		UseColor:     useColor,
		ShowProgress: showProgress,
	})
	return p, &buf
}

func TestHumanPresenter(t *testing.T) {
	t.Run("step_started_shown_when_progress", func(t *testing.T) {
		p, buf := makeHuman(true, false)
		p.Report(engine.Event{Kind: engine.EventStepStarted, Step: "preflight"})
		if !strings.Contains(buf.String(), "preflight") {
			t.Errorf("expected 'preflight' in stderr, got %q", buf.String())
		}
	})

	t.Run("step_started_hidden_when_quiet", func(t *testing.T) {
		p, buf := makeHuman(false, false)
		p.Report(engine.Event{Kind: engine.EventStepStarted, Step: "preflight"})
		if buf.String() != "" {
			t.Errorf("expected nothing in stderr (quiet), got %q", buf.String())
		}
	})

	t.Run("step_finished_ok_shown_when_progress", func(t *testing.T) {
		p, buf := makeHuman(true, false)
		p.Report(engine.Event{Kind: engine.EventStepFinished, Step: "install", Err: nil})
		if !strings.Contains(buf.String(), "✓") {
			t.Errorf("expected ✓ in stderr, got %q", buf.String())
		}
		if !strings.Contains(buf.String(), "install") {
			t.Errorf("expected 'install' in stderr, got %q", buf.String())
		}
	})

	t.Run("step_finished_ok_hidden_when_quiet", func(t *testing.T) {
		p, buf := makeHuman(false, false)
		p.Report(engine.Event{Kind: engine.EventStepFinished, Step: "install", Err: nil})
		if buf.String() != "" {
			t.Errorf("expected nothing in stderr (quiet), got %q", buf.String())
		}
	})

	t.Run("step_finished_err_always_shown", func(t *testing.T) {
		// Even with quiet=true, errors must appear.
		p, buf := makeHuman(false, false)
		p.Report(engine.Event{
			Kind: engine.EventStepFinished,
			Step: "install",
			Err:  errors.New("disk full"),
		})
		if !strings.Contains(buf.String(), "✗") {
			t.Errorf("expected ✗ in stderr, got %q", buf.String())
		}
		if !strings.Contains(buf.String(), "disk full") {
			t.Errorf("expected error message in stderr, got %q", buf.String())
		}
	})

	t.Run("warning_always_shown", func(t *testing.T) {
		p, buf := makeHuman(false, false)
		p.Report(engine.Event{
			Kind:    engine.EventWarning,
			Step:    "connectivity",
			Message: "slow response",
		})
		if !strings.Contains(buf.String(), "!") {
			t.Errorf("expected warning marker in stderr, got %q", buf.String())
		}
		if !strings.Contains(buf.String(), "slow response") {
			t.Errorf("expected warning message in stderr, got %q", buf.String())
		}
	})

	t.Run("info_shown_when_progress", func(t *testing.T) {
		p, buf := makeHuman(true, false)
		p.Report(engine.Event{Kind: engine.EventInfo, Message: "pulling image"})
		if !strings.Contains(buf.String(), "pulling image") {
			t.Errorf("expected 'pulling image' in stderr, got %q", buf.String())
		}
	})

	t.Run("info_hidden_when_quiet", func(t *testing.T) {
		p, buf := makeHuman(false, false)
		p.Report(engine.Event{Kind: engine.EventInfo, Message: "pulling image"})
		if buf.String() != "" {
			t.Errorf("expected nothing in stderr (quiet), got %q", buf.String())
		}
	})
}

// ─── Task 1.4b: MachinePresenter ─────────────────────────────────────────────

func TestMachinePresenter(t *testing.T) {
	newMP := func() (*MachinePresenter, *bytes.Buffer, *bytes.Buffer) {
		var outBuf, errBuf bytes.Buffer
		return NewMachinePresenter(&outBuf, &errBuf), &outBuf, &errBuf
	}

	t.Run("step_started_silent", func(t *testing.T) {
		mp, outBuf, errBuf := newMP()
		mp.Report(engine.Event{Kind: engine.EventStepStarted, Step: "anything"})
		if outBuf.String() != "" || errBuf.String() != "" {
			t.Errorf("expected silence, got stdout=%q stderr=%q", outBuf, errBuf)
		}
	})

	t.Run("info_silent", func(t *testing.T) {
		mp, outBuf, errBuf := newMP()
		mp.Report(engine.Event{Kind: engine.EventInfo, Message: "something"})
		if outBuf.String() != "" || errBuf.String() != "" {
			t.Errorf("expected silence, got stdout=%q stderr=%q", outBuf, errBuf)
		}
	})

	t.Run("warning_to_stderr", func(t *testing.T) {
		mp, outBuf, errBuf := newMP()
		mp.Report(engine.Event{
			Kind:    engine.EventWarning,
			Step:    "disk",
			Message: "low space",
		})
		if outBuf.String() != "" {
			t.Errorf("expected nothing on stdout, got %q", outBuf)
		}
		if !strings.Contains(errBuf.String(), "warning") {
			t.Errorf("expected 'warning' on stderr, got %q", errBuf)
		}
		if !strings.Contains(errBuf.String(), "low space") {
			t.Errorf("expected warning message on stderr, got %q", errBuf)
		}
	})

	t.Run("step_finished_err_to_stderr", func(t *testing.T) {
		mp, outBuf, errBuf := newMP()
		mp.Report(engine.Event{
			Kind: engine.EventStepFinished,
			Step: "install",
			Err:  errors.New("failed"),
		})
		if outBuf.String() != "" {
			t.Errorf("expected nothing on stdout, got %q", outBuf)
		}
		if !strings.Contains(errBuf.String(), "error") {
			t.Errorf("expected 'error' on stderr, got %q", errBuf)
		}
	})

	t.Run("emit_json_to_stdout", func(t *testing.T) {
		mp, outBuf, errBuf := newMP()
		type result struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}
		if err := mp.EmitJSON(result{Status: "ok", Version: "0.2.0"}); err != nil {
			t.Fatalf("EmitJSON error: %v", err)
		}
		if errBuf.String() != "" {
			t.Errorf("expected nothing on stderr, got %q", errBuf)
		}
		if !strings.Contains(outBuf.String(), `"status"`) {
			t.Errorf("expected JSON on stdout, got %q", outBuf)
		}
		if !strings.Contains(outBuf.String(), `"ok"`) {
			t.Errorf("expected status value on stdout, got %q", outBuf)
		}
	})
}

// ─── Task 1.5a: Resolve ───────────────────────────────────────────────────────

func TestResolve(t *testing.T) {
	def := InputDef{
		Label:   "API URL",
		Flag:    "api-url",
		EnvVar:  "CRENEIN_API_URL",
		Default: "http://localhost:8080",
	}

	t.Run("flag_value_wins", func(t *testing.T) {
		deps := ResolverDeps{StdinIsTTY: false, StderrIsTTY: false}
		got, err := Resolve("https://custom.example.com", def, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://custom.example.com" {
			t.Errorf("got %q, want %q", got, "https://custom.example.com")
		}
	})

	t.Run("env_var_used_when_flag_empty", func(t *testing.T) {
		t.Setenv("CRENEIN_API_URL", "https://env.example.com")
		deps := ResolverDeps{StdinIsTTY: false, StderrIsTTY: false}
		got, err := Resolve("", def, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://env.example.com" {
			t.Errorf("got %q, want %q", got, "https://env.example.com")
		}
	})

	t.Run("tty_prompt_enter_gives_default", func(t *testing.T) {
		// Simulate pressing Enter (empty input) — should return default.
		var errBuf bytes.Buffer
		deps := ResolverDeps{
			Stdin:       strings.NewReader("\n"),
			Stderr:      &errBuf,
			StdinIsTTY:  true,
			StderrIsTTY: true,
		}
		got, err := Resolve("", def, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != def.Default {
			t.Errorf("got %q, want default %q", got, def.Default)
		}
		// Prompt label should appear on stderr.
		if !strings.Contains(errBuf.String(), "API URL") {
			t.Errorf("expected prompt label on stderr, got %q", errBuf.String())
		}
	})

	t.Run("tty_prompt_typed_value", func(t *testing.T) {
		var errBuf bytes.Buffer
		deps := ResolverDeps{
			Stdin:       strings.NewReader("https://typed.example.com\n"),
			Stderr:      &errBuf,
			StdinIsTTY:  true,
			StderrIsTTY: true,
		}
		got, err := Resolve("", def, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://typed.example.com" {
			t.Errorf("got %q, want %q", got, "https://typed.example.com")
		}
	})

	t.Run("no_tty_returns_ErrMissingInput", func(t *testing.T) {
		deps := ResolverDeps{StdinIsTTY: false, StderrIsTTY: false}
		_, err := Resolve("", def, deps)
		if !errors.Is(err, ErrMissingInput) {
			t.Errorf("expected ErrMissingInput, got %v", err)
		}
	})
}

// ─── Task 1.5b: ResolveAll ───────────────────────────────────────────────────

func TestResolveAll(t *testing.T) {
	defs := []InputDef{
		{Label: "API URL", Flag: "api-url", EnvVar: "CRENEIN_API_URL", Default: "http://localhost:8080"},
		{Label: "API Token", Flag: "api-token", EnvVar: "CRENEIN_API_TOKEN", Default: ""},
	}

	t.Run("all_via_flags", func(t *testing.T) {
		deps := ResolverDeps{StdinIsTTY: false, StderrIsTTY: false}
		results, err := ResolveAll([]string{"https://prod.example.com", "tok-abc"}, defs, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if results[0] != "https://prod.example.com" {
			t.Errorf("results[0] = %q, want %q", results[0], "https://prod.example.com")
		}
		if results[1] != "tok-abc" {
			t.Errorf("results[1] = %q, want %q", results[1], "tok-abc")
		}
	})

	t.Run("missing_first_via_tty", func(t *testing.T) {
		var errBuf bytes.Buffer
		deps := ResolverDeps{
			Stdin:       strings.NewReader("https://prompted.example.com\n"),
			Stderr:      &errBuf,
			StdinIsTTY:  true,
			StderrIsTTY: true,
		}
		// Provide second value via flag, first via prompt.
		results, err := ResolveAll([]string{"", "tok-xyz"}, defs, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if results[0] != "https://prompted.example.com" {
			t.Errorf("results[0] = %q, want prompted value", results[0])
		}
		if results[1] != "tok-xyz" {
			t.Errorf("results[1] = %q, want tok-xyz", results[1])
		}
	})

	t.Run("multiple_missing_no_tty_exit64", func(t *testing.T) {
		deps := ResolverDeps{StdinIsTTY: false, StderrIsTTY: false}
		_, err := ResolveAll([]string{"", ""}, defs, deps)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var ecErr *exitCodeError
		if !errors.As(err, &ecErr) {
			t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
		}
		if ecErr.code != ExitUsage {
			t.Errorf("exit code = %d, want %d (ExitUsage)", ecErr.code, ExitUsage)
		}
		// Both missing inputs must appear in the error message.
		msg := err.Error()
		if !strings.Contains(msg, "--api-url") {
			t.Errorf("error message should contain '--api-url', got %q", msg)
		}
		if !strings.Contains(msg, "CRENEIN_API_URL") {
			t.Errorf("error message should contain 'CRENEIN_API_URL', got %q", msg)
		}
		if !strings.Contains(msg, "--api-token") {
			t.Errorf("error message should contain '--api-token', got %q", msg)
		}
		if !strings.Contains(msg, "CRENEIN_API_TOKEN") {
			t.Errorf("error message should contain 'CRENEIN_API_TOKEN', got %q", msg)
		}
	})
}
