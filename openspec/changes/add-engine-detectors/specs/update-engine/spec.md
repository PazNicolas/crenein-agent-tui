## ADDED Requirements

### Requirement: Update pre-flight validates the host and locates the installation
The update engine SHALL run pre-flight checks before any modification: root privileges, Docker daemon running, install directory discovery, `.env` present in the install directory, at least 2048 MB free disk, and connectivity to `https://registry-1.docker.io/v2/` and `https://hub.docker.com` with a 10-second timeout each. Install directory discovery MUST search in order: current working directory, then `/root/`, then `/home/*/`, accepting only a directory whose `docker-compose.yml` references the `crenein/c-network-agent-back` image. Any pre-flight failure MUST abort the update before any write.

#### Scenario: Install directory found in /root
- **GIVEN** the CWD has no valid `docker-compose.yml`
- **AND** `/root/docker-compose.yml` exists and references `crenein/c-network-agent-back`
- **WHEN** pre-flight runs
- **THEN** `/root` MUST be selected as the install directory

#### Scenario: Compose without agent image is rejected
- **GIVEN** a `docker-compose.yml` exists in the CWD but does not reference the agent image
- **WHEN** install directory discovery runs
- **THEN** that directory MUST be skipped and the search MUST continue to `/root/` and `/home/*/`

#### Scenario: No installation found
- **GIVEN** no searched location contains a valid compose file
- **WHEN** pre-flight runs
- **THEN** the engine MUST return a structured error stating no installation was found
- **AND** the fix suggestion MUST mention running install first or executing update from the install directory

#### Scenario: Missing .env aborts
- **GIVEN** the install directory has a valid compose file but no `.env`
- **WHEN** pre-flight runs
- **THEN** the update MUST abort with a structured error before any pull or backup

### Requirement: Current state is detected and MongoDB is immutable
Before updating, the engine SHALL capture the current state: agent and frontend image IDs and digests, the MongoDB image/version, running services, and presence of data in `/data/*`. The MongoDB image SHALL be read from the `mongodb` service entry in `docker-compose.yml`, with fallback to `docker inspect --format='{{.Config.Image}}'` of the running container. The update engine MUST NEVER change the MongoDB image, version, or service definition.

#### Scenario: Mongo 4.4 installation stays on 4.4
- **GIVEN** the compose file declares `mongo:4.4`
- **WHEN** an update to any agent version runs
- **THEN** the `mongodb` service definition MUST remain byte-identical
- **AND** the mongodb container MUST NOT be recreated or restarted

#### Scenario: Mongo image read via inspect fallback
- **GIVEN** the compose file's mongodb image cannot be parsed
- **AND** the mongodb container is running
- **WHEN** state detection runs
- **THEN** the engine MUST resolve the image via `docker inspect` of the compose-managed mongodb container

### Requirement: Backup precedes any mutation and retains the last five
Before pulling or recreating anything, the engine SHALL create `.backups/${TIMESTAMP}/` inside the install directory containing: `docker-compose.yml`, `.env` copied with mode 600, and `image-state.txt` recording the current agent and frontend image IDs (e.g. `AGENT_IMAGE_ID=<id>`). After a successful backup the engine SHALL prune the `.backups/` directory to the 5 most recent entries.

#### Scenario: Backup created before pull
- **GIVEN** pre-flight passed
- **WHEN** the update proceeds
- **THEN** the backup directory MUST exist with all three files before the first `docker pull`
- **AND** the copied `.env` MUST have mode 600

#### Scenario: Old backups pruned
- **GIVEN** `.backups/` already contains 5 previous backups
- **WHEN** a new backup is created
- **THEN** only the 5 most recent backups MUST remain

#### Scenario: Backup failure aborts the update
- **GIVEN** the backup cannot be written (e.g. disk error)
- **WHEN** the backup step runs
- **THEN** the update MUST abort before pulling or recreating anything

### Requirement: Images are pulled by explicit version tag
The update engine SHALL pull `crenein/c-network-agent-back:<version>` where `<version>` is the explicit version in `UpdateOptions` (e.g. `1.8.4`), and the corresponding frontend image unless `SkipFrontend` is set. The engine MUST NOT pull the bare `:latest` tag implicitly. This is an intentional divergence from `update-agent.sh`. After pulling, the engine SHALL compare image IDs against the pre-pull state; when nothing changed and `Force` is not set, the update MUST exit as a successful no-op.

#### Scenario: Explicit version pull
- **GIVEN** `UpdateOptions.Version = "1.8.4"`
- **WHEN** the pull step runs
- **THEN** the engine MUST pull `crenein/c-network-agent-back:1.8.4`
- **AND** MUST NOT pull `crenein/c-network-agent-back:latest`

#### Scenario: No-op when image unchanged
- **GIVEN** the pulled image ID equals the currently deployed image ID
- **AND** `Force` is not set
- **WHEN** the comparison step runs
- **THEN** the update MUST finish successfully reporting that no update was needed
- **AND** no container MUST be recreated

#### Scenario: Skip frontend
- **GIVEN** `SkipFrontend` is set
- **WHEN** the pull and recreate steps run
- **THEN** only the agent image MUST be pulled and only the `agent` service recreated

### Requirement: Recreate touches only agent and frontend
The update engine SHALL apply the new images with compose `up -d --no-deps --force-recreate agent frontend` (dispatched through dockerx with the detected compose variant). The `mongodb`, `influxdb`, and `redis` services MUST NOT be restarted, recreated, or reconfigured by an update.

#### Scenario: Databases stay up through an update
- **GIVEN** mongodb, influxdb, and redis are running
- **WHEN** the recreate step runs
- **THEN** the exact compose invocation MUST include `--no-deps --force-recreate` and only the `agent` and `frontend` services
- **AND** the database containers MUST keep their container IDs (no restart)

### Requirement: Health checks gate success and trigger automatic rollback
After recreating, the engine SHALL verify: backend readiness via the root `GET /health` endpoint — public, requiring no `X-API-Key` (introduced by the `c-network-agent-back` change `add-agent-health-version`) — probing `https://localhost:8000/health` with insecure TLS (self-signed certificate) falling back to `http://localhost:8000/health` (HTTP 200, 60-second timeout, 3-second interval); frontend `https://localhost:443` falling back to `http://localhost:80`; databases running via compose ps. An HTTP 404 MUST NOT be counted as a passing backend check (the legacy `update-agent.sh` accepted any HTTP response, including 404 — that bug MUST NOT be ported). When the update's target version predates the root `/health` endpoint (transition case, e.g. `1.8.3`), the engine SHALL fall back to verifying that the agent container is running via dockerx and MUST log a `WARN` entry stating that HTTP readiness could not be confirmed; it MUST NOT silently approve a 404. A backend health failure, or a failed compose up, MUST trigger automatic rollback: re-tag the previous image IDs from `image-state.txt` onto the deployed tags, recreate `agent` and `frontend` with the same `--no-deps --force-recreate` invocation, and re-verify health. The result MUST mark the update as rolled back.

#### Scenario: Healthy update succeeds
- **GIVEN** the backend returns HTTP 200 on `/health` within 60 seconds after recreate
- **WHEN** health checks run
- **THEN** the update MUST be reported successful with previous and new image IDs

#### Scenario: Failed health check rolls back automatically
- **GIVEN** the backend does not return HTTP 200 within 60 seconds after recreate
- **WHEN** health checks run
- **THEN** the engine MUST re-tag the agent and frontend image IDs recorded in `image-state.txt`
- **AND** recreate `agent` and `frontend` with `--no-deps --force-recreate`
- **AND** the result MUST report the update as failed-and-rolled-back, naming the backup directory used

#### Scenario: 404 from a legacy target version is never silent success
- **GIVEN** the update's target version was published without the root `/health` endpoint
- **AND** the recreated backend answers `GET /health` with HTTP 404
- **WHEN** health checks run
- **THEN** the engine MUST NOT treat the 404 as a passing health check
- **AND** it MUST verify the agent container is running via dockerx as the readiness fallback
- **AND** it MUST log a `WARN` entry stating that HTTP readiness could not be confirmed

#### Scenario: Frontend degraded is a warning, not a rollback
- **GIVEN** the backend health check passes
- **AND** the frontend check fails
- **WHEN** health checks run
- **THEN** the engine MUST report a warning for the frontend
- **AND** MUST NOT roll back the backend solely for a frontend check failure

### Requirement: Cleanup and logging follow the legacy contract
After a successful update the engine SHALL run `docker image prune -f` unless `NoCleanup` is set. All update steps SHALL be appended to `/var/log/c-network-agent-update.log` in the format `[YYYY-MM-DD HH:MM:SS] LEVEL: message` with levels `STEP`, `INFO`, `OK`, `WARN`, `ERR`. The engine MUST NOT write colored or formatted text to stdout — presentation belongs to the consumers.

#### Scenario: Log entries written
- **GIVEN** an update runs to completion
- **WHEN** the log file is inspected
- **THEN** it MUST contain timestamped entries for each major step in the documented format

#### Scenario: Cleanup skipped
- **GIVEN** `NoCleanup` is set
- **WHEN** the update finishes successfully
- **THEN** `docker image prune` MUST NOT be executed

### Requirement: Dry-run performs no mutations
When `DryRun` is set, the update engine SHALL execute pre-flight checks and current-state detection, compute and return the would-be plan (version to pull, services to recreate, backup destination), and MUST NOT create backups, pull images, recreate containers, prune images, or write to the update log.

#### Scenario: Dry-run reports the plan without side effects
- **GIVEN** `UpdateOptions{Version: "1.8.4", DryRun: true}` on a valid installation
- **WHEN** the update runs
- **THEN** the result MUST describe the planned pull, recreate, and backup destination
- **AND** no file MUST be written, no image pulled, and no container touched
