package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// versionString renders the canonical version line shared by `--version` and
// the `version` subcommand:
//
//	crenein-agent version X.Y.Z (commit: abc1234, built: <date>)
func versionString(b buildInfo) string {
	return fmt.Sprintf("crenein-agent version %s (commit: %s, built: %s)",
		b.version, b.commit, b.date)
}

// newVersionCmd returns the `version` subcommand. Its output is identical to
// what `crenein-agent --version` prints, so automation can rely on either.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the crenein-agent version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString(build))
		},
	}
}
