package cmd

import (
	"os"

	"golang.org/x/term"
)

// TTYState holds injectable booleans representing the TTY state of standard
// streams. Use DetectTTY() to populate from the real file descriptors, or
// construct manually in tests.
type TTYState struct {
	StdinIsTTY  bool
	StdoutIsTTY bool
	StderrIsTTY bool
}

// DetectTTY detects the real TTY state for stdin, stdout, and stderr.
func DetectTTY() TTYState {
	return TTYState{
		StdinIsTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		StdoutIsTTY: term.IsTerminal(int(os.Stdout.Fd())),
		StderrIsTTY: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// UseColor returns true only when color output should be enabled.
//
//   - streamIsTTY: whether the target output stream (e.g. stderr) is a TTY.
//   - jsonMode:    --json flag is active; machine output, no color.
//   - noColorFlag: --no-color flag is active.
//   - noColorEnv:  value of the NO_COLOR env var (empty = not set).
//
// Note: --quiet does NOT disable color; it only suppresses progress lines.
func UseColor(streamIsTTY, jsonMode, noColorFlag bool, noColorEnv string) bool {
	return streamIsTTY && !jsonMode && !noColorFlag && noColorEnv == ""
}

// DecorPolicy bundles all decoration decisions for a single command invocation.
type DecorPolicy struct {
	UseColor     bool // whether to use ANSI color
	ShowProgress bool // whether to show informational progress lines
	JSONMode     bool // whether this is a machine/JSON run
}

// NewDecorPolicy computes the decoration policy from flags and TTY state.
// streamIsTTY should be the TTY state of the stream used for progress output
// (typically stderr).
func NewDecorPolicy(tty TTYState, jsonMode, noColorFlag, quiet bool, noColorEnv string) DecorPolicy {
	return DecorPolicy{
		UseColor:     UseColor(tty.StderrIsTTY, jsonMode, noColorFlag, noColorEnv),
		ShowProgress: !quiet && !jsonMode,
		JSONMode:     jsonMode,
	}
}
