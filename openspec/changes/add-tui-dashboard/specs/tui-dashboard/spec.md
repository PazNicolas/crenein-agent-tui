## ADDED Requirements

### Requirement: Bare invocation opens the dashboard, subcommands stay headless
Running `crenein-agent` with no arguments on an interactive terminal SHALL open the full-screen TUI dashboard as the primary mode. Running `crenein-agent` with any subcommand (`install`, `update`, `doctor`, `status`, `logs`, `self-update`, `rollback`) SHALL execute the existing headless behavior and MUST NOT start the TUI.

#### Scenario: Open dashboard on a TTY
- **GIVEN** stdout is a TTY with `TERM=xterm-256color` and size at least 80x24
- **WHEN** the user runs `crenein-agent` with no arguments
- **THEN** the TUI MUST start in the alternate screen buffer
- **AND** the Status view MUST be the initial view
- **AND** a header with the application name and CLI version and a footer with key hints MUST be visible

#### Scenario: Subcommand bypasses the TUI
- **GIVEN** any terminal, interactive or not
- **WHEN** the user runs `crenein-agent doctor --json`
- **THEN** the command MUST run headless and produce the JSON output defined by the headless commands capability
- **AND** no TUI screen MUST be drawn

### Requirement: Graceful degradation on unsupported terminals
The dashboard SHALL detect terminal capabilities before starting and SHALL degrade without crashing: unsupported terminals get a plain-text notice pointing to the headless subcommands, undersized terminals get a resize prompt, and `NO_COLOR` disables color while preserving meaning through text.

#### Scenario: Non-TTY or TERM=dumb points to headless mode
- **GIVEN** stdout is not a TTY, or `TERM` is `dumb` or empty
- **WHEN** the user runs `crenein-agent` with no arguments
- **THEN** the process MUST NOT start the TUI and MUST NOT crash
- **AND** it MUST print a plain-text notice listing the headless subcommands (`crenein-agent status`, `install`, `update`, `doctor`, `logs`, `self-update`)
- **AND** it MUST exit with code `0`

#### Scenario: Terminal smaller than 80x24
- **GIVEN** the TUI is running and the terminal reports a size smaller than 80 columns or 24 rows
- **WHEN** a `tea.WindowSizeMsg` with the undersized dimensions is received
- **THEN** the TUI MUST render a screen stating the current size and the required minimum 80x24
- **AND** when the terminal is resized to at least 80x24 the previously active view MUST be restored with its state intact

#### Scenario: NO_COLOR renders monochrome with text fallbacks
- **GIVEN** the environment variable `NO_COLOR` is set to any value
- **WHEN** the dashboard renders any view
- **THEN** the output MUST NOT contain ANSI color sequences
- **AND** check and service states MUST remain distinguishable through text markers (`[OK]`, `[WARN]`, `[FAIL]`) so meaning never depends on color alone

### Requirement: Global keyboard navigation
The dashboard SHALL provide a global key map handled by the root model: `s` Status, `i` Install wizard, `u` Update wizard, `d` Doctor, `l` Logs, `esc` back to Status, `q` or `ctrl+c` quit. While an engine operation is running, navigation away from its view MUST be blocked and quitting MUST require confirmation.

#### Scenario: Navigate between views
- **GIVEN** the Status view is active and no operation is running
- **WHEN** the user presses `d`
- **THEN** the Doctor view MUST become active
- **AND** pressing `esc` MUST return to the Status view

#### Scenario: Quit from idle
- **GIVEN** no engine operation is running
- **WHEN** the user presses `q`
- **THEN** the TUI MUST exit, restore the terminal from the alternate screen, and return exit code `0`

#### Scenario: Quit confirmation during a running operation
- **GIVEN** an install or update operation is in progress
- **WHEN** the user presses `q` or `ctrl+c`
- **THEN** the TUI MUST show a confirmation prompt explaining that the operation will be cancelled
- **AND** confirming MUST cancel the operation's `context.Context` before exiting
- **AND** declining MUST return to the running view with the operation unaffected

### Requirement: Status view shows live services, versions, and update indicators
The Status view SHALL display, using the same engine status call as `crenein-agent status`: one row per compose service (`agent`, `frontend`, `mongodb`, `influxdb`, `redis`) with state, version, and uptime; the CLI version; the agent version; and update-available indicators for both CLI and agent fed by the `versions.json` manifest from `internal/release`.

#### Scenario: Installed stack renders service table
- **GIVEN** a detected installation directory containing `docker-compose.yml` and `.env`
- **AND** all five services are running
- **WHEN** the Status view loads
- **THEN** it MUST show one row per service (`agent`, `frontend`, `mongodb`, `influxdb`, `redis`) with state `running`, the image version (e.g. `crenein/c-network-agent-back:1.8.3`, `mongo:4.4` or `mongodb/mongodb-community-server:7.0-ubuntu2204`), and uptime
- **AND** it MUST show the CLI version and the agent version (from the `version` field of the backend's public root `GET /health` when available; legacy agents without the endpoint answer 404, in which case the version MUST come from the image tag or digest, or render as `unknown`)
- **AND** pressing `r` MUST refresh the data immediately

#### Scenario: Update available indicator
- **GIVEN** the manifest reports agent `latest = 1.8.4` and the installed agent is `1.8.3`
- **WHEN** the background manifest fetch resolves
- **THEN** the Status view MUST show an `update available → 1.8.4` indicator next to the agent version
- **AND** the indicator MUST hint that the Update wizard (`u`) performs the update

#### Scenario: Manifest unavailable degrades to unknown
- **GIVEN** the manifest cannot be fetched (no network, GitHub unavailable, or the manifest release does not exist yet)
- **WHEN** the Status view renders
- **THEN** version and service data MUST still be displayed without delay
- **AND** the update indicators MUST show `version check unavailable` instead of an error screen

#### Scenario: No installation present
- **GIVEN** no installation directory is found (no `docker-compose.yml` with the agent image in the search paths)
- **WHEN** the Status view loads
- **THEN** it MUST state that the agent is not installed
- **AND** it MUST point the user to the Install wizard (`i`)

### Requirement: Install wizard guides system checks, configuration, preview, execution, and summary
The Install wizard SHALL run as progressive steps: (1) live system checks via the engine detectors, (2) configuration form with defaults, (3) read-only preview of planned actions, (4) live execution with per-step progress, (5) final access summary equivalent to the closing block of `install-agent.sh`. The wizard SHALL invoke the same engine install operation as `crenein-agent install`.

#### Scenario: System checks pass and select the MongoDB image
- **GIVEN** the host runs Ubuntu/Debian with Docker available, at least 2048 MB free disk, registry connectivity, and `/proc/cpuinfo` without the `avx` flag
- **WHEN** step 1 runs
- **THEN** each check MUST render live as pending → running → ✅/⚠️/❌
- **AND** the wizard MUST report that `mongo:4.4` will be used due to missing AVX
- **AND** the user MUST be able to advance to the configuration step

#### Scenario: Fatal check blocks progress with a fix suggestion
- **GIVEN** Docker is not installed or its daemon is not running
- **WHEN** step 1 completes
- **THEN** the failed check MUST render ❌ with the engine's fix suggestion
- **AND** the wizard MUST NOT allow advancing to the next step
- **AND** the user MUST be able to re-run the checks or go back to Status

#### Scenario: Existing installation is protected
- **GIVEN** an existing installation is detected (a `docker-compose.yml` containing the `crenein/c-network-agent-back` image in the search paths)
- **WHEN** the user opens the Install wizard
- **THEN** the wizard MUST display the detected installation path and versions
- **AND** it MUST state that installing over an existing installation is not allowed
- **AND** it MUST NOT offer an execution path that overwrites `docker-compose.yml`, `.env`, or `/data/*`

#### Scenario: Configuration form offers safe defaults
- **GIVEN** step 1 passed
- **WHEN** step 2 renders
- **THEN** the form MUST pre-fill engine defaults: generated random 32-character alphanumeric MongoDB and Redis passwords, MongoDB user `cnetwork_admin`, ports `8000`, `8443`, `80`, `443`, `8086`, and data root `/data`
- **AND** generated secrets MUST be displayed masked
- **AND** invalid field values MUST show inline validation errors and block advancing

#### Scenario: Preview requires explicit confirmation
- **GIVEN** the configuration step is complete
- **WHEN** step 3 renders
- **THEN** it MUST list the planned actions: packages to install, the chosen MongoDB image, the `docker-compose.yml` and `.env` paths to be written (`.env` keys listed without secret values), directories to create under `/data`, and certificates to generate
- **AND** execution MUST NOT start until the user explicitly confirms

#### Scenario: Execution shows live progress and ends with the access summary
- **GIVEN** the user confirmed the preview
- **WHEN** the engine install runs
- **THEN** each engine step event MUST render live in order with a spinner on the active step
- **AND** on success the wizard MUST show the access summary with parity to `install-agent.sh`: `https://<VM_IP>:8000` (backend API), `https://<VM_IP>:443` and `http://<VM_IP>:80` (frontend), admin `admin@example.com` / `admin123`, InfluxDB `http://<VM_IP>:8086` (`admin`/`adminpassword`), persistent data under `/data/*`, and certificate locations

#### Scenario: Execution step fails
- **GIVEN** the engine install fails at any step
- **WHEN** the failure event arrives
- **THEN** the failed step MUST render ❌ with the engine error message and its fix suggestion
- **AND** subsequent steps MUST render as not executed
- **AND** the wizard MUST NOT claim success and MUST offer returning to Status

### Requirement: Update wizard previews the version, confirms, shows progress, and surfaces rollback
The Update wizard SHALL show the installed version against the manifest's available version with release notes, require explicit confirmation, render live progress for backup → pull → recreate → health, and show the result — including the automatic rollback when health checks fail. The wizard SHALL invoke the same engine update operation as `crenein-agent update`.

#### Scenario: Version preview with release notes
- **GIVEN** the installed agent is `1.8.3` and the manifest reports `latest = 1.8.4` with notes
- **WHEN** the Update wizard opens
- **THEN** it MUST display `1.8.3 → 1.8.4` and the release notes for `1.8.4`
- **AND** it MUST state what will be updated (agent, frontend) and what will NOT be touched (mongodb, influxdb, redis, `/data/*`)
- **AND** the update MUST NOT start without explicit confirmation

#### Scenario: Already up to date
- **GIVEN** the installed agent version equals the manifest's `latest`
- **WHEN** the Update wizard opens
- **THEN** it MUST state that the agent is up to date and offer no update action

#### Scenario: Successful update with live progress
- **GIVEN** the user confirmed the update
- **WHEN** the engine update runs
- **THEN** the wizard MUST render the phases live: backup (`.backups/${TIMESTAMP}/` with `docker-compose.yml`, `.env`, `image-state.txt`), pull of the target image tag, recreate via `docker compose up -d --no-deps --force-recreate agent frontend`, and health checks against `https://localhost:8000/health`
- **AND** on success it MUST show the new running version and the backup location used

#### Scenario: Failed health check makes rollback visible
- **GIVEN** the recreate succeeded but the backend health check does not pass within 60 seconds
- **WHEN** the engine triggers automatic rollback
- **THEN** the wizard MUST render the rollback as explicit steps (restore previous image IDs from `image-state.txt`, recreate `agent` and `frontend`)
- **AND** the final screen MUST state that the update failed, that rollback ran, the restored version, and the rollback outcome
- **AND** the wizard MUST NOT present the failed update as successful

### Requirement: Doctor view runs checks live with detail and re-run
The Doctor view SHALL run the same engine doctor checks as `crenein-agent doctor`, rendering each item live as ✅/⚠️/❌, with a selectable detail pane that shows the check output and fix suggestion, and a key to re-run all checks.

#### Scenario: Checks execute live
- **GIVEN** the Doctor view is opened
- **WHEN** the doctor run starts
- **THEN** each check (Docker installed and running, compose v1/v2 available, Docker Hub connectivity, `CNETWORK_API` connectivity, free disk > 2048 MB, file permissions on `.env`/compose/certs, agent services status, recent log errors) MUST render pending → running → result as its event arrives
- **AND** a summary line MUST show totals (passed / warnings / failures) when the run finishes

#### Scenario: Selecting a check shows detail and fix
- **GIVEN** a completed run where the disk space check failed
- **WHEN** the user moves the selection to that check with the arrow keys and presses `enter`
- **THEN** a detail pane MUST show the check output (e.g. available MB versus the 2048 MB minimum)
- **AND** it MUST show the engine's fix suggestion for that check

#### Scenario: Re-run resets and repeats
- **GIVEN** a completed doctor run
- **WHEN** the user presses `r`
- **THEN** all check states MUST reset to pending and the run MUST start again

#### Scenario: Doctor run cannot start the engine
- **GIVEN** the doctor engine call fails before producing any check events
- **WHEN** the Doctor view receives the failure
- **THEN** it MUST show the error with the suggestion to run `crenein-agent doctor` headless for raw output
- **AND** the TUI MUST remain responsive (no crash, navigation still works)

### Requirement: Logs view follows compose logs with filter, pause, and tail control
The Logs view SHALL stream compose logs live through the same engine call as `crenein-agent logs`, support filtering by service, pause with scrollback, resume with auto-follow, and a configurable tail size with a bounded buffer.

#### Scenario: Live follow
- **GIVEN** the stack is running
- **WHEN** the Logs view opens
- **THEN** it MUST start following the compose logs for all services with the default tail
- **AND** new lines MUST appear without user interaction, auto-scrolled to the bottom

#### Scenario: Filter by service
- **GIVEN** logs are streaming for all services
- **WHEN** the user presses `f`
- **THEN** the filter MUST cycle all → `agent` → `frontend` → `mongodb` → `influxdb` → `redis` → all
- **AND** only lines from the selected service MUST be displayed
- **AND** the active filter MUST be visible in the view header

#### Scenario: Pause and scroll
- **GIVEN** logs are streaming
- **WHEN** the user presses `space`
- **THEN** auto-scroll MUST stop while the stream continues buffering up to the buffer cap
- **AND** arrow/page keys MUST scroll the buffered history
- **AND** pressing `space` again MUST resume auto-follow at the newest line

#### Scenario: Log stream ends or fails
- **GIVEN** the underlying log stream terminates (services stopped or docker error)
- **WHEN** the stream closes
- **THEN** the view MUST show a clear stream-ended message with the reason when available
- **AND** offer a key to restart the stream
- **AND** the TUI MUST NOT crash

### Requirement: The TUI is a pure presentation layer over the engine
All operations triggered from the TUI SHALL execute the same `internal/engine` functions as the headless subcommands, receiving progress through engine events adapted to `tea.Msg`. The TUI MUST NOT implement install, update, doctor, status, or logs business logic, and MUST NOT shell out to `docker` except through `internal/dockerx` via the engine.

#### Scenario: Identical engine path for update
- **GIVEN** the same installed stack state
- **WHEN** an update is run from the Update wizard and via `crenein-agent update --yes --version 1.8.4`
- **THEN** both MUST invoke the same engine update function with equivalent parameters
- **AND** both MUST produce the same sequence of engine step events and the same on-disk effects (backup directory, recreated services)

#### Scenario: Views are testable with a fake engine
- **GIVEN** a fake engine that emits a scripted event sequence
- **WHEN** a view is driven by teatest with that fake
- **THEN** the rendered output MUST match the golden file captured at 80x24
- **AND** no real Docker daemon MUST be required by the test
