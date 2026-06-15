package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// runRoot executes the command tree with the given args, capturing stdout.
func runRoot(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute(%v) returned error: %v", args, err)
	}
	return out.String()
}

// TestVersionSubcommandReportsDev asserts the dev default surfaces through the
// `version` subcommand when no ldflags are injected.
func TestVersionSubcommandReportsDev(t *testing.T) {
	build = buildInfo{version: "dev", commit: "none", date: "unknown"}

	got := runRoot(t, "version")
	if !strings.Contains(got, "dev") {
		t.Errorf("version output %q does not contain %q", got, "dev")
	}
	if !strings.Contains(got, "crenein-agent version") {
		t.Errorf("version output %q missing canonical prefix", got)
	}
}

// TestVersionFlagMatchesSubcommand guarantees `--version` and `version` emit
// the same string — a spec-level contract for automation.
func TestVersionFlagMatchesSubcommand(t *testing.T) {
	build = buildInfo{version: "0.1.0", commit: "abc1234", date: "2026-06-12"}

	fromFlag := strings.TrimSpace(runRoot(t, "--version"))
	fromSub := strings.TrimSpace(runRoot(t, "version"))

	if fromFlag != fromSub {
		t.Errorf("--version (%q) != version subcommand (%q)", fromFlag, fromSub)
	}
	if !strings.Contains(fromFlag, "0.1.0") {
		t.Errorf("version output %q does not contain injected version", fromFlag)
	}
	if strings.Contains(fromFlag, "dev") {
		t.Errorf("release version output %q must not contain %q", fromFlag, "dev")
	}
}

// TestUnknownFlag_Exit64 verifies that passing an unknown flag to any
// subcommand exits 64 (EX_USAGE) rather than 1.
func TestUnknownFlag_Exit64(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"root unknown flag", []string{"--this-flag-does-not-exist"}},
		{"version unknown flag", []string{"version", "--this-flag-does-not-exist"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCmd()
			var outBuf, errBuf bytes.Buffer
			root.SetOut(&outBuf)
			root.SetErr(&errBuf)
			root.SilenceErrors = true
			root.SilenceUsage = true
			root.SetArgs(tc.args)

			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for unknown flag %v, got nil", tc.args)
			}

			var ecErr *exitCodeError
			if !errors.As(err, &ecErr) {
				t.Fatalf("expected *exitCodeError, got %T: %v", err, err)
			}
			if ecErr.code != ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage/64)", ecErr.code, ExitUsage)
			}
		})
	}
}
