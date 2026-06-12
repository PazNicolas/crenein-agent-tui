// Command crenein-agent is the CRENEIN C-Network agent CLI/TUI.
//
// It is distributed as a single static binary to client VMs and replaces the
// legacy install/update bash scripts. The version, commit, and build date are
// injected at release time via -ldflags; local builds report "dev".
package main

import "github.com/PazNicolas/crenein-agent-tui/cmd"

// Build-time metadata. Overridden by goreleaser via:
//
//	-ldflags "-X main.version=... -X main.commit=... -X main.date=..."
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.Execute(version, commit, date)
}
