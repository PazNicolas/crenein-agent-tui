// Package cmd wires the cobra command tree for crenein-agent.
package cmd

import (
	"errors"
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
		SilenceErrors: true,
	}

	// Unknown or malformed flags → exit 64 (EX_USAGE) instead of exit 1.
	// SetFlagErrorFunc is inherited by all subcommands when cobra propagates it.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err.Error())
	})

	// Persistent flags inherited by all subcommands.
	root.PersistentFlags().BoolVar(&globalFlags.quiet, "quiet", false, "suppress informational progress output")
	root.PersistentFlags().BoolVar(&globalFlags.noColor, "no-color", false, "disable ANSI color output")

	// Render --version with the same rich string as the `version` subcommand.
	root.SetVersionTemplate(versionString(build) + "\n")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newSelfUpdateCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newRollbackCmd())
	return root
}

// Execute builds the command tree with the injected build metadata and runs it.
// cobra prints the error (and a usage hint) to stderr itself; we only translate
// a failure into a non-zero exit status.
// When the error carries an exitCodeError the embedded code is used instead of 1.
func Execute(version, commit, date string) {
	build = buildInfo{version: version, commit: commit, date: date}
	if err := newRootCmd().Execute(); err != nil {
		var ecErr *exitCodeError
		if errors.As(err, &ecErr) {
			os.Exit(ecErr.code)
		}
		os.Exit(1)
	}
}

// globalFlags holds the values of the root-level persistent flags.
var globalFlags struct {
	quiet   bool
	noColor bool
}
