> **Estado (2026-06-12): capa de librería implementada.** `internal/release` (manifest schema/validación/seed + cliente con cache + resolución + detección de versión del agente) y `internal/selfupdate` (swap atómico verificado) están hechos y testeados — esto destraba `add-headless-commands` (necesita `internal/release`).
>
> **Actualización (2026-06-14): núcleo desbloqueable implementado.** Hecho: 1.4 (generación + validación de `versions.json` en el workflow vía `tools/genmanifest`, asset por `release.extra_files`) y sección 4 salvo 4.4 (`cmd/self-update` completo con `--yes`/`--check`/`--version`/`--force-check`, exit codes 0/10/1, mensajes, tests). El adaptador `release.GitHubReleaseSource` (que `internal/selfupdate` requería) también quedó implementado.
>
> **Aún diferido** (wirean en superficies que no existen todavía): 4.4 (integración en `crenein-agent status` — `status` es subcomando de `add-headless-commands`), sección 5 (wire del manifest en `crenein-agent update` + indicadores en la TUI Status view — dependen de `add-headless-commands` y `add-tui-dashboard`), 1.5 + 6.2–6.4 (verificación en release real + VM cliente, validación manual). Se completan después de headless/TUI.

## 1. Version Manifest — Schema And Publication

- [x] 1.1 Define the `versions.json` schema (Go structs + JSON tags in `internal/release/manifest.go`) matching the design exactly: `agent.latest`, `agent.releases.{X.Y.Z}.{date,image,mongo,notes}`, `cli.latest`, `cli.releases`.
- [x] 1.2 Implement manifest validation (semver keys, non-empty `image`, `mongo` map contains `"7"` and `"4"`, `latest` exists in `releases`); malformed manifest returns a structured error, never a partial result.
- [x] 1.3 Add the agent-release seed data file in this repo (current backend history: 1.8.3, 1.8.2, 1.8.1, 1.8.0, 1.6.1) used by the workflow to build the `agent` section.
- [x] 1.4 Extend `.github/workflows/release.yml` to generate `versions.json` on each tag (merge seed data + CLI tag being released), validate it, and upload it as a release asset; a validation failure MUST fail the release. _(Generador `tools/genmanifest` reusa `release.ParseManifest` para validar — exit ≠0 falla el job antes de goreleaser; goreleaser sube el asset vía `release.extra_files`. `agent.latest` se computa como el mayor semver del seed; `cli.releases` lleva solo el tag actual en v1.)_
- [ ] 1.5 Verify the manifest on the latest release is fetchable via the `releases/latest` asset URL without knowing the tag.

## 2. Release Client — Manifest Consumption (internal/release/)

- [x] 2.1 Implement the GitHub Releases API client (latest release metadata, release by tag, asset download URLs) behind an interface, `context.Context`-first, no network in unit tests.
- [x] 2.2 Implement the local cache: read/write `~/.crenein/version-cache.json` (`0600`, dir `0700`), 24h TTL via `fetched_at`, corrupt cache treated as absent, every live fetch rewrites it.
- [x] 2.3 Implement manifest fetch-and-parse from the latest release asset, served through the cache unless bypassed.
- [x] 2.4 Implement resolution helpers: target agent version (explicit pin or `agent.latest`), fully qualified agent image, Mongo image by AVX family (`mongo."7"` / `mongo."4"`), and release notes for a given version.
- [x] 2.5 Implement running-agent version detection: `GET /health` (`https -k` then `http` on `localhost:8000`) reading `"version"`; fallback to Docker image tag/digest via `internal/dockerx`; both unavailable → `unknown`.
- [x] 2.6 Implement update-available computation for CLI and agent (semver compare local vs manifest; suppressed when the local version is `unknown`).
- [x] 2.7 Unit tests: cache TTL/bypass/corruption, manifest validation failures, resolution helpers, `/health`-vs-Docker fallback chain, `unknown` suppression.

## 3. Self-Update Engine (internal/selfupdate/)

- [x] 3.1 Implement binary path resolution (`os.Executable` + `filepath.EvalSymlinks`) and the early writability probe of the target file and its directory.
- [x] 3.2 Implement asset selection for the current `GOOS/GOARCH` and streamed download to a temp file in the same directory as the target (cleanup of stale temp files; `defer` removal on every failure path).
- [x] 3.3 Implement SHA256 verification against the release `checksums.txt` entry for the exact asset name; mismatch or missing entry aborts before any swap.
- [x] 3.4 Implement the atomic swap: `chmod 0755` the verified temp file, then `os.Rename` over the target; surface a structured result (`from`, `to`, action taken).
- [x] 3.5 Implement semver decision logic: newer → update; equal/older → "already up to date" (exit 0); explicit `--version` pin allows downgrade and bypasses "already up to date" only when versions differ.
- [x] 3.6 Unit tests with fakes (no network, temp-dir targets): happy path, checksum mismatch leaves target byte-identical, interrupted download leaves no temp residue, permission-denied probe, downgrade pin, same-version no-op.

## 4. CLI Subcommand (cmd/)

- [x] 4.1 Add `crenein-agent self-update` (cobra) wiring `internal/selfupdate` + `internal/release`, with interactive confirmation showing `current → target` and release notes.
- [x] 4.2 Implement flags: `--yes`, `--check` (no modification; exit `0` up to date, `10` update available, `1` error), `--version X.Y.Z`, `--force-check`.
- [x] 4.3 Implement user-facing messages: `updated X → Y`, `already up to date (X)`, permission error with `sudo crenein-agent self-update` suggestion, checksum-abort explanation.
- [x] 4.4 Surface agent + CLI update-available status in `crenein-agent status` (human and `--json` output includes `cli_version`, `cli_latest`, `agent_version`, `agent_latest`, `update_available` booleans).
- [x] 4.5 Integration-style tests for exit codes and output contracts (`--check` codes; `--json` NOT implemented — spec only requires `--json` for `crenein-agent status` (task 4.4), not for `self-update`).

## 5. Update Flow And TUI Integration

- [x] 5.1 Wire `crenein-agent update` to resolve the target agent version, image tag, Mongo image, and notes from the manifest (replacing blind `:latest`) and pass them to the engine (`add-engine-detectors` owns execution).
- [x] 5.2 Show release notes from the manifest in the update preview/confirmation (headless and TUI).
- [x] 5.3 Add "CLI update available" / "Agent update available" indicators to the TUI Status view (coordination with `add-tui-dashboard`), driven only by cached/TTL-respecting checks, with a "last checked" timestamp. _(internal/tui/status_view.go: `updateIndicator` + "Last checked" line via `lastCheckedText()`, surfacing `UpdatesInfo.LastChecked`)_
- [x] 5.4 Ensure TUI rendering never triggers uncached GitHub API calls. _(verificado: `View()` es puro; las comprobaciones van por `FetchUpdatesInfo`→`FetchManifest(ctx, false)`, que respeta el TTL de 24h en disco)_

## 6. Validation And Release

- [x] 6.1 Run `go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`.
- [ ] 6.2 Cut a pre-release tag and verify `versions.json` is attached and valid on the GitHub Release.
- [ ] 6.3 Validate on a real client-like VM (must include: non-root user, and a backend WITHOUT `/health` version to exercise the Docker fallback): full self-update, `--check` exit codes, tampered-asset checksum abort, permission-denied message, pinned downgrade and recovery.
- [ ] 6.4 Document coordination status with `c-network-agent-back` change `add-agent-health-version` (deployed where, fallback behavior confirmed) and record the rollback procedure (yank release / `self-update --version <previous>`).
