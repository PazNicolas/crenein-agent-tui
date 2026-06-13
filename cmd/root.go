// Package cmd wires the cobra command tree for crenein-agent.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// buildInfo holds the build-time metadata injected from the main package.
type buildInfo struct {
	version string
	commit  string
	date    string
}

var build buildInfo

// newRootCmd constructs the root command. It is a constructor (rather than a
// package-level var) so tests can build an isolated command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "crenein-agent",
		Short: "CRENEIN C-Network agent installer and manager",
		Long: "crenein-agent installs, updates, and supervises the CRENEIN " +
			"C-Network agent stack on a client host.\n\n" +
			"This is Phase 1 (scaffold + distribution): only the version " +
			"command is functional. Engine, headless commands, the TUI " +
			"dashboard, and self-update arrive in later releases.",
		Version:       build.version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	// Render --version with the same rich string as the `version` subcommand.
	root.SetVersionTemplate(versionString(build) + "\n")

	root.AddCommand(newVersionCmd())
	return root
}

// Execute builds the command tree with the injected build metadata and runs it.
// cobra prints the error (and a usage hint) to stderr itself; we only translate
// a failure into a non-zero exit status.
func Execute(version, commit, date string) {
	build = buildInfo{version: version, commit: commit, date: date}
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
