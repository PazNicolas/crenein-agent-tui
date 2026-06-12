## Why

The CLI is distributed as a static binary to ~20 client VMs, and there is no mechanism to keep it current: once installed, a stale `crenein-agent` binary stays stale until someone re-runs the `install.sh` one-liner by hand. At the same time, the agent stack itself is still updated blindly (`docker pull crenein/c-network-agent-back:latest`), so neither the client nor the operator knows which version will land, whether the Mongo image pairing is correct for the host CPU, or what changed. Phase 5 of the master plan closes both gaps: a `self-update` subcommand that safely replaces the CLI binary from GitHub Releases, and a `versions.json` manifest that becomes the single source of truth for which agent and CLI versions exist, which Docker images they map to, and what changed in each release.

## What Changes

- Add `internal/selfupdate/` and the `crenein-agent self-update` subcommand: query the GitHub Releases API of `PazNicolas/crenein-agent-tui`, compare semver against the running binary, download the correct asset for the current OS/arch, verify SHA256 against the release `checksums.txt`, and replace the binary atomically (temp file in the same directory + `rename(2)`), preserving `0755` permissions.
- Add self-update flags: `--yes` (non-interactive), `--check` (report only, distinct exit code when an update is available), `--version X.Y.Z` (pin to a specific version, allowing explicit downgrade), and `--force-check` (bypass the local version cache).
- Fail safely: checksum mismatch aborts without touching the current binary; missing write permission produces an actionable message suggesting `sudo`; an interrupted download never leaves a corrupt or partial binary in place.
- Cache release lookups in `~/.crenein/version-cache.json` with a 24h TTL to stay under unauthenticated GitHub API rate limits.
- Define the `versions.json` manifest schema (`agent.latest`, `agent.releases.{X.Y.Z}.{date,image,mongo,notes}`, `cli.latest`, `cli.releases`) and extend the CLI release workflow to generate it and attach it as an asset to every GitHub Release, with the latest release always carrying the current manifest.
- Extend `internal/release/` so `crenein-agent update` and the TUI resolve the target agent version, the AVX-appropriate Mongo image (`mongo."7"` / `mongo."4"`), and the release notes for the update preview from the manifest instead of pulling `:latest` blindly.
- Detect the running agent version via `GET /health` (which will expose `"version"` after the coordinated sibling change) with a fallback to inspecting the Docker image tag/digest, and surface "CLI update available" / "Agent update available" notifications in the CLI and the TUI Status view.

## Capabilities

### New Capabilities
- `cli-selfupdate`: Safe self-replacement of the `crenein-agent` binary from GitHub Releases with semver comparison, mandatory SHA256 verification, atomic swap, and rate-limit-friendly caching.
- `version-manifest`: The `versions.json` schema, its publication as a release asset by the CLI release workflow, and its consumption for agent version resolution, Mongo image selection, update previews, and update-available notifications.

### Modified Capabilities
- None (the update engine consumes the manifest through `internal/release/`; its own behavior is specified in `add-engine-detectors`).

## Impact

- Affected packages: `internal/selfupdate/` (new), `internal/release/` (manifest client, agent version detection), `cmd/` (new `self-update` subcommand, update-available output), `internal/tui/` (Status view notification hooks, update preview notes), `.github/workflows/release.yml` (manifest generation and asset upload).
- Files on the client VM: replaces the CLI binary itself (typically `/usr/local/bin/crenein-agent`) and creates/updates `~/.crenein/version-cache.json`. It does NOT touch `.env`, `docker-compose.yml`, `/data/*`, or certs.
- Coordination with `c-network-agent-back`: REQUIRED. The sibling change `add-agent-health-version` (in `c-network-agent-back/openspec/changes/`) makes `GET /health` return `"version"`. This change consumes that field but MUST degrade gracefully (Docker image tag/digest fallback) while clients still run agent versions that do not expose it. The manifest also encodes the backend image tags (`crenein/c-network-agent-back:X.Y.Z`), so the CLI release workflow must be fed real backend release data.
- Network/API contracts consumed: GitHub Releases API (`api.github.com`, unauthenticated, 60 req/h/IP — hence the cache), GitHub release asset downloads, and the agent backend `GET /health`.
- Rollback plan (high risk — self-update rewrites the binary on client VMs):
  1. The atomic swap itself is the first line of defense: until `rename(2)` succeeds, the old binary is fully intact; any failure before that point leaves the system unchanged.
  2. If a published CLI release turns out to be broken, recovery is `crenein-agent self-update --version <previous>` (explicit downgrade is a first-class feature) or re-running the `install.sh` one-liner pinned to the previous release.
  3. On the publisher side, deleting/yanking the bad GitHub Release (or re-pointing `latest`) stops further propagation; the 24h cache delays pickup, which bounds the blast radius.
  4. If the manifest is malformed, `update` MUST refuse to act on it (parse/validation failure aborts) rather than fall back to blind `:latest`, so a bad manifest cannot corrupt client stacks; republishing a corrected `versions.json` asset on the same release restores service without any client-side action.
  5. The `/health` version field rollback lives in the sibling change; this change's fallback path means reverting `add-agent-health-version` does not break the CLI.
