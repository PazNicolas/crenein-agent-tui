package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/status"
)

// ─── Command constructor ──────────────────────────────────────────────────────

// newStatusCmd constructs the `status` subcommand wired to real deps.
func newStatusCmd() *cobra.Command {
	return newStatusCmdWithDeps(status.NewDepsReal())
}

// newStatusCmdWithDeps constructs the `status` subcommand with injectable deps.
func newStatusCmdWithDeps(deps status.Deps) *cobra.Command {
	var flagJSON bool

	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show the running status of the CRENEIN agent stack",
		Long:          "Queries the agent stack and reports the state of all services.\nReturns exit code 0 when all services are running, 1 when degraded, 3 when no installation is found.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runStatus(ctx, cmd, deps, flagJSON)
		},
	}

	cmd.Flags().BoolVar(&flagJSON, "json", false, "emit machine-readable JSON to stdout")
	return cmd
}

// runStatus executes the status logic and renders output in human or JSON mode.
func runStatus(
	ctx context.Context,
	cmd *cobra.Command,
	deps status.Deps,
	jsonMode bool,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// Detect install dir.
	installDir := resolveInstallDir(deps.ReadFile, deps.ReadDir, deps.InstallDir)
	if installDir == "" {
		WriteError(stderr, "no CRENEIN installation found (no docker-compose.yml referencing crenein/c-network-agent-back in . or /root or /home/*/)\n")
		WriteError(stderr, "hint: run `crenein-agent install` to set up the agent stack\n")
		return preflightError(fmt.Errorf("no installation found"))
	}

	// Resolve CLI version — never pass "" to Collect; use "dev" for dev builds.
	cliVersion := build.version
	if cliVersion == "" {
		cliVersion = "dev"
	}

	// Collect status.
	doc, allRunning, collectionWarning := status.Collect(ctx, deps, cliVersion, installDir, stderr)

	// Surface any non-fatal collection warning to stderr before emitting output.
	if collectionWarning != "" {
		WriteError(stderr, "warning: %s\n", collectionWarning)
	}

	// Render.
	if jsonMode {
		mp := NewMachinePresenter(stdout, stderr)
		if err := mp.EmitJSON(doc); err != nil {
			mp.WriteError("error: failed to encode JSON: " + err.Error())
			return opFailureError(err)
		}
	} else {
		renderStatusHuman(stdout, doc)
	}

	if !allRunning {
		return opFailureError(fmt.Errorf("one or more services are not running or are unhealthy"))
	}
	return nil
}

// ─── Human renderer ───────────────────────────────────────────────────────────

// renderStatusHuman writes the human-readable status output to w.
func renderStatusHuman(w io.Writer, doc status.Doc) {
	fmt.Fprintf(w, "Install dir:   %s\n", doc.InstallDir)                                          //nolint:errcheck
	fmt.Fprintf(w, "Agent version: %s (source: %s)\n", doc.Agent.Version, doc.Agent.VersionSource) //nolint:errcheck
	fmt.Fprintf(w, "Mongo:         %s (major: %s)\n", doc.Mongo.Image, doc.Mongo.Major)            //nolint:errcheck

	if doc.Updates != nil {
		fmt.Fprintln(w) //nolint:errcheck
		renderUpdateLine(w, "CLI", doc.Updates.CLIVersion, doc.Updates.CLILatest, doc.Updates.CLIUpdateAvailable)
		renderUpdateLine(w, "Agent", doc.Updates.AgentVersion, doc.Updates.AgentLatest, doc.Updates.AgentUpdateAvailable)
	}

	fmt.Fprintln(w) //nolint:errcheck

	// Aligned service table.
	const hdrFmt = "%-12s  %-40s  %-9s  %-11s  %s\n"
	const rowFmt = "%-12s  %-40s  %-9s  %-11s  %s\n"
	fmt.Fprintf(w, hdrFmt, "SERVICE", "IMAGE", "STATE", "HEALTH", "UPTIME") //nolint:errcheck
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 100))                        //nolint:errcheck
	for _, svc := range doc.Services {
		uptime := ""
		if svc.UptimeSeconds > 0 {
			uptime = formatUptimeHuman(svc.UptimeSeconds)
		} else if svc.StatusText != "" {
			uptime = svc.StatusText
		}
		fmt.Fprintf(w, rowFmt, svc.Name, svc.Image, svc.State, svc.Health, uptime) //nolint:errcheck
	}
}

// renderUpdateLine writes one update-available line for a component.
// Format: "<label>: <version> (<update available: <latest>> | up to date | unknown)"
func renderUpdateLine(w io.Writer, label, version, latest string, updateAvailable bool) {
	if updateAvailable && latest != "" {
		fmt.Fprintf(w, "%-6s %s (update available: %s)\n", label+":", version, latest) //nolint:errcheck
	} else if latest == "" || latest == "unknown" {
		fmt.Fprintf(w, "%-6s %s (update status: unknown)\n", label+":", version) //nolint:errcheck
	} else {
		fmt.Fprintf(w, "%-6s %s (up to date)\n", label+":", version) //nolint:errcheck
	}
}

// formatUptimeHuman converts seconds to a human string like "3d 2h 5m".
func formatUptimeHuman(secs int64) string {
	days := secs / 86400
	secs %= 86400
	hours := secs / 3600
	secs %= 3600
	mins := secs / 60

	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if len(parts) == 0 {
		return "< 1m"
	}
	return strings.Join(parts, " ")
}
