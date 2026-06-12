## Why

CRENEIN currently maintains ~20 client installations through two duplicated ~700-line bash scripts (`install-agent.sh`, `install-agent-mongo4.sh`) and a blind-to-`:latest` `update-agent.sh`. The replacement is a compiled Go CLI/TUI (`crenein-agent`), but no repository scaffold, build pipeline, or distribution channel exists yet. Nothing else in the plan (engine porting, headless subcommands, TUI, self-update) can start or be validated on a real client VM until a versioned static binary can be built, published to GitHub Releases, and installed on a client VM with a single `curl | sudo bash` one-liner. This change is Phase 1 of the master plan: scaffold + distribution.

## What Changes

- Create the Go repository scaffold for `github.com/PazNicolas/crenein-agent-tui`: `go.mod` (Go 1.24), `main.go`, and a `cmd/` package with a cobra root command named `crenein-agent`.
- Implement `crenein-agent --version` and `crenein-agent version`, printing a version string injected at build time via `-ldflags` (`dev` for local builds, `X.Y.Z` for releases).
- Add `.goreleaser.yaml` producing statically linked binaries (`CGO_ENABLED=0`) for `linux/amd64` and `linux/arm64`, packaged as `crenein-agent_X.Y.Z_linux_{amd64,arm64}.tar.gz` plus a `checksums.txt` with SHA256 sums.
- Add `.github/workflows/release.yml`: triggered by tags matching `v*`, runs goreleaser, and publishes a GitHub Release with the archives and checksums using only the built-in `GITHUB_TOKEN`.
- Add `install.sh` at the repo root, usable as `curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash`: detects architecture (amd64/arm64), resolves the latest release, downloads the matching archive, verifies its SHA256 against `checksums.txt`, and installs the binary to `/usr/local/bin/crenein-agent` with mode `0755`. Re-running it upgrades in place (idempotent).
- Add `.github/workflows/ci.yml`: `go build ./...`, `go vet ./...`, and `gofmt -l .` on pull requests and pushes to `main`.
- Repository hygiene for a public repo: `.gitignore` for Go/goreleaser artifacts, no secrets or client data anywhere in the tree.

## Capabilities

### New Capabilities
- `cli-distribution`: Defines how the `crenein-agent` binary is built (static, versioned via ldflags), released (GitHub Releases with SHA256 checksums on `v*` tags), and installed on client VMs (`install.sh` one-liner into `/usr/local/bin/crenein-agent`), plus the CI quality gate for the public repository.

### Modified Capabilities
- None. This is the first change in a new repository.

## Impact

- Affected packages: `main.go` (new), `cmd/` (new — root command and version wiring). The planned `internal/*` packages (`engine`, `detect`, `compose`, `dockerx`, `release`, `selfupdate`, `tui`) are NOT created in this change.
- Affected non-Go files: `go.mod`, `go.sum`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `.github/workflows/ci.yml`, `install.sh`, `.gitignore`, `README.md`.
- Client VM footprint: the only file this change touches on a client VM is `/usr/local/bin/crenein-agent` (written by `install.sh` with mode `0755`, owner root). It does NOT touch `.env`, `docker-compose.yml`, `/data/*`, or certs — those remain owned by the legacy bash scripts until later changes.
- Coordination with `c-network-agent-back`: none required for this change. The `/health` version contract and `versions.json` manifest belong to `add-selfupdate-version-manifest`.
- Public repo exposure: the repository is public; this change MUST NOT introduce any token, credential, or client-identifying data. The release workflow uses only the ephemeral `GITHUB_TOKEN` provided by Actions.
- Non-goals (deferred to sibling changes): install/update/doctor engine logic and detectors (`add-engine-detectors`), functional headless subcommands (`add-headless-commands`), the bubbletea TUI dashboard (`add-tui-dashboard`), and `self-update` plus the `versions.json` manifest (`add-selfupdate-version-manifest`).
- Rollback plan: low-risk — no production system depends on the CLI yet. To roll back a bad release, delete the GitHub Release and its `vX.Y.Z` tag (`gh release delete vX.Y.Z --cleanup-tag`); `install.sh` resolves "latest" dynamically, so the one-liner immediately serves the previous release again. A client VM with a bad binary recovers by re-running the one-liner. To roll back the scaffold itself, revert the commits — no client state is affected.
