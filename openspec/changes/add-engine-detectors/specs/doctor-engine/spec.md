## ADDED Requirements

### Requirement: Doctor returns a structured, machine-consumable report
The doctor engine SHALL return a typed report containing one entry per check with: a stable check ID, a human-readable name, a status of `OK`, `WARNING`, or `CRITICAL`, a detail message, and — for any non-OK status — an actionable fix suggestion. The report SHALL include an overall summary equal to the worst individual status. The report MUST be serializable for the future `--json` output and renderable by both the TUI and the CLI without re-running checks.

#### Scenario: All checks pass
- **GIVEN** a healthy host with a running agent stack
- **WHEN** doctor runs
- **THEN** every check MUST report `OK`
- **AND** the summary MUST be `OK`

#### Scenario: Mixed results
- **GIVEN** one check reports `WARNING` and another `CRITICAL`
- **WHEN** doctor runs
- **THEN** the summary MUST be `CRITICAL`
- **AND** each non-OK check MUST include a non-empty fix suggestion

### Requirement: Doctor checks Docker and compose availability
The doctor engine SHALL verify that Docker is installed, that the daemon is running, and that a compose variant (v2 `docker compose` preferred, v1 `docker-compose` accepted) is available, reusing `internal/detect`.

#### Scenario: Daemon stopped
- **GIVEN** Docker is installed but the daemon is not running
- **WHEN** doctor runs
- **THEN** the Docker check MUST report `CRITICAL`
- **AND** the fix suggestion MUST include `systemctl start docker`

#### Scenario: Only compose v1 present
- **GIVEN** only the `docker-compose` v1 binary is available
- **WHEN** doctor runs
- **THEN** the compose check MUST report `OK` (or at most `WARNING`) indicating v1 is in use
- **AND** the detail MUST recommend installing `docker-compose-plugin` for v2

#### Scenario: No compose available
- **GIVEN** neither compose variant is available
- **WHEN** doctor runs
- **THEN** the compose check MUST report `CRITICAL` with an install suggestion

### Requirement: Doctor checks connectivity and disk space
The doctor engine SHALL check connectivity to Docker Hub (`https://registry-1.docker.io/v2/`, `https://hub.docker.com`) and to `https://core.crenein.com` with a 10-second timeout per endpoint, and SHALL check that free disk space exceeds 2048 MB.

#### Scenario: core.crenein.com unreachable
- **GIVEN** Docker Hub responds but `core.crenein.com` times out
- **WHEN** doctor runs
- **THEN** the core connectivity check MUST report `CRITICAL` (the agent cannot reach its API)
- **AND** the Docker Hub check MUST independently report `OK`
- **AND** the fix suggestion MUST mention firewall/DNS/outbound HTTPS to core.crenein.com

#### Scenario: Low disk space
- **GIVEN** less than 2048 MB is free
- **WHEN** doctor runs
- **THEN** the disk check MUST report `CRITICAL` with the measured and required values
- **AND** the fix suggestion MUST include `docker image prune -f`

### Requirement: Doctor checks file and socket permissions
The doctor engine SHALL verify: `.env` exists with mode 600, `docker-compose.yml` exists and is readable, certificate files have the expected modes (`cert.pem` 644, `key.pem` 600), and the docker socket is accessible to the current user.

#### Scenario: World-readable .env
- **GIVEN** `.env` exists with mode 644
- **WHEN** doctor runs
- **THEN** the `.env` permission check MUST report `WARNING`
- **AND** the fix suggestion MUST be `chmod 600 <install-dir>/.env`

#### Scenario: Socket permission denied
- **GIVEN** the current user cannot access `/var/run/docker.sock`
- **WHEN** doctor runs
- **THEN** the socket check MUST report `CRITICAL`
- **AND** the fix suggestion MUST include `sudo usermod -aG docker <user>` or re-running with sudo

### Requirement: Doctor checks agent stack status and recent log errors
The doctor engine SHALL report the status of each agent service (`agent`, `frontend`, `mongodb`, `influxdb`, `redis`) via compose ps, and SHALL scan the last 50 log lines of each agent service for error-level entries, summarizing any matches.

#### Scenario: Service down
- **GIVEN** the `agent` container is not running
- **WHEN** doctor runs
- **THEN** the services check MUST report `CRITICAL` naming the `agent` service
- **AND** the fix suggestion MUST include the compose command to start it and how to view its logs

#### Scenario: Errors found in recent logs
- **GIVEN** the last 50 log lines of the `agent` service contain error entries
- **WHEN** doctor runs
- **THEN** the log check MUST report `WARNING` with a count and a sample of the matched lines

#### Scenario: No installation present
- **GIVEN** no install directory with a valid `docker-compose.yml` is found
- **WHEN** doctor runs
- **THEN** the stack and log checks MUST report that no installation was found (WARNING, not a crash)
- **AND** all host-level checks (Docker, compose, connectivity, disk, socket) MUST still execute and be reported

### Requirement: Doctor is read-only and fault-tolerant
The doctor engine MUST NOT modify any file, container, image, service, or configuration. Every check SHALL run even when previous checks fail; an unexpected error inside a check MUST be converted into a `CRITICAL` entry for that check instead of aborting the run.

#### Scenario: Check crash does not abort the run
- **GIVEN** one check encounters an unexpected internal error
- **WHEN** doctor runs
- **THEN** that check MUST appear as `CRITICAL` with the error in its detail
- **AND** all remaining checks MUST still execute and appear in the report

#### Scenario: No side effects
- **GIVEN** any host state
- **WHEN** doctor runs to completion
- **THEN** no file modification, container restart, image pull, or configuration change MUST have occurred
