## Context

`crenein-agent-tui` is a brand-new public repository. The master plan requires a static, multi-platform Go binary distributed through GitHub Releases and installed on ~20 client VMs (Ubuntu/Debian, some without AVX, some on arm64 cloud instances) via a `curl | sudo bash` one-liner. The legacy bash scripts live in the private `c-network-agent-back` repo and stay untouched here. This change builds only the delivery skeleton: compile, version, release, install. Every later phase (engine, headless commands, TUI, self-update) ships through the pipeline established now, so the naming and checksum conventions chosen here become a long-term contract — `install.sh` and the future `internal/selfupdate` package must both consume the same release asset layout.

## Goals / Non-Goals

**Goals:**
- A `go build ./...`-compilable repo with a cobra root command and a build-injected version string.
- Reproducible static binaries for `linux/amd64` and `linux/arm64` published automatically on every `v*` tag.
- A SHA256-verified, idempotent `install.sh` that places `/usr/local/bin/crenein-agent` on a client VM.
- A CI gate (`build` + `vet` + `gofmt`) on every PR so the public repo stays healthy from day one.

**Non-Goals:**
- No install/update/doctor logic, no detectors, no `internal/*` packages (→ `add-engine-detectors`).
- No functional subcommands beyond `version` (→ `add-headless-commands`).
- No TUI (→ `add-tui-dashboard`).
- No `self-update` and no `versions.json` manifest generation (→ `add-selfupdate-version-manifest`); however, the asset naming fixed here is designed so self-update can be added without changing the release layout.
- No darwin/windows builds: every client VM is Linux. Adding targets later is a one-line goreleaser change.

## Decisions

### AD-1: Version injected into `main` package variables via ldflags, surfaced through cobra

`main.go` declares `var version = "dev"` (plus `commit` and `date`, both defaulting to `"none"`/`"unknown"`) and passes them into `cmd.Execute(...)`. The cobra root command sets `Version` so that both `crenein-agent --version` and an explicit `version` subcommand print the same string. Releases inject values with goreleaser's standard ldflags: `-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}`.

Rationale: goreleaser injects `main.version`/`main.commit`/`main.date` by default with zero configuration, and keeping the variables in `main` avoids creating an `internal/version` package prematurely — there is no other consumer yet. When `add-selfupdate-version-manifest` needs programmatic access to the running version, the value is already threaded through `cmd`.

Alternative considered: a dedicated `internal/buildinfo` package. Rejected for now — it adds structure with a single caller; cheap to extract later if self-update needs it.

### AD-2: goreleaser as the single build authority, static binaries only

`.goreleaser.yaml` (schema v2) defines one build: `env: [CGO_ENABLED=0]`, `goos: [linux]`, `goarch: [amd64, arm64]`, `mod_timestamp: '{{ .CommitTimestamp }}'` for reproducibility. Archives use the goreleaser default name template `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}` with `project_name: crenein-agent`, producing exactly:

- `crenein-agent_X.Y.Z_linux_amd64.tar.gz`
- `crenein-agent_X.Y.Z_linux_arm64.tar.gz`
- `checksums.txt` (SHA256, `sha256sum`-compatible format: `<hash>  <filename>`)

Each tar.gz contains the binary named `crenein-agent` plus `LICENSE` and `README.md`.

Rationale: `CGO_ENABLED=0` yields fully static binaries that run on any glibc/musl Linux (old Ubuntu, Debian, Alpine-based rescue shells) — the same property the agent backend already relies on in its Dockerfile. One tool owns naming, checksums, and the release upload, so `install.sh` and the future self-updater have a single contract to code against. Local dev never needs goreleaser; `go build` still works because ldflags only override defaults.

### AD-3: Release workflow triggered by `v*` tags, using only `GITHUB_TOKEN`

`.github/workflows/release.yml` runs on `push: tags: ['v*']`, with `permissions: contents: write`, steps: `actions/checkout@v4` (`fetch-depth: 0`, required by goreleaser changelog), `actions/setup-go@v5` (`go-version: '1.24'`), and `goreleaser/goreleaser-action@v6` (`args: release --clean`) with `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}`.

Rationale: tag-driven releases give an explicit, auditable version gate (`git tag v0.1.0 && git push origin v0.1.0` is the entire release procedure). The ephemeral `GITHUB_TOKEN` is sufficient to create releases in the same repo, so no PAT or organization secret ever enters this public repository — a hard requirement.

### AD-4: `install.sh` resolves "latest" via the GitHub redirect, not the API

`install.sh` resolves the latest tag by following the redirect of `https://github.com/PazNicolas/crenein-agent-tui/releases/latest` (`curl -sI ... | parse final Location header → vX.Y.Z`), then downloads `https://github.com/PazNicolas/crenein-agent-tui/releases/download/vX.Y.Z/crenein-agent_X.Y.Z_linux_${ARCH}.tar.gz` and `checksums.txt` from the same tag.

Rationale: the redirect endpoint is not subject to the GitHub REST API rate limit (60 req/h per IP unauthenticated), which matters when 20 clients sit behind shared cloud NAT, and it removes the `jq` dependency. The plan's risk table explicitly calls out API rate limiting.

Behavior contract of `install.sh` (bash, `set -euo pipefail`):

1. MUST run as root (it writes to `/usr/local/bin`); otherwise exit `5` with a message suggesting `sudo`.
2. Detect architecture from `uname -m`: `x86_64 → amd64`, `aarch64|arm64 → arm64`; anything else exits `1`. Non-Linux `uname -s` also exits `1`.
3. Verify required tools exist (`curl`, `tar`, `sha256sum`, `mktemp`); missing tool exits `2`.
4. Download archive + `checksums.txt` into a `mktemp -d` workdir; any download/resolve failure exits `3`.
5. Verify with `sha256sum --check --ignore-missing checksums.txt`; mismatch exits `4` and MUST NOT touch the existing binary.
6. Extract and install atomically: `install -m 755 crenein-agent /usr/local/bin/crenein-agent` (install(1) writes then renames within the same filesystem, so a concurrent invocation never sees a partial binary).
7. Print the installed version by running `/usr/local/bin/crenein-agent --version`; exit `0`.
8. Idempotent: re-running replaces the binary with the latest release regardless of what was there before; the temp workdir is removed via `trap ... EXIT`.

Exit codes are a spec-level contract (see `specs/cli-distribution/spec.md`) so monitoring/automation can distinguish failure modes.

Alternative considered: parsing `/repos/.../releases/latest` JSON via the API. Rejected: rate limits and a `jq`/`python` dependency on a box that may be freshly provisioned.

### AD-5: CI separate from release, minimal but blocking

`.github/workflows/ci.yml` runs on `pull_request` and `push` to `main`: `go build ./...`, `go vet ./...`, and a gofmt guard (`test -z "$(gofmt -l .)"` — fails listing offending files). `go test ./...` is included even though Phase 1 has trivial tests, so the gate exists before the engine code arrives.

Rationale: matches the project verify rules in `openspec/config.yaml`. Keeping CI and release as separate workflows means a broken release pipeline never blocks PR feedback and vice versa.

### AD-6: Module path `github.com/PazNicolas/crenein-agent-tui`

The Go module path matches the public repo. The binary/project name everywhere (cobra `Use`, goreleaser `project_name`, archive contents, install target) is `crenein-agent`.

Rationale: `go install github.com/PazNicolas/crenein-agent-tui@latest` works for developers, while clients only ever see the `crenein-agent` name. The legacy backend module (`github.com/crenein/c-network-agent-go`) is unrelated and stays private.

## Release Flow

```text
developer            GitHub                     goreleaser                client VM
    |                   |                            |                        |
    | git tag v0.1.0    |                            |                        |
    | git push --tags   |                            |                        |
    |------------------>| release.yml (tag v*)       |                        |
    |                   |--------------------------->| build linux/amd64,arm64|
    |                   |                            | tar.gz + checksums.txt |
    |                   |<---------------------------| publish Release v0.1.0 |
    |                   |                            |                        |
    |                   |   curl install.sh | sudo bash                       |
    |                   |<----------------------------------------------------|
    |                   | resolve latest -> v0.1.0                            |
    |                   | download tar.gz + checksums.txt                     |
    |                   |---------------------------------------------------->|
    |                   |                            |  sha256sum --check     |
    |                   |                            |  install -m 755        |
    |                   |                            |  /usr/local/bin/...    |
```

## Risks / Trade-offs

- Public repo leaks internal details → only generic build/install tooling lives here; reviewed before push; no client names, tokens, or infra hostnames. CI/release use no repository secrets at all.
- `curl | sudo bash` is inherently trust-on-first-use → mitigated by serving over HTTPS from `raw.githubusercontent.com` and by SHA256 verification of the binary archive against the release's `checksums.txt`. (Both come from GitHub, so this protects against corruption and mirror tampering, not a full GitHub compromise — accepted, same trust root as the releases themselves.)
- Redirect-based latest resolution could break if GitHub changes URL semantics → low probability; failure mode is exit `3` with a clear message, and the script also accepts an optional explicit version argument (`install.sh v0.1.0`) as escape hatch.
- arm64 binaries shipped untested on real hardware initially → validation task flags a real or emulated (qemu/binfmt) arm64 check before announcing arm64 support.

## Migration Plan

1. Land scaffold + workflows + `install.sh` on `main` via PR (CI green).
2. Tag `v0.1.0`; verify the GitHub Release contains exactly the three expected assets.
3. Run the one-liner on a disposable Ubuntu VM (amd64) and on an arm64 instance or qemu; verify `crenein-agent --version` prints `0.1.0`.
4. Re-run the one-liner on the same VM to verify idempotent upgrade behavior.
5. Only after this validation do sibling changes (`add-engine-detectors`, etc.) start building on the scaffold. Rollback at any point = delete release + tag, revert commits; no client state involved.

## Open Questions

- None. License choice (MIT vs Apache-2.0) is cosmetic for the archive contents; defaulting to MIT unless Nicolás prefers otherwise — does not block implementation.
