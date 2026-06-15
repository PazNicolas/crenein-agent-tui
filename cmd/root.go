// Package cmd wires the cobra command tree for crenein-agent.
package cmd

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/tui"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
)

// buildInfo holds the build-time metadata injected from the main package.
type buildInfo struct {
	version string
	commit  string
	date    string
}

var build buildInfo

// shouldRunTUI returns true when conditions for launching the interactive TUI
// dashboard are met: stdout must be a real TTY and TERM must be set and not
// "dumb". Exposed as a plain function so it can be unit-tested without
// spawning a real cobra command.
func shouldRunTUI(tty TTYState, term string) bool {
	return tty.StdoutIsTTY && term != "" && term != "dumb"
}

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
		RunE: func(cmd *cobra.Command, args []string) error {
			tty := DetectTTY()
			term := os.Getenv("TERM")
			if !shouldRunTUI(tty, term) {
				fmt.Fprintf(os.Stdout,
					"crenein-agent runs as an interactive dashboard on a real terminal.\n\n"+
						"For headless/scripted use, run one of the available subcommands:\n"+
						"  crenein-agent status      — show stack status\n"+
						"  crenein-agent install     — install the agent stack\n"+
						"  crenein-agent update      — update the agent stack\n"+
						"  crenein-agent doctor      — run diagnostic checks\n"+
						"  crenein-agent logs        — stream compose logs\n"+
						"  crenein-agent self-update — update the CLI itself\n",
				)
				return nil
			}

			// Choose color profile: --no-color flag takes precedence over detection.
			var profile styles.Profile
			if globalFlags.noColor {
				profile = styles.NewProfile(true)
			} else {
				profile = styles.DetectProfile()
			}

			m := tui.NewModel(build.version, profile)
			p := tea.NewProgram(m, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return opFailureError(err)
			}
			return nil
		},
	}

	// Unknown or malformed flags → exit 64 (EX_USAGE) instead of exit 1.
	// SetFlagErrorFunc is inherited by all subcommands when cobra propagates it.
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		// cobra's own error printing is silenced (SilenceErrors); surface the
		// flag error (which names the offending flag) on stderr ourselves.
		c.PrintErrln("error:", err.Error())
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
