package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// InputDef describes a single promptable input value.
type InputDef struct {
	// Label is the prompt label: printed as "<Label> [<Default>]: ".
	Label string
	// Flag is the cobra flag name (e.g. "api-url"). Used in missing-input errors.
	Flag string
	// EnvVar is the CRENEIN_* env var name (e.g. "CRENEIN_API_URL"). Used in
	// missing-input errors.
	EnvVar string
	// Default is the value used when no flag/env/prompt provides one.
	Default string
	// Secret when true masks the value in prompt echoing (reserved for future use).
	Secret bool
}

// ResolverDeps holds injectable dependencies for testing without a real TTY.
type ResolverDeps struct {
	// Stdin is the reader for interactive prompts. When nil, os.Stdin is used.
	Stdin io.Reader
	// Stderr is the writer for prompts. When nil, os.Stderr is used.
	Stderr io.Writer
	// StdinIsTTY reports whether stdin is a TTY.
	StdinIsTTY bool
	// StderrIsTTY reports whether stderr is a TTY.
	StderrIsTTY bool
}

// ErrMissingInput is returned by Resolve when a value would require a TTY
// prompt but no TTY is available. Callers should collect all missing inputs
// before returning a combined error.
var ErrMissingInput = errors.New("missing input")

// Resolve resolves a single value following the precedence:
//
//	flagValue (non-empty) > os.Getenv(def.EnvVar) (non-empty) > TTY prompt > def.Default
//
// If a prompt is needed but no TTY is available, ("", ErrMissingInput) is
// returned.
func Resolve(flagValue string, def InputDef, deps ResolverDeps) (string, error) {
	// 1. Flag value wins.
	if flagValue != "" {
		return flagValue, nil
	}

	// 2. Environment variable.
	if def.EnvVar != "" {
		if v := os.Getenv(def.EnvVar); v != "" {
			return v, nil
		}
	}

	// 3. TTY prompt.
	stdin := deps.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stderr := deps.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	// Per spec: prompting requires an interactive stdin AND stderr. If either
	// is not a TTY, the value cannot be prompted for and becomes a missing input.
	hasTTY := deps.StdinIsTTY && deps.StderrIsTTY
	if hasTTY {
		fmt.Fprintf(stderr, "%s [%s]: ", def.Label, def.Default) //nolint:errcheck
		// Avoid double-wrapping when stdin is already a *bufio.Reader (shared buffer).
		var reader *bufio.Reader
		if br, ok := stdin.(*bufio.Reader); ok {
			reader = br
		} else {
			reader = bufio.NewReader(stdin)
		}
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return def.Default, nil
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return def.Default, nil
		}
		return line, nil
	}

	// 4. No TTY and no value.
	return "", ErrMissingInput
}

// ResolveAll resolves multiple inputs in order. If any are missing (no TTY),
// it collects ALL missing ones and returns a single exit-64 error listing each
// with its --flag and CRENEIN_* env var.
func ResolveAll(values []string, defs []InputDef, deps ResolverDeps) ([]string, error) {
	results := make([]string, len(defs))
	var missing []InputDef

	for i, def := range defs {
		var flagValue string
		if i < len(values) {
			flagValue = values[i]
		}
		v, err := Resolve(flagValue, def, deps)
		if err != nil {
			if errors.Is(err, ErrMissingInput) {
				missing = append(missing, def)
				continue
			}
			return nil, err
		}
		results[i] = v
	}

	if len(missing) > 0 {
		var sb strings.Builder
		sb.WriteString("missing required inputs (no TTY and --yes not set):\n")
		for _, m := range missing {
			sb.WriteString(fmt.Sprintf("  --%s  (env: %s)\n", m.Flag, m.EnvVar))
		}
		msg := strings.TrimRight(sb.String(), "\n")
		return nil, &exitCodeError{code: ExitUsage, err: fmt.Errorf("%s", msg)}
	}

	return results, nil
}
