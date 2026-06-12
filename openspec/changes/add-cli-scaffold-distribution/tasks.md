## 1. Repository Scaffold

- [x] 1.1 Initialize the module: `go mod init github.com/PazNicolas/crenein-agent-tui` with `go 1.24`; add `.gitignore` (binaries, `dist/`, `*.tar.gz`, `coverage.out`) and a minimal `README.md` with the one-liner install command.
- [x] 1.2 Create `main.go` declaring `var version = "dev"`, `var commit = "none"`, `var date = "unknown"` and calling `cmd.Execute(version, commit, date)`.
- [x] 1.3 Create `cmd/root.go` with the cobra root command (`Use: "crenein-agent"`, short description, `Version` set from the injected value, `SilenceUsage: true`); verify `go build ./...` produces a runnable binary.
- [x] 1.4 Create `cmd/version.go` with a `version` subcommand printing `crenein-agent version X.Y.Z (commit: abc1234, built: date)`; ensure `--version` and `version` print the same version string; add a unit test asserting the dev default output contains `dev`.

## 2. Release Pipeline (goreleaser)

- [x] 2.1 Write `.goreleaser.yaml`: `project_name: crenein-agent`, single build with `env: [CGO_ENABLED=0]`, `goos: [linux]`, `goarch: [amd64, arm64]`, ldflags `-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}`, tar.gz archives named `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}`, and `checksum: {name_template: checksums.txt, algorithm: sha256}`.
- [x] 2.2 Validate locally with `goreleaser release --snapshot --clean`; confirm `dist/` contains `crenein-agent_*_linux_amd64.tar.gz`, `crenein-agent_*_linux_arm64.tar.gz`, and `checksums.txt`; confirm `file dist/.../crenein-agent` reports a statically linked executable for both arches.
- [x] 2.3 Write `.github/workflows/release.yml`: trigger `on: push: tags: ['v*']`, `permissions: contents: write`, steps checkout (`fetch-depth: 0`), setup-go `1.24`, `goreleaser/goreleaser-action@v6` with `args: release --clean` and `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}` — no other secrets.

## 3. Install Script

- [x] 3.1 Write `install.sh` (bash, `set -euo pipefail`): root check (exit 5), OS/arch detection from `uname -s` / `uname -m` mapping `x86_64→amd64`, `aarch64|arm64→arm64` (unsupported → exit 1), dependency check for `curl`/`tar`/`sha256sum`/`mktemp` (missing → exit 2).
- [x] 3.2 Implement latest-release resolution by following the `https://github.com/PazNicolas/crenein-agent-tui/releases/latest` redirect to extract `vX.Y.Z`; support an optional explicit version argument (`install.sh v0.1.0`); resolution or download failure → exit 3.
- [x] 3.3 Implement download of `crenein-agent_X.Y.Z_linux_${ARCH}.tar.gz` + `checksums.txt` into a `mktemp -d` workdir (cleaned via `trap ... EXIT`), verification with `sha256sum --check --ignore-missing` (mismatch → exit 4, existing binary untouched), extraction, and atomic install via `install -m 755 crenein-agent /usr/local/bin/crenein-agent`; on success print the result of `/usr/local/bin/crenein-agent --version` and exit 0.
- [~] 3.4 Lint the script with `shellcheck install.sh` and `bash -n install.sh`; fix all findings. _(bash -n pasa; shellcheck no instalable en este entorno sin sudo/red — pendiente correrlo, lo cubre además el CI si se agrega, o ejecutar localmente con shellcheck disponible.)_

## 4. CI Quality Gate

- [x] 4.1 Write `.github/workflows/ci.yml`: trigger on `pull_request` and `push` to `main`; steps setup-go `1.24`, `go build ./...`, `go vet ./...`, gofmt guard (`test -z "$(gofmt -l .)"` printing offending files on failure), and `go test ./...`.
- [x] 4.2 Sweep the repository for secrets/client data before making it public (no tokens, hostnames, client names, or `.env` content anywhere, including workflow files and git history of this fresh repo).

## 5. End-to-End Validation

- [ ] 5.1 Open the scaffold PR and confirm `ci.yml` runs and passes on the PR.
- [ ] 5.2 Tag and push `v0.1.0`; confirm `release.yml` publishes a GitHub Release containing exactly `crenein-agent_0.1.0_linux_amd64.tar.gz`, `crenein-agent_0.1.0_linux_arm64.tar.gz`, and `checksums.txt`; confirm a non-tag push to `main` creates no release.
- [ ] 5.3 [VM] On a clean Ubuntu amd64 VM (client-like, fresh provision): run `curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash`; verify `/usr/local/bin/crenein-agent` exists with mode `0755`, owner root, and `crenein-agent --version` prints `0.1.0`.
- [ ] 5.4 [VM] Re-run the one-liner on the same VM to validate idempotency (exit 0, binary replaced); also validate failure paths: without sudo (exit 5) and with a corrupted local checksum test (exit 4, binary untouched).
- [ ] 5.5 [VM] Validate the arm64 binary on a real arm64 instance or via qemu/binfmt (`docker run --platform linux/arm64 ...`): archive extracts and `crenein-agent --version` runs.
