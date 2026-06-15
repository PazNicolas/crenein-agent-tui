package main

import (
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

func TestGenerate_ValidManifest(t *testing.T) {
	tests := []struct {
		name        string
		tag         string
		wantCLI     string
		wantInNotes string
	}{
		{name: "tag with v prefix", tag: "v0.2.0", wantCLI: "0.2.0", wantInNotes: "Release 0.2.0"},
		{name: "tag without v prefix", tag: "0.1.0", wantCLI: "0.1.0", wantInNotes: "Release 0.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := generate(tt.tag, "2026-06-14", "")
			if err != nil {
				t.Fatalf("generate() error = %v", err)
			}

			// Must round-trip through the real consumer-side parser.
			m, verr := release.ParseManifest(data)
			if verr != nil {
				t.Fatalf("generated manifest is invalid: %v", verr)
			}

			if m.CLI.Latest != tt.wantCLI {
				t.Errorf("cli.latest = %q, want %q", m.CLI.Latest, tt.wantCLI)
			}
			rel, ok := m.CLI.Releases[tt.wantCLI]
			if !ok {
				t.Fatalf("cli.releases missing entry for %q", tt.wantCLI)
			}
			if !strings.Contains(rel.Notes, tt.wantInNotes) {
				t.Errorf("cli.releases[%q].notes = %q, want to contain %q", tt.wantCLI, rel.Notes, tt.wantInNotes)
			}
			if rel.Date != "2026-06-14" {
				t.Errorf("cli.releases[%q].date = %q, want 2026-06-14", tt.wantCLI, rel.Date)
			}

			// agent.latest must be the highest seeded version (1.8.3) and present.
			if m.Agent.Latest != "1.8.3" {
				t.Errorf("agent.latest = %q, want 1.8.3", m.Agent.Latest)
			}
			if _, ok := m.Agent.Releases[m.Agent.Latest]; !ok {
				t.Errorf("agent.latest %q not present in agent.releases", m.Agent.Latest)
			}
		})
	}
}

func TestGenerate_ExplicitNotes(t *testing.T) {
	data, err := generate("v0.2.0", "2026-06-14", "Adds self-update")
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	m, verr := release.ParseManifest(data)
	if verr != nil {
		t.Fatalf("invalid manifest: %v", verr)
	}
	if got := m.CLI.Releases["0.2.0"].Notes; got != "Adds self-update" {
		t.Errorf("notes = %q, want %q", got, "Adds self-update")
	}
}

func TestLatestSemver(t *testing.T) {
	got := latestSemver([]string{"1.6.1", "1.8.0", "1.8.3", "1.8.2"})
	if got != "1.8.3" {
		t.Errorf("latestSemver = %q, want 1.8.3", got)
	}
}
