## ADDED Requirements

### Requirement: Global exit code contract
The CLI SHALL implement a single documented exit code table shared by all subcommands, with per-command extensions only where explicitly specified below. The global codes are: `0` success, `1` operation failure, `3` pre-flight/precondition failure, `4` aborted by the user, `64` usage error (unknown flag, invalid flag value, or required input unavailable in a non-interactive context). The `doctor` command overrides `1` and `2` with diagnostic semantics. The `update` command extends the table with `5` and `6` for rollback outcomes. Exit codes are an automation contract and MUST NOT change meaning without a new spec change.

#### Scenario: Unknown flag is a usage error on every command
- **GIVEN** any subcommand of `crenein-agent`
- **WHEN** it is invoked with an unrecognized flag such as `crenein-agent status --bogus`
- **THEN** the process MUST exit with code `64`
- **AND** an error message naming the unknown flag MUST be written to stderr
- **AND** nothing MUST be written to stdout

#### Scenario: Invalid flag value is a usage error
- **GIVEN** the `update` subcommand
- **WHEN** invoked as `crenein-agent update --version not-a-semver`
- **THEN** the process MUST exit with code `64`
- **AND** stderr MUST state that `--version` requires an `X.Y.Z` value

#### Scenario: Usage errors never collide with doctor semantics
- **GIVEN** the `doctor` command reserves exit codes `0`, `1`, and `2` for diagnostic results
- **WHEN** `crenein-agent doctor --bogus` is invoked
- **THEN** the process MUST exit with code `64`, not `1` or `2`

### Requirement: Output stream discipline
Every subcommand SHALL write machine-consumable data (JSON documents, log content, dry-run plans, final result summaries) to stdout and everything else (progress, prompts, confirmations, warnings, errors) to stderr, so that piping stdout never captures decoration and redirecting stderr never loses data.

#### Scenario: JSON output is parseable through a pipe
- **GIVEN** an installed agent stack
- **WHEN** the operator runs `crenein-agent doctor --json | jq .summary.status`
- **THEN** stdout MUST contain exactly one JSON document and nothing else
- **AND** `jq` MUST parse it without errors
- **AND** any progress or warning text MUST appear only on stderr

#### Scenario: Errors are reported on stderr with a non-zero exit
- **GIVEN** the Docker daemon is stopped
- **WHEN** the operator runs `crenein-agent status`
- **THEN** the error message (including a fix suggestion such as `systemctl start docker`) MUST be written to stderr
- **AND** stdout MUST be empty
- **AND** the exit code MUST be non-zero

### Requirement: Non-interactive contexts never prompt
The CLI SHALL detect whether stdin and stderr are attached to a TTY (via `term.IsTerminal`). When they are not, the CLI MUST NOT emit prompts or confirmations and MUST NOT block waiting for input; any value or consent that would have been collected interactively MUST instead cause an immediate exit with code `64` and a stderr message naming the missing flag and its environment variable equivalent.

#### Scenario: Update without TTY and without --yes fails fast
- **GIVEN** `crenein-agent update` is executed from a cron job (stdin is not a TTY)
- **AND** the `--yes` flag is not provided
- **WHEN** the command reaches the confirmation step
- **THEN** the process MUST exit with code `64` before any backup, pull, or container mutation
- **AND** stderr MUST state that confirmation is required and that `--yes` enables non-interactive mode

#### Scenario: Install without TTY and without required input fails fast
- **GIVEN** `crenein-agent install` is executed with stdin piped from `/dev/null`
- **AND** neither `--yes` nor a complete set of value flags/environment variables is provided
- **WHEN** the command needs a value it would otherwise prompt for
- **THEN** the process MUST exit with code `64`
- **AND** stderr MUST list each missing input with its flag name and `CRENEIN_*` environment variable

#### Scenario: Piped commands never hang
- **GIVEN** any subcommand executed with stdin closed
- **WHEN** the command runs to completion or failure
- **THEN** the process MUST terminate on its own without waiting for terminal input

### Requirement: Machine mode and decoration controls
The `--json` flag (available on `doctor` and `status`) SHALL switch the command to machine mode: ANSI colors, spinners, and progress decorations MUST be disabled and exactly one JSON document MUST be emitted on stdout. Independently, colors and spinners MUST also be disabled when the target stream is not a TTY, when `--no-color` is passed, or when the `NO_COLOR` environment variable is set to any non-empty value. Every JSON document MUST include a top-level `schema_version` field (integer, initially `1`); within a schema version, fields MAY be added but MUST NOT be renamed, removed, or change type.

#### Scenario: --json disables color and spinners
- **GIVEN** a terminal session with color support
- **WHEN** the operator runs `crenein-agent doctor --json`
- **THEN** stdout MUST contain no ANSI escape sequences
- **AND** no spinner or progress animation MUST be rendered on either stream

#### Scenario: NO_COLOR is honored
- **GIVEN** the environment contains `NO_COLOR=1`
- **WHEN** the operator runs `crenein-agent doctor` (human mode)
- **THEN** the output MUST contain no ANSI escape sequences while keeping the same textual content

#### Scenario: schema_version is present and stable
- **GIVEN** any `--json` invocation
- **WHEN** the JSON document is emitted
- **THEN** it MUST contain `"schema_version": 1`
- **AND** consumers parsing documented version-1 fields MUST continue to work across CLI releases that keep `schema_version = 1`

### Requirement: Quiet mode
All subcommands SHALL accept a global `--quiet` flag that suppresses non-essential stderr output (progress steps, informational notes). Errors, prompts that are still required, and the stdout data contract MUST be unaffected by `--quiet`. `--quiet` MUST NOT change any exit code.

#### Scenario: Quiet update in automation
- **GIVEN** a cron job runs `crenein-agent update --yes --quiet`
- **WHEN** the update succeeds
- **THEN** stderr MUST contain no progress lines
- **AND** the exit code MUST be `0`
- **AND** the final result line on stdout (old version, new version) MUST still be emitted

#### Scenario: Quiet never hides errors
- **GIVEN** `crenein-agent update --yes --quiet` and a failing pre-flight check
- **WHEN** the command aborts
- **THEN** the error and its fix suggestion MUST still be written to stderr
- **AND** the exit code MUST be `3`

### Requirement: Install command interactive plain mode
`crenein-agent install` SHALL provide a plain, sequential, line-based prompt flow (no TUI) when stdin and stderr are TTYs. Prompts MUST be written to stderr, one per line, in the stable format `<label> [<default>]: `, and MUST be answerable by pressing Enter to accept the default. The prompt order MUST be stable within a schema version so `expect`-style automation keeps working. The collected inputs are: install directory, MongoDB major selection (`auto`/`7`/`4`, where `auto` applies AVX detection from `/proc/cpuinfo`), `CNETWORK_API_URL`, `CNETWORK_API_TOKEN`, admin email, and admin password. After collection the command MUST print a summary of resolved values (secrets masked) and ask for a final `Proceed? [y/N]` confirmation before invoking the engine.

#### Scenario: Interactive install with defaults
- **GIVEN** a root shell on a supported Ubuntu/Debian VM with a TTY
- **WHEN** the operator runs `crenein-agent install` and presses Enter at every prompt, then answers `y` to the final confirmation
- **THEN** the engine install MUST run with install dir = current working directory, MongoDB selection = `auto`, `CNETWORK_API_URL=http://localhost:8000`, `CNETWORK_API_TOKEN=your-api-token-here`, admin `admin@example.com`/`admin123`
- **AND** on success the process MUST exit `0` and print the access summary (backend `https://<IP>:8000`, frontend `https://<IP>:443`, InfluxDB `http://<IP>:8086`) to stdout

#### Scenario: Declining the final confirmation aborts cleanly
- **GIVEN** the interactive flow reached `Proceed? [y/N]`
- **WHEN** the operator answers `n` or presses Enter
- **THEN** the process MUST exit with code `4`
- **AND** no system change (packages, files, containers) MUST have been made

#### Scenario: AVX detection drives the Mongo image in auto mode
- **GIVEN** the operator accepted `auto` for the MongoDB selection
- **WHEN** `/proc/cpuinfo` lacks the `avx` flag
- **THEN** the engine MUST be invoked with `mongo:4.4`
- **AND** when `avx` is present it MUST be invoked with `mongodb/mongodb-community-server:7.0-ubuntu2204`
- **AND** the chosen image MUST be shown in the pre-confirmation summary

### Requirement: Install command non-interactive mode
`crenein-agent install` SHALL support a fully non-interactive mode activated by `--yes`. Each promptable value SHALL be resolvable by flag and environment variable with precedence flag > environment variable > (TTY prompt) > default: `--dir` / `CRENEIN_INSTALL_DIR` (default: current working directory), `--mongo {auto|7|4}` / `CRENEIN_MONGO_MAJOR` (default `auto`), `--api-url` / `CRENEIN_API_URL` (default `http://localhost:8000`), `--api-token` / `CRENEIN_API_TOKEN` (default `your-api-token-here`), `--admin-email` / `CRENEIN_ADMIN_EMAIL` (default `admin@example.com`), `--admin-password` / `CRENEIN_ADMIN_PASSWORD` (default `admin123`). With `--yes`, unset values take their defaults and no prompt or confirmation is shown. Exit codes: `0` success; `1` install step failed; `3` pre-flight failure (not root, unsupported distro per `/etc/os-release` `ID` not in {`ubuntu`,`debian`}, insufficient disk, no connectivity); `4` user aborted; `64` usage error.

#### Scenario: Unattended install via flags
- **GIVEN** a root shell without a TTY (e.g. `ssh host 'crenein-agent install --yes --api-url https://core.crenein.com --api-token tok123'`)
- **WHEN** the command runs
- **THEN** no prompt MUST be emitted
- **AND** the engine MUST receive `CNETWORK_API_URL=https://core.crenein.com` and `CNETWORK_API_TOKEN=tok123` for the generated `.env`
- **AND** on success the exit code MUST be `0`

#### Scenario: Environment variables fill unset flags
- **GIVEN** `CRENEIN_MONGO_MAJOR=4` is exported and no `--mongo` flag is passed
- **WHEN** `crenein-agent install --yes` runs
- **THEN** the engine MUST be invoked with the `mongo:4.4` image regardless of AVX detection
- **AND** a flag value `--mongo 7` WOULD override the environment variable if both were present

#### Scenario: Pre-flight failure exits 3 before mutating the system
- **GIVEN** the command is executed as a non-root user
- **WHEN** `crenein-agent install --yes` runs
- **THEN** the process MUST exit with code `3`
- **AND** stderr MUST suggest re-running with `sudo`
- **AND** no package, file, or container change MUST have been made

#### Scenario: Forcing Mongo 7 without AVX is refused unless forced
- **GIVEN** a CPU without the `avx` flag in `/proc/cpuinfo`
- **WHEN** `crenein-agent install --yes --mongo 7` runs
- **THEN** the process MUST exit with code `3`
- **AND** stderr MUST explain that MongoDB >= 5.0 requires AVX and suggest `--mongo 4`

### Requirement: Update command flags and behavior parity
`crenein-agent update` SHALL accept the flags `--yes` (skip confirmation), `--version X.Y.Z` (explicit target tag), `--dry-run` (print the plan, change nothing), `--skip-frontend` (do not pull/recreate `crenein/c-network-agent-front`), `--no-cleanup` (skip `docker image prune -f`), and `--force` (proceed even when the target equals the current version / the image is unchanged). The command MUST keep behavioral parity with `update-agent.sh`: pre-flight checks (root, Docker daemon, install dir discovery in CWD then `/root/` then `/home/*/` validating the compose file references `crenein/c-network-agent-back`, `.env` present, >= 2048 MB free disk, connectivity to `https://registry-1.docker.io/v2/` and `https://hub.docker.com`), backup to `.backups/${TIMESTAMP}/` (compose, `.env` chmod 600, `image-state.txt`; keep last 5), recreation via `docker compose up -d --no-deps --force-recreate agent frontend` so databases are never restarted, backend health check on the public root `GET /health` (no `X-API-Key`; `https://localhost:8000/health` with insecure TLS falling back to `http://localhost:8000/health`) with 60s timeout, automatic rollback on failure, and append-style logging to `/var/log/c-network-agent-update.log`. An HTTP 404 from a legacy backend image that predates the root `/health` endpoint MUST NOT be counted as success (the legacy script's accept-any-response bug is not ported); in that transition case the CLI relies on the engine's container-running fallback with a logged warning, per the `update-engine` spec of `add-engine-detectors`. Divergence from the legacy script: consent is `--yes` (not `--force`); the MongoDB image MUST never be changed by update.

#### Scenario: Default target is the manifest latest
- **GIVEN** the `versions.json` manifest reports `agent.latest = "1.8.4"` and the installed agent is `1.8.3`
- **WHEN** the operator runs `crenein-agent update --yes`
- **THEN** the CLI MUST pull `crenein/c-network-agent-back:1.8.4` (a versioned tag, not `:latest`)
- **AND** show `1.8.3 -> 1.8.4` (with release notes when available) before executing
- **AND** exit `0` on healthy completion

#### Scenario: Explicit version is validated against the manifest
- **GIVEN** the manifest does not contain release `9.9.9`
- **WHEN** the operator runs `crenein-agent update --yes --version 9.9.9`
- **THEN** the process MUST exit with code `1`
- **AND** stderr MUST list the nearest available versions from the manifest
- **AND** with `--force` added, the CLI MAY skip manifest validation and attempt `docker pull crenein/c-network-agent-back:9.9.9` directly

#### Scenario: Already up to date
- **GIVEN** the installed agent already runs the manifest's latest version
- **WHEN** `crenein-agent update --yes` runs without `--force`
- **THEN** the process MUST exit with code `0`
- **AND** print that the agent is already up to date, performing no backup, pull, or recreate

#### Scenario: Dry run is side-effect free and TTY-independent
- **GIVEN** any environment, with or without a TTY
- **WHEN** `crenein-agent update --dry-run` runs
- **THEN** the plan (current version, target version, services to recreate, backup directory that would be created) MUST be printed to stdout
- **AND** no confirmation MUST be requested even without `--yes`
- **AND** no container, image, file, or backup MUST be created or modified
- **AND** the exit code MUST be `0`

#### Scenario: Skip frontend
- **GIVEN** an installed stack
- **WHEN** `crenein-agent update --yes --skip-frontend` runs
- **THEN** only `crenein/c-network-agent-back` MUST be pulled
- **AND** the recreate step MUST target only the `agent` service
- **AND** the frontend health check MUST be skipped

#### Scenario: Cleanup is skippable
- **GIVEN** a successful update
- **WHEN** `--no-cleanup` was passed
- **THEN** `docker image prune -f` MUST NOT be executed
- **AND** without `--no-cleanup` it MUST be executed after the health checks pass

#### Scenario: Databases are never restarted by update
- **GIVEN** mongodb, influxdb, and redis containers are running
- **WHEN** any `crenein-agent update` execution recreates services
- **THEN** the recreate command MUST be `docker compose up -d --no-deps --force-recreate` limited to `agent` (and `frontend` unless `--skip-frontend`)
- **AND** the mongodb/influxdb/redis container IDs MUST be unchanged afterwards

### Requirement: Update exit codes encode the rollback outcome
`crenein-agent update` SHALL use these exit codes: `0` success (including "already up to date"); `1` failure before any mutation (manifest unreachable without `--force`, pull failed before recreate); `3` pre-flight failure; `4` confirmation declined on a TTY; `5` update failed after mutation and the automatic rollback restored the previous images successfully; `6` update failed and the automatic rollback also failed (manual intervention required); `64` usage error.

#### Scenario: Health check failure triggers rollback and exit 5
- **GIVEN** the new image was pulled and `agent`/`frontend` were recreated
- **WHEN** the root `GET /health` on the backend (`https://localhost:8000/health` with insecure TLS, falling back to `http://localhost:8000/health`) does not return HTTP 200 within 60 seconds
- **THEN** the CLI MUST restore the previous images from `.backups/${TIMESTAMP}/image-state.txt` (`docker tag <previous_id> crenein/c-network-agent-back:latest` and recreate)
- **AND** exit with code `5`
- **AND** stderr MUST state which backup was used and that the previous version is running again

#### Scenario: Failed rollback exits 6 with manual instructions
- **GIVEN** the update failed and the automatic rollback also fails (e.g. tagging or recreate error)
- **WHEN** the command terminates
- **THEN** the exit code MUST be `6`
- **AND** stderr MUST print the backup directory path and the exact manual recovery commands

#### Scenario: Confirmation declined
- **GIVEN** a TTY session and no `--yes`
- **WHEN** the operator answers `n` to the update confirmation
- **THEN** the process MUST exit with code `4`
- **AND** no backup, pull, or recreate MUST have happened

### Requirement: Doctor command human output
`crenein-agent doctor` SHALL run the engine's diagnostic checks and render one line per check with a status marker (pass / warning / critical), the check name, a short result message, and — for any non-passing check — an actionable fix suggestion. The check set includes at minimum: Docker installed, Docker daemon running, docker compose available (v1 or v2, reporting which), connectivity to Docker Hub, connectivity to the configured `CNETWORK_API_URL`, free disk space >= 2048 MB, file permissions (`.env` mode 600, compose file present, certs readable), agent stack services running (agent, frontend, mongodb, influxdb, redis), backend public root `GET /health` returning HTTP 200 (no `X-API-Key`; `https://localhost:8000/health` with insecure TLS, falling back to `http://localhost:8000/health`; an HTTP 404 from a legacy backend that predates the root endpoint MUST render as a warning backed by the agent container state via Docker, never as a pass), recent errors in the last 50 log lines, and AVX/MongoDB compatibility (running a Mongo >= 5.0 image on a CPU without AVX is critical). Output ends with a one-line summary of totals.

#### Scenario: Healthy system renders all passes
- **GIVEN** a correctly installed and running stack
- **WHEN** the operator runs `crenein-agent doctor` on a color-capable TTY
- **THEN** every check MUST render as passing
- **AND** the summary line MUST report zero warnings and zero critical failures
- **AND** the exit code MUST be `0`

#### Scenario: Failing check includes a fix suggestion
- **GIVEN** the Docker daemon is stopped
- **WHEN** `crenein-agent doctor` runs
- **THEN** the `Docker daemon` check MUST render as critical
- **AND** its line (or an attached line) MUST include a concrete fix such as `systemctl start docker`
- **AND** the remaining checks that depend on Docker MUST be reported as skipped rather than failing misleadingly

### Requirement: Doctor JSON output shape
`crenein-agent doctor --json` SHALL emit exactly one JSON document on stdout with this shape (types in parentheses; all fields REQUIRED unless marked nullable):

```json
{
  "schema_version": 1,
  "command": "doctor",
  "timestamp": "2026-06-12T15:04:05Z",
  "cli_version": "0.3.0",
  "summary": {
    "status": "ok",
    "total": 11,
    "passed": 9,
    "warnings": 1,
    "critical": 1,
    "skipped": 0
  },
  "checks": [
    {
      "id": "docker.daemon",
      "name": "Docker daemon running",
      "severity": "critical",
      "status": "pass",
      "message": "Docker 26.1 is running",
      "fix": null,
      "duration_ms": 41
    }
  ]
}
```

Field contract: `schema_version` (int), `command` (string, constant `"doctor"`), `timestamp` (string, RFC 3339 UTC), `cli_version` (string), `summary.status` (string enum: `"ok"` | `"warning"` | `"critical"`), `summary.total`/`passed`/`warnings`/`critical`/`skipped` (int), `checks` (array, one element per executed or skipped check, stable order), `checks[].id` (string, stable machine identifier in dotted form, e.g. `docker.installed`, `docker.daemon`, `docker.compose`, `net.dockerhub`, `net.cnetwork_api`, `disk.space`, `files.permissions`, `services.running`, `agent.health`, `logs.recent_errors`, `cpu.avx_mongo`), `checks[].name` (string, human label), `checks[].severity` (string enum: `"critical"` | `"warning"` — the weight of the check if it fails), `checks[].status` (string enum: `"pass"` | `"warn"` | `"fail"` | `"skip"`), `checks[].message` (string), `checks[].fix` (string or null — actionable command/suggestion, null when passing), `checks[].duration_ms` (int). Check `id` values are part of the contract: new ids MAY be added, existing ids MUST NOT be renamed within `schema_version` 1.

#### Scenario: JSON shape is stable and jq-addressable
- **GIVEN** any system state
- **WHEN** `crenein-agent doctor --json` runs
- **THEN** `jq -e '.schema_version == 1'`, `jq -e '.summary.status'`, and `jq -e '.checks[0].id'` MUST all succeed against stdout
- **AND** every element of `checks` MUST contain all eight documented fields

#### Scenario: Summary status reflects the worst check
- **GIVEN** at least one check with `severity = "critical"` has `status = "fail"`
- **WHEN** the JSON document is emitted
- **THEN** `summary.status` MUST be `"critical"`
- **AND** with only warning-level findings it MUST be `"warning"`
- **AND** with no findings it MUST be `"ok"`

### Requirement: Doctor exit codes encode the diagnosis
`crenein-agent doctor` SHALL exit with code `0` when every check passes, `1` when at least one warning-level finding exists and no critical check failed, and `2` when at least one critical check failed. These codes apply identically with and without `--json`. Usage errors exit `64`. A doctor run that cannot even start its checks (e.g. internal error) SHALL exit `2` with the error on stderr.

#### Scenario: Warnings only
- **GIVEN** all critical checks pass but the CNETWORK API connectivity check warns
- **WHEN** `crenein-agent doctor --json` runs
- **THEN** the exit code MUST be `1`
- **AND** `summary.status` MUST be `"warning"`

#### Scenario: Critical failure
- **GIVEN** the backend `/health` check fails
- **WHEN** `crenein-agent doctor` runs
- **THEN** the exit code MUST be `2`
- **AND** a cron wrapper using `crenein-agent doctor --json --quiet; case $? in ... esac` MUST be able to branch on `0`/`1`/`2`

### Requirement: Status command
`crenein-agent status` SHALL report the installation directory, the agent version (from the `version` field of the backend's public root `GET /health` when present — legacy backends answer 404 on that route — otherwise from the running image tag, otherwise `"unknown"`), the MongoDB flavor in use, and one row per expected compose service (`agent`, `frontend`, `mongodb`, `influxdb`, `redis`) with image, state, health, and uptime. Exit codes: `0` all expected services are running; `1` at least one expected service is not running or unhealthy; `3` no installation found (no compose file referencing `crenein/c-network-agent-back` in CWD, `/root/`, or `/home/*/`); `64` usage error. Human mode renders an aligned table; `--json` emits the document below.

#### Scenario: Healthy stack
- **GIVEN** all five services are running
- **WHEN** `crenein-agent status` runs
- **THEN** stdout MUST contain one row per service with its state and uptime
- **AND** the exit code MUST be `0`

#### Scenario: Degraded stack is visible in the exit code
- **GIVEN** the `redis` container is exited
- **WHEN** `crenein-agent status --json --quiet` runs from a script
- **THEN** the exit code MUST be `1`
- **AND** the JSON `services` entry for `redis` MUST have `"state": "exited"`

#### Scenario: No installation found
- **GIVEN** a VM where the agent was never installed
- **WHEN** `crenein-agent status` runs
- **THEN** the process MUST exit with code `3`
- **AND** stderr MUST suggest running `crenein-agent install`

### Requirement: Status JSON output shape
`crenein-agent status --json` SHALL emit exactly one JSON document on stdout with this shape:

```json
{
  "schema_version": 1,
  "command": "status",
  "timestamp": "2026-06-12T15:04:05Z",
  "cli_version": "0.3.0",
  "install_dir": "/root",
  "agent": {
    "version": "1.8.3",
    "version_source": "health",
    "image": "crenein/c-network-agent-back:1.8.3",
    "health": "healthy"
  },
  "mongo": {
    "image": "mongodb/mongodb-community-server:7.0-ubuntu2204",
    "major": "7.x"
  },
  "services": [
    {
      "name": "agent",
      "image": "crenein/c-network-agent-back:1.8.3",
      "state": "running",
      "health": "healthy",
      "status_text": "Up 3 days",
      "uptime_seconds": 262800
    }
  ]
}
```

Field contract: `schema_version` (int), `command` (string, constant `"status"`), `timestamp` (string, RFC 3339 UTC), `cli_version` (string), `install_dir` (string, absolute path), `agent.version` (string, `"unknown"` when undeterminable), `agent.version_source` (string enum: `"health"` | `"image_tag"` | `"unknown"`), `agent.image` (string), `agent.health` (string enum: `"healthy"` | `"unhealthy"` | `"unknown"`), `mongo.image` (string), `mongo.major` (string, e.g. `"7.x"`, `"4.x"`), `services` (array with exactly one element per expected service, stable order: agent, frontend, mongodb, influxdb, redis), `services[].name` (string), `services[].image` (string, `""` when the container is missing), `services[].state` (string enum: `"running"` | `"restarting"` | `"exited"` | `"created"` | `"paused"` | `"missing"`), `services[].health` (string enum: `"healthy"` | `"unhealthy"` | `"none"` — `"none"` when the container defines no healthcheck), `services[].status_text` (string, docker's human status), `services[].uptime_seconds` (int, `0` when not running).

#### Scenario: Version source degrades gracefully
- **GIVEN** the deployed backend does not yet expose `version` in `GET /health`
- **WHEN** `crenein-agent status --json` runs against an agent running image `crenein/c-network-agent-back:1.8.3`
- **THEN** `agent.version` MUST be `"1.8.3"` and `agent.version_source` MUST be `"image_tag"`
- **AND** when the image tag is `latest` and `/health` has no version, `agent.version` MUST be `"unknown"` with `version_source = "unknown"`

#### Scenario: Missing service is represented, not omitted
- **GIVEN** the `frontend` container was removed manually
- **WHEN** `crenein-agent status --json` runs
- **THEN** the `services` array MUST still contain a `frontend` element with `"state": "missing"`, `"uptime_seconds": 0`
- **AND** the exit code MUST be `1`

### Requirement: Logs command
`crenein-agent logs [service]` SHALL stream logs from the compose stack via the detected compose binary (`docker compose logs` or `docker-compose logs`). Flags: `-f`/`--follow` to stream continuously, `--tail N` (int, default `100`) for the initial backlog. The optional positional `service` MUST be one of `agent`, `frontend`, `mongodb`, `influxdb`, `redis`; when omitted, logs from all services are shown. Log content goes to stdout. When stdout is not a TTY, or `--no-color`/`NO_COLOR` is in effect, compose MUST be invoked with `--no-color`. Exit codes: `0` on normal completion, including termination of `--follow` by SIGINT/SIGTERM; `1` Docker/compose error; `3` no installation found; `64` unknown service name or usage error.

#### Scenario: Tail a single service
- **GIVEN** a running stack
- **WHEN** the operator runs `crenein-agent logs agent --tail 50`
- **THEN** the last 50 log lines of the `agent` service MUST be written to stdout
- **AND** the process MUST exit `0` without following

#### Scenario: Follow mode terminates cleanly on Ctrl-C
- **GIVEN** `crenein-agent logs -f agent` is streaming
- **WHEN** the operator presses Ctrl-C (SIGINT)
- **THEN** the underlying compose process MUST be terminated
- **AND** the CLI MUST exit with code `0`

#### Scenario: Unknown service is a usage error
- **GIVEN** a running stack
- **WHEN** the operator runs `crenein-agent logs nginx`
- **THEN** the process MUST exit with code `64`
- **AND** stderr MUST list the valid service names (agent, frontend, mongodb, influxdb, redis)

#### Scenario: Piped logs carry no color codes
- **GIVEN** the operator runs `crenein-agent logs agent --tail 200 > /tmp/agent.log`
- **WHEN** the command completes
- **THEN** `/tmp/agent.log` MUST contain no ANSI escape sequences

### Requirement: Rollback command
`crenein-agent rollback` SHALL restore the previous agent (and frontend) images using the most recent snapshot under `.backups/` in the installation directory, reading the previous image IDs from `image-state.txt`, retagging them (`docker tag <id> crenein/c-network-agent-back:latest`, idem frontend), and recreating with `docker compose up -d --no-deps --force-recreate agent frontend`, followed by the same 60-second `/health` verification used by update. Before acting, the command MUST display the chosen backup timestamp and the image IDs/versions it will restore and ask for confirmation, skipped by `--yes`. The flag `--backup <TIMESTAMP>` MAY select a specific snapshot instead of the most recent, and `--list` SHALL print the available snapshots (timestamp, backed-up versions) without changing anything. Exit codes: `0` rollback completed and health check passed; `1` rollback executed but failed (including failed post-rollback health check); `3` no backups available or no installation found; `4` confirmation declined; `64` usage error (including a `--backup` timestamp that does not exist, with available timestamps listed on stderr).

#### Scenario: Roll back to the previous version
- **GIVEN** `.backups/20260612-1030/` is the most recent snapshot containing `image-state.txt`
- **WHEN** the operator runs `crenein-agent rollback` on a TTY and confirms
- **THEN** the agent and frontend images recorded in that `image-state.txt` MUST be retagged and recreated with `--no-deps --force-recreate`
- **AND** databases MUST NOT be restarted
- **AND** after `GET /health` returns HTTP 200 within 60 seconds the process MUST exit `0`

#### Scenario: Non-interactive rollback requires --yes
- **GIVEN** stdin is not a TTY
- **WHEN** `crenein-agent rollback` runs without `--yes`
- **THEN** the process MUST exit with code `64` before any change
- **AND** with `--yes` it MUST proceed without prompting

#### Scenario: No backups available
- **GIVEN** the installation directory contains no `.backups/` snapshots
- **WHEN** `crenein-agent rollback --yes` runs
- **THEN** the process MUST exit with code `3`
- **AND** stderr MUST explain that no backup exists and that backups are created automatically by `crenein-agent update`

#### Scenario: Listing backups is read-only
- **GIVEN** three snapshots exist under `.backups/`
- **WHEN** `crenein-agent rollback --list` runs
- **THEN** the three timestamps with their recorded versions MUST be printed to stdout, most recent first
- **AND** no confirmation MUST be requested and no state MUST change
- **AND** the exit code MUST be `0`

### Requirement: Integration tests validate the automation contract
The repository SHALL include bash-based integration tests (e.g. under `test/integration/`) that invoke the compiled `crenein-agent` binary and assert the headless contract: exit codes per scenario, stdout/stderr separation, prompt-free behavior without a TTY, and `--json` shapes validated with `jq`. Contract-level tests (usage errors, `--dry-run`, missing-input failures, JSON schema fields) MUST run without Docker; full-stack tests (real install/update/rollback) MAY require a Docker-capable, client-like VM and MUST be marked as such.

#### Scenario: Exit codes are asserted by scripts
- **GIVEN** the integration test suite
- **WHEN** it runs `crenein-agent update --version not-a-semver; echo $?`
- **THEN** the assertion `[ $? -eq 64 ]` MUST pass
- **AND** equivalent assertions MUST exist for at least exit codes `0`, `1`, `2` (doctor), `3`, `4`, `5` (simulated), and `64`

#### Scenario: JSON shapes are validated with jq
- **GIVEN** the integration test suite
- **WHEN** it runs `crenein-agent doctor --json` and `crenein-agent status --json`
- **THEN** `jq -e` assertions MUST verify `schema_version`, `summary.status` enum membership, presence of all documented `checks[]` fields, and all documented `services[]` fields
- **AND** the suite MUST fail if any documented field is missing or has the wrong type

#### Scenario: No-TTY behavior is exercised in CI
- **GIVEN** the test suite runs with stdin redirected from `/dev/null`
- **WHEN** it invokes `crenein-agent update` (no `--yes`) and `crenein-agent install` (no `--yes`)
- **THEN** both invocations MUST exit `64` within a bounded time (no hang)
- **AND** stderr MUST name the missing `--yes` flag or missing inputs
