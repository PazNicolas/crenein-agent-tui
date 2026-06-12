## ADDED Requirements

### Requirement: Repository scaffold builds a runnable binary
The repository SHALL contain a Go module at path `github.com/PazNicolas/crenein-agent-tui` (Go 1.24) with a `main.go` entry point and a `cmd/` package exposing a cobra root command named `crenein-agent`, such that `go build ./...` succeeds from a clean checkout.

#### Scenario: Clean build from checkout
- **GIVEN** a clean clone of the repository with Go 1.24 installed
- **WHEN** the developer runs `go build -o crenein-agent .`
- **THEN** the command MUST exit `0`
- **AND** the produced `crenein-agent` binary MUST be executable

#### Scenario: Root command without arguments
- **GIVEN** a locally built `crenein-agent` binary
- **WHEN** the user runs `crenein-agent` with no arguments
- **THEN** the process MUST exit `0`
- **AND** it MUST print help/usage text naming the binary `crenein-agent` (functional subcommands and the TUI are out of scope for this change)

#### Scenario: Unknown subcommand is rejected
- **GIVEN** a locally built `crenein-agent` binary
- **WHEN** the user runs `crenein-agent bogus`
- **THEN** the process MUST exit with a non-zero code
- **AND** stderr MUST indicate the command is unknown

### Requirement: Version is injected at build time and reported by the CLI
The CLI SHALL expose its version through both `crenein-agent --version` and `crenein-agent version`. The version value MUST come from a `main` package variable defaulting to `"dev"` and overridden at release build time via `-ldflags "-X main.version={{.Version}}"` (with `main.commit` and `main.date` injected alongside).

#### Scenario: Dev build reports dev version
- **GIVEN** a binary built with plain `go build` (no ldflags)
- **WHEN** the user runs `crenein-agent --version`
- **THEN** the process MUST exit `0`
- **AND** the output MUST contain the string `dev`

#### Scenario: Release build reports the tagged version
- **GIVEN** a binary produced by goreleaser for tag `v0.1.0`
- **WHEN** the user runs `crenein-agent --version`
- **THEN** the output MUST contain `0.1.0`
- **AND** it MUST NOT contain `dev`

#### Scenario: Version subcommand matches the flag
- **GIVEN** any build of the binary
- **WHEN** the user runs `crenein-agent version`
- **THEN** the process MUST exit `0`
- **AND** the version string printed MUST equal the version printed by `crenein-agent --version`
- **AND** the output SHOULD additionally include the commit hash and build date when they were injected

### Requirement: Release builds are static Linux binaries for amd64 and arm64
The `.goreleaser.yaml` configuration SHALL build the binary with `CGO_ENABLED=0` for exactly two targets: `linux/amd64` and `linux/arm64`. Release binaries MUST be statically linked so they run on any Linux distribution used by client VMs without shared-library dependencies.

#### Scenario: Snapshot build produces both architectures
- **GIVEN** the repository with its `.goreleaser.yaml`
- **WHEN** the developer runs `goreleaser release --snapshot --clean`
- **THEN** the command MUST exit `0`
- **AND** `dist/` MUST contain one tar.gz archive for `linux_amd64` and one for `linux_arm64`

#### Scenario: Binary is statically linked
- **GIVEN** the extracted `crenein-agent` binary from the `linux_amd64` archive
- **WHEN** the developer inspects it with `file` (or `ldd`)
- **THEN** it MUST be reported as statically linked (no dynamic interpreter)

#### Scenario: No other platforms are published
- **GIVEN** the goreleaser configuration
- **WHEN** a release is built
- **THEN** no darwin or windows artifacts MUST be produced (Linux-only client fleet; other targets are deferred deliberately)

### Requirement: Tag push publishes a GitHub Release with checksummed archives
The workflow `.github/workflows/release.yml` SHALL trigger on pushed tags matching `v*`, run goreleaser, and publish a GitHub Release on `PazNicolas/crenein-agent-tui` containing exactly: `crenein-agent_X.Y.Z_linux_amd64.tar.gz`, `crenein-agent_X.Y.Z_linux_arm64.tar.gz`, and `checksums.txt` with SHA256 sums in `sha256sum`-compatible format. Each archive MUST contain the binary named `crenein-agent`.

#### Scenario: Pushing a version tag creates the release
- **GIVEN** the workflow exists on the default branch
- **WHEN** the maintainer runs `git tag v0.1.0 && git push origin v0.1.0`
- **THEN** the `release.yml` workflow MUST run and succeed
- **AND** a GitHub Release for tag `v0.1.0` MUST exist with assets `crenein-agent_0.1.0_linux_amd64.tar.gz`, `crenein-agent_0.1.0_linux_arm64.tar.gz`, and `checksums.txt`

#### Scenario: Checksums match the published archives
- **GIVEN** the published release assets downloaded to one directory
- **WHEN** the user runs `sha256sum --check --ignore-missing checksums.txt`
- **THEN** every downloaded archive MUST be reported as `OK`

#### Scenario: Non-tag pushes do not release
- **GIVEN** the workflow configuration
- **WHEN** a commit is pushed to `main` without a `v*` tag
- **THEN** the release workflow MUST NOT run
- **AND** no GitHub Release MUST be created

#### Scenario: Release uses only the built-in token
- **GIVEN** the `release.yml` workflow definition
- **WHEN** it is reviewed
- **THEN** the only credential referenced MUST be `${{ secrets.GITHUB_TOKEN }}`
- **AND** no personal access token or organization secret MUST be required

### Requirement: install.sh installs the latest release via one-liner
The repository SHALL provide `install.sh` at its root such that `curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash` installs the latest released binary to `/usr/local/bin/crenein-agent` with mode `0755` and owner `root`. The script MUST detect the architecture from `uname -m` (`x86_64` → `amd64`, `aarch64`/`arm64` → `arm64`), MUST resolve the latest version without using the rate-limited GitHub REST API (release-redirect resolution), and MAY accept an explicit version argument (e.g. `install.sh v0.1.0`).

#### Scenario: Fresh install on amd64
- **GIVEN** a clean Ubuntu amd64 VM with `curl`, `tar`, and `sha256sum` available and no prior installation
- **WHEN** the operator runs the one-liner with sudo
- **THEN** the script MUST exit `0`
- **AND** `/usr/local/bin/crenein-agent` MUST exist with permissions `0755`
- **AND** `crenein-agent --version` MUST print the latest released version

#### Scenario: Fresh install on arm64
- **GIVEN** a clean arm64 Linux VM
- **WHEN** the operator runs the one-liner with sudo
- **THEN** the script MUST download the `crenein-agent_X.Y.Z_linux_arm64.tar.gz` asset
- **AND** the installed binary MUST execute successfully on arm64

#### Scenario: Re-running updates idempotently
- **GIVEN** `/usr/local/bin/crenein-agent` already exists from a previous run (any version)
- **WHEN** the operator re-runs the one-liner
- **THEN** the script MUST exit `0`
- **AND** the binary MUST be replaced atomically with the latest release (no window in which the path is missing or partially written)

#### Scenario: Explicit version install
- **GIVEN** a published release `v0.1.0` that is not the latest
- **WHEN** the operator runs `sudo bash install.sh v0.1.0`
- **THEN** the script MUST install exactly version `0.1.0`

### Requirement: install.sh verifies integrity and fails safely with distinct exit codes
`install.sh` SHALL verify the downloaded archive's SHA256 against the release's `checksums.txt` before installing, and SHALL use distinct exit codes for each failure class: `0` success, `1` unsupported OS/architecture, `2` missing required dependency (`curl`, `tar`, `sha256sum`, `mktemp`), `3` version-resolution or download failure, `4` checksum mismatch, `5` not running as root. On any failure the script MUST NOT modify an existing `/usr/local/bin/crenein-agent` and MUST clean up its temporary working directory.

#### Scenario: Checksum mismatch aborts without touching the binary
- **GIVEN** an existing installed binary and a downloaded archive whose SHA256 does not match `checksums.txt`
- **WHEN** verification runs
- **THEN** the script MUST exit `4` with an error message on stderr
- **AND** the previously installed `/usr/local/bin/crenein-agent` MUST remain byte-identical

#### Scenario: Unsupported architecture
- **GIVEN** a host where `uname -m` returns a value other than `x86_64`, `aarch64`, or `arm64`
- **WHEN** the script runs
- **THEN** it MUST exit `1`
- **AND** the error message MUST name the unsupported architecture

#### Scenario: Not run as root
- **GIVEN** the script is executed by a non-root user without sudo
- **WHEN** the privilege check runs
- **THEN** the script MUST exit `5` before any download occurs
- **AND** the message MUST suggest re-running with `sudo`

#### Scenario: Download failure
- **GIVEN** the release asset cannot be downloaded (network error, deleted release, HTTP error)
- **WHEN** the script attempts resolution or download
- **THEN** it MUST exit `3`
- **AND** no file MUST be installed and the temporary directory MUST be removed

### Requirement: CI validates build quality on pull requests
The workflow `.github/workflows/ci.yml` SHALL run on every pull request and on pushes to `main`, executing at minimum `go build ./...`, `go vet ./...`, and a gofmt check that fails when `gofmt -l .` reports any file. The CI job MUST fail (blocking merge) if any step fails.

#### Scenario: Clean PR passes
- **GIVEN** a pull request whose code builds, passes `go vet`, and is gofmt-formatted
- **WHEN** the CI workflow runs
- **THEN** all steps MUST succeed and the check MUST be reported green on the PR

#### Scenario: Unformatted code blocks the PR
- **GIVEN** a pull request containing a Go file not formatted with gofmt
- **WHEN** the CI workflow runs
- **THEN** the gofmt step MUST fail
- **AND** the failing output MUST list the offending file paths

#### Scenario: Vet error blocks the PR
- **GIVEN** a pull request introducing code that fails `go vet ./...`
- **WHEN** the CI workflow runs
- **THEN** the job MUST fail with the vet diagnostics visible in the log

### Requirement: The public repository contains no secrets or client data
As a public repository, `PazNicolas/crenein-agent-tui` SHALL NOT contain any credential, API token, password, private hostname, or client-identifying data in code, configuration, workflows, or documentation. Credentials used by the agent stack are generated at install time on the client VM (out of scope here) and MUST never be committed.

#### Scenario: No hardcoded credentials in the tree
- **GIVEN** the full repository tree at any commit of this change
- **WHEN** it is scanned for secrets (e.g. tokens, `PASSWORD=`, private URLs, the legacy hardcoded InfluxDB token)
- **THEN** no match MUST be found
- **AND** the legacy bash scripts' hardcoded InfluxDB token MUST NOT be ported into this repository

#### Scenario: Workflows reference no external secrets
- **GIVEN** all files under `.github/workflows/`
- **WHEN** they are reviewed for `secrets.*` references
- **THEN** the only reference MUST be the ephemeral `secrets.GITHUB_TOKEN`
