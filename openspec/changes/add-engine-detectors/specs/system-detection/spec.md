## ADDED Requirements

### Requirement: AVX detection selects the MongoDB image
The system SHALL detect CPU AVX support by reading `/proc/cpuinfo` and SHALL use the result to select the MongoDB image: `mongodb/mongodb-community-server:7.0-ubuntu2204` when AVX is present, `mongo:4.4` when it is not. Detection MUST go through the filesystem seam so it is testable without a real `/proc`.

#### Scenario: CPU with AVX support
- **GIVEN** `/proc/cpuinfo` contains a `flags` line that includes the `avx` flag on at least one processor
- **WHEN** AVX detection runs
- **THEN** the result MUST be `true`
- **AND** the recommended MongoDB image MUST be `mongodb/mongodb-community-server:7.0-ubuntu2204`

#### Scenario: CPU without AVX support
- **GIVEN** `/proc/cpuinfo` contains `flags` lines but none includes the `avx` flag
- **WHEN** AVX detection runs
- **THEN** the result MUST be `false`
- **AND** the recommended MongoDB image MUST be `mongo:4.4`

#### Scenario: cpuinfo unreadable
- **GIVEN** `/proc/cpuinfo` cannot be read
- **WHEN** AVX detection runs
- **THEN** detection MUST return a structured error including a fix suggestion to use the explicit MongoDB image override
- **AND** the caller MUST NOT silently assume AVX support

### Requirement: Distro detection restricts supported operating systems
The system SHALL determine the distribution by parsing the `ID` variable in `/etc/os-release` and SHALL only accept `ubuntu` or `debian`. Any other value, or a missing file, MUST produce a structured unsupported-distro error.

#### Scenario: Supported distro
- **GIVEN** `/etc/os-release` contains `ID=ubuntu` or `ID=debian`
- **WHEN** distro detection runs
- **THEN** detection MUST succeed and report the distro ID
- **AND** the distro ID MUST be available for composing the Docker repository URL (`https://download.docker.com/linux/{ID}`)

#### Scenario: Unsupported distro
- **GIVEN** `/etc/os-release` contains `ID=fedora` (or any value other than `ubuntu`/`debian`)
- **WHEN** distro detection runs
- **THEN** detection MUST return a structured error stating that only Ubuntu and Debian are supported
- **AND** install pre-flight MUST treat this error as fatal

#### Scenario: Missing os-release
- **GIVEN** `/etc/os-release` does not exist or is unreadable
- **WHEN** distro detection runs
- **THEN** detection MUST return a structured error
- **AND** the error MUST include a fix suggestion indicating the supported operating systems

### Requirement: Docker availability detection distinguishes failure modes
The system SHALL detect whether the `docker` binary is installed and whether the Docker daemon is reachable, and MUST distinguish three failure modes — not installed, daemon not running, and socket permission denied — each with its own fix suggestion.

#### Scenario: Docker installed and running
- **GIVEN** the `docker` binary is in PATH
- **AND** the daemon responds to a ping through the dockerx client
- **WHEN** Docker detection runs
- **THEN** the result MUST report Docker as installed and the daemon as running

#### Scenario: Docker not installed
- **GIVEN** the `docker` binary is not in PATH
- **WHEN** Docker detection runs
- **THEN** the result MUST report Docker as not installed
- **AND** the fix suggestion MUST indicate that the install engine can install Docker automatically

#### Scenario: Daemon not running
- **GIVEN** the `docker` binary exists but the daemon does not respond
- **WHEN** Docker detection runs
- **THEN** the result MUST report the daemon as not running
- **AND** the fix suggestion MUST include `systemctl start docker`

#### Scenario: Socket permission denied
- **GIVEN** the daemon is running but access to `/var/run/docker.sock` is denied for the current user
- **WHEN** Docker detection runs
- **THEN** the result MUST report a permission problem distinct from a stopped daemon
- **AND** the fix suggestion MUST include `usermod -aG docker <user>` or running with sudo

### Requirement: Compose variant detection prefers v2
The system SHALL detect whether docker compose is available as the v2 plugin (`docker compose`) or the v1 standalone binary (`docker-compose`), and when both are available it MUST prefer v2. The detected variant SHALL drive all compose command dispatch in `internal/dockerx`.

#### Scenario: Compose v2 available
- **GIVEN** `docker compose version` succeeds
- **WHEN** compose detection runs
- **THEN** the result MUST report variant v2
- **AND** all compose operations MUST be dispatched as `docker compose <args>`

#### Scenario: Only compose v1 available
- **GIVEN** `docker compose version` fails
- **AND** `docker-compose --version` succeeds
- **WHEN** compose detection runs
- **THEN** the result MUST report variant v1
- **AND** all compose operations MUST be dispatched as `docker-compose <args>`

#### Scenario: Both variants available
- **GIVEN** both `docker compose` and `docker-compose` work
- **WHEN** compose detection runs
- **THEN** the result MUST report variant v2 as the selected variant

#### Scenario: No compose available
- **GIVEN** neither variant works
- **WHEN** compose detection runs
- **THEN** the result MUST report compose as unavailable with a structured error
- **AND** the fix suggestion MUST mention installing `docker-compose-plugin`

### Requirement: Connectivity detection probes required endpoints with a 10-second timeout
The system SHALL check connectivity to `https://registry-1.docker.io/v2/`, `https://hub.docker.com`, and `https://core.crenein.com`, using a timeout of 10 seconds per endpoint, and SHALL report a per-endpoint result rather than a single aggregate boolean. Probes MUST honor the provided `context.Context`.

#### Scenario: All endpoints reachable
- **GIVEN** all three endpoints respond within 10 seconds
- **WHEN** connectivity detection runs
- **THEN** the result MUST mark each endpoint as reachable

#### Scenario: One endpoint unreachable
- **GIVEN** `https://core.crenein.com` does not respond within 10 seconds
- **AND** the Docker endpoints respond
- **WHEN** connectivity detection runs
- **THEN** the result MUST mark only `core.crenein.com` as unreachable
- **AND** the Docker Hub endpoints MUST still be reported as reachable
- **AND** the unreachable entry MUST carry a fix suggestion (check firewall/DNS/outbound HTTPS)

#### Scenario: Context cancelled
- **GIVEN** the caller cancels the context during probing
- **WHEN** connectivity detection runs
- **THEN** detection MUST stop promptly and return the context error

### Requirement: Disk space detection enforces a 2 GB minimum
The system SHALL measure free disk space on the target path and SHALL report failure when fewer than 2048 MB are available, matching the legacy `check_disk_space` behavior.

#### Scenario: Sufficient space
- **GIVEN** the target filesystem has 5000 MB free
- **WHEN** disk space detection runs
- **THEN** the result MUST pass and include the measured free megabytes

#### Scenario: Insufficient space
- **GIVEN** the target filesystem has 1024 MB free
- **WHEN** disk space detection runs
- **THEN** the result MUST fail
- **AND** the error MUST state the required minimum (2048 MB) and the measured value
- **AND** the fix suggestion MUST mention freeing space (e.g. `docker image prune`)

### Requirement: Permission detection covers root and docker socket
The system SHALL detect whether the process runs with root privileges and whether the docker socket is accessible, since install and update operations require root.

#### Scenario: Running as root
- **GIVEN** the effective UID is 0
- **WHEN** permission detection runs
- **THEN** the result MUST report root privileges as available

#### Scenario: Not running as root
- **GIVEN** the effective UID is not 0
- **WHEN** permission detection runs
- **THEN** the result MUST report missing root privileges
- **AND** the fix suggestion MUST be to re-run the command with `sudo`
