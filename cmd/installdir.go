package cmd

import "github.com/PazNicolas/crenein-agent-tui/internal/status"

// resolveInstallDir is a thin wrapper around status.ResolveInstallDir.
// It is the single shared entry point used by status, logs, and rollback
// commands; readFile/readDir are injected so the resolution is hermetic in tests.
func resolveInstallDir(
	readFile func(name string) ([]byte, error),
	readDir func(path string) ([]string, error),
	override string,
) string {
	return status.ResolveInstallDir(readFile, readDir, override)
}
