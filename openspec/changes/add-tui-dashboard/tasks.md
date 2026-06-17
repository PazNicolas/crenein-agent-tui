## 1. TUI Foundation

- [x] 1.1 Add dependencies: `bubbletea`, `lipgloss`, `bubbles`, and `teatest` (test-only); verify `go build ./...` still produces a static binary.
- [x] 1.2 Create `internal/tui/styles` with the shared palette, status glyphs with text fallbacks (`✅/[OK]`, `⚠️/[WARN]`, `❌/[FAIL]`), and `NO_COLOR`/ASCII-profile handling.
- [x] 1.3 Implement the root model: view registry, navigation stack, global key map (`s`, `i`, `u`, `d`, `l`, `esc`, `q`, `ctrl+c`), header and footer chrome, `tea.WindowSizeMsg` propagation.
- [x] 1.4 Implement the engine event adapter: listen-loop `tea.Cmd` that converts engine progress channel events into `stepStartedMsg`/`stepProgressMsg`/`stepDoneMsg`/`stepFailedMsg`/`operationFinishedMsg`, plus a fake engine emitting scripted sequences for tests.
- [x] 1.5 Wire `cmd`: no arguments + TTY + usable `TERM` → run the dashboard; any subcommand → existing headless path untouched.

## 2. Status View (home)

- [x] 2.1 Implement the service table (agent, frontend, mongodb, influxdb, redis): status, version, uptime, sourced from the same engine status call used by `crenein-agent status`.
- [x] 2.2 Implement the versions panel: CLI version, agent version (from `/health` `version` field when present, image tag fallback).
- [x] 2.3 Implement update-available indicators with background manifest fetch via `internal/release` and the three states: up-to-date, update available, version check unavailable.
- [x] 2.4 Implement periodic refresh tick, manual refresh key (`r`), and the not-installed state that points to the Install wizard.
- [x] 2.5 Implement the ≥100-column side-by-side layout and the <100-column stacked layout.

## 3. Install Wizard View

- [x] 3.1 Implement step 1 — system checks: run engine detect (OS, AVX, Docker, compose v1/v2, disk, connectivity) with live per-check results; block forward navigation on fatal failures with the fix suggestion shown.
- [x] 3.2 Implement the existing-installation guard: if an installation is detected, show its location and versions and do not allow proceeding.
- [x] 3.3 Implement step 2 — configuration form: credentials/ports/paths inputs pre-filled with engine defaults (generated 32-char passwords, ports 8000/8443/80/443/8086, `/data` paths) and per-field validation.
- [x] 3.4 Implement step 3 — preview: read-only summary of planned actions (packages, compose file with chosen Mongo image, `.env` keys without secret values, directories, certs) requiring explicit confirmation.
- [x] 3.5 Implement step 4 — execution: live step list driven by engine events, spinner on the active step, failure state with the engine error and fix suggestion.
- [x] 3.6 Implement step 5 — access summary with parity to `install-agent.sh`: backend/frontend/InfluxDB URLs, admin credentials, data and cert paths.

## 4. Update Wizard View

- [x] 4.1 Implement the version preview screen: current → available version with release notes from the manifest; handle already-up-to-date and manifest-unavailable states.
- [x] 4.2 Implement explicit confirmation listing what will and will not be touched (databases are not restarted).
- [x] 4.3 Implement live progress for backup → pull → recreate → health, driven by engine events.
- [x] 4.4 Implement the result screen: success summary, or failure with the automatic rollback steps and their outcome visible.

## 5. Doctor View

- [x] 5.1 Implement the live check list: each engine doctor check renders pending → running → ✅/⚠️/❌ as events arrive, with a final summary line.
- [x] 5.2 Implement check selection: detail pane with the check output and its fix suggestion.
- [x] 5.3 Implement re-run (`r`) restarting the full doctor run and resetting states.

## 6. Logs View

- [x] 6.1 Implement live follow of compose logs through the same engine/dockerx streaming call as `crenein-agent logs`, rendered in a viewport.
- [x] 6.2 Implement service filter cycling (all → agent → frontend → mongodb → influxdb → redis).
- [x] 6.3 Implement pause/resume (`space`) with scrollback while paused and auto-follow on resume.
- [x] 6.4 Implement configurable tail size and buffer cap so memory stays bounded.

## 7. Degradation And Resilience

- [x] 7.1 Implement the pre-start gate: non-TTY or `TERM=dumb`/empty prints the headless-subcommand notice and exits `0` without starting bubbletea.
- [x] 7.2 Implement the "terminal too small" screen for sizes under 80x24, recovering on resize.
- [x] 7.3 Verify `NO_COLOR` and ASCII color profiles render monochrome with text fallbacks across all five views.
- [ ] 7.4 Validate manually on client-like terminals: xterm over SSH, `screen`, a cloud web console, and a no-AVX VM. **[requires real client-like VM]**

## 8. Tests (teatest)

- [x] 8.1 Status view: golden file at 80x24 (installed and not-installed states) plus indicator states via fake release client.
- [x] 8.2 Install wizard: simulated full run with the fake engine (checks → form → preview → execution → summary) and the existing-installation guard.
- [x] 8.3 Update wizard: simulated success run and simulated health-check failure showing rollback.
- [x] 8.4 Doctor view: scripted check sequence with a warning and a failure; selection detail; re-run.
- [x] 8.5 Logs view: scripted log stream; filter, pause/scroll, resume.
- [x] 8.6 Navigation and degradation: global keys, quit confirmation during a running operation, too-small screen, no-color goldens.
      — Added 5 root-level mono goldens (TestRootModelGoldenMono_{Status,Install,Update,Doctor,Logs}) in internal/tui/root_golden_test.go.
- [x] 8.7 Run `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`.
      — All four gates pass: gofmt clean, vet clean, build clean, all tests green.
