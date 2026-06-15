package dockerx

import (
	"context"
	"testing"
)

// TestComposeCmdV1_DoesNotDropF verifies that composeCmd for ComposeV1 does NOT
// strip the -f flag when composeArgs builds V1-style args (no "compose" prefix).
// Regression guard for the bug where args[1:] was unconditional, dropping "-f".
func TestComposeCmdV1_DoesNotDropF(t *testing.T) {
	c := &CLIClient{variant: ComposeV1}

	// Simulate what composeArgs produces for V1 with a composeFile:
	//   ["-f", "docker-compose.yml", "logs", "--no-color", "agent"]
	// composeCmd must pass ALL of these to docker-compose (no stripping).
	args := []string{"-f", "docker-compose.yml", "logs", "--no-color", "agent"}
	cmd := c.composeCmd(context.Background(), args...)

	if cmd.Path == "" {
		t.Fatal("cmd.Path is empty")
	}
	// The first arg to docker-compose should be "-f", not "docker-compose.yml".
	if len(cmd.Args) < 2 {
		t.Fatalf("expected at least 2 cmd.Args, got %d: %v", len(cmd.Args), cmd.Args)
	}
	// cmd.Args[0] is the binary name; cmd.Args[1] is the first real arg.
	if cmd.Args[1] != "-f" {
		t.Errorf("cmd.Args[1] = %q, want \"-f\" (the flag must not be dropped); full args: %v",
			cmd.Args[1], cmd.Args)
	}
}

// TestComposeCmdV1_StripsComposePrefix verifies the defensive path: if args[0]
// happens to be "compose" (v2-style), it is stripped before passing to
// docker-compose (mirrors the runCompose guard).
func TestComposeCmdV1_StripsComposePrefix(t *testing.T) {
	c := &CLIClient{variant: ComposeV1}

	args := []string{"compose", "-f", "docker-compose.yml", "logs"}
	cmd := c.composeCmd(context.Background(), args...)

	if len(cmd.Args) < 2 {
		t.Fatalf("expected at least 2 cmd.Args, got %d: %v", len(cmd.Args), cmd.Args)
	}
	// After stripping "compose", cmd.Args[1] should be "-f".
	if cmd.Args[1] != "-f" {
		t.Errorf("cmd.Args[1] = %q, want \"-f\" after stripping 'compose'; full args: %v",
			cmd.Args[1], cmd.Args)
	}
}

// TestComposeArgsV1_NoComposePrefix verifies that composeArgs for V1 does NOT
// prepend "compose" — so the first arg is "-f" (when composeFile is set) or
// the subcommand directly.
func TestComposeArgsV1_NoComposePrefix(t *testing.T) {
	c := &CLIClient{variant: ComposeV1}

	args := c.composeArgs("docker-compose.yml", "logs", "--no-color")
	if len(args) == 0 {
		t.Fatal("expected non-empty args")
	}
	if args[0] == "compose" {
		t.Errorf("composeArgs for V1 must NOT start with 'compose'; got: %v", args)
	}
	if args[0] != "-f" {
		t.Errorf("composeArgs[0] for V1 with file = %q, want \"-f\"; full: %v", args[0], args)
	}
}
