// Command genmanifest builds the versions.json manifest published as a release
// asset on every tagged release of crenein-agent.
//
// It merges the checked-in agent release seed (internal/release/agent-seed.json)
// with the CLI tag currently being released, then validates the result against
// the manifest schema by round-tripping it through release.ParseManifest — the
// exact validation the CLI runs at consumption time. Any validation failure
// exits non-zero so the release workflow fails BEFORE uploading any asset.
//
// Usage (from the release workflow):
//
//	go run ./tools/genmanifest -tag "$GITHUB_REF_NAME" -notes "$TAG_SUBJECT" -out versions.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

func main() {
	tag := flag.String("tag", "", "CLI release tag being published (e.g. v0.2.0 or 0.2.0); required")
	date := flag.String("date", "", "release date as YYYY-MM-DD (defaults to today, UTC)")
	notes := flag.String("notes", "", "release notes for the CLI version (defaults to \"Release X.Y.Z\")")
	out := flag.String("out", "versions.json", "output path for the generated manifest")
	flag.Parse()

	if strings.TrimSpace(*tag) == "" {
		fmt.Fprintln(os.Stderr, "genmanifest: -tag is required")
		os.Exit(2)
	}

	data, err := generate(*tag, *date, *notes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genmanifest: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genmanifest: write %s: %v\n", *out, err)
		os.Exit(1)
	}

	fmt.Printf("genmanifest: wrote %s (%d bytes)\n", *out, len(data))
}

// generate builds the manifest for the given CLI tag and returns the validated,
// indented JSON bytes. It returns an error when the resulting manifest does not
// satisfy the schema (release.ParseManifest), so the caller can fail the release.
//
// Empty date defaults to today (UTC); empty notes default to "Release X.Y.Z".
func generate(tag, date, notes string) ([]byte, error) {
	version := strings.TrimPrefix(strings.TrimSpace(tag), "v")

	if strings.TrimSpace(date) == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if strings.TrimSpace(notes) == "" {
		notes = "Release " + version
	}

	agentReleases := release.AgentSeed()
	if len(agentReleases) == 0 {
		return nil, fmt.Errorf("agent seed is empty; check internal/release/agent-seed.json")
	}

	m := release.Manifest{
		Agent: release.AgentSection{
			Latest:   latestSemver(keys(agentReleases)),
			Releases: agentReleases,
		},
		CLI: release.CLISection{
			Latest: version,
			Releases: map[string]release.CLIRelease{
				version: {Date: date, Notes: notes},
			},
		},
	}

	// Encode without HTML escaping so notes keep readable characters like "&"
	// in the published asset (json.MarshalIndent would emit "&").
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	data := buf.Bytes()

	// Validate by round-tripping through the real consumer-side parser. This is
	// the schema gate the spec requires: a failure here must fail the release.
	if _, verr := release.ParseManifest(data); verr != nil {
		return nil, fmt.Errorf("generated manifest failed schema validation: %v", verr)
	}

	return data, nil
}

// latestSemver returns the highest version among versions using semver ordering.
func latestSemver(versions []string) string {
	latest := ""
	for _, v := range versions {
		if latest == "" || release.CompareSemver(v, latest) > 0 {
			latest = v
		}
	}
	return latest
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
