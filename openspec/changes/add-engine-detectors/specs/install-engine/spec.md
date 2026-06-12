## ADDED Requirements

### Requirement: Install is a single engine operation with automatic MongoDB selection
The install engine SHALL replace both `install-agent.sh` and `install-agent-mongo4.sh` with one operation whose only MongoDB branching point is the AVX detection result (or an explicit override in `InstallOptions`). The engine MUST be UI-agnostic: it SHALL NOT print, prompt, or read stdin, and SHALL report progress through the injected event sink and return a structured `InstallResult`.

#### Scenario: Install on AVX-capable host
- **GIVEN** AVX detection returns `true`
- **AND** no MongoDB override is set in `InstallOptions`
- **WHEN** install runs
- **THEN** the rendered `docker-compose.yml` MUST use image `mongodb/mongodb-community-server:7.0-ubuntu2204` for the `mongodb` service

#### Scenario: Install on host without AVX
- **GIVEN** AVX detection returns `false`
- **WHEN** install runs
- **THEN** the rendered `docker-compose.yml` MUST use image `mongo:4.4`
- **AND** the result MUST include a warning that MongoDB 4.4 is used for CPUs without AVX

#### Scenario: Explicit MongoDB override
- **GIVEN** `InstallOptions` sets a MongoDB image override
- **WHEN** install runs
- **THEN** the override MUST take precedence over the AVX detection result

### Requirement: Install pre-flight blocks unsupported or unsafe hosts
Before modifying the host, the install engine SHALL run pre-flight checks: supported distro (`ubuntu`/`debian`), root privileges, at least 2048 MB free disk, and connectivity to `https://registry-1.docker.io/v2/`, `https://hub.docker.com`, and `https://core.crenein.com` with a 10-second timeout each. Any fatal pre-flight failure MUST abort the install before any write occurs.

#### Scenario: Pre-flight failure aborts before writes
- **GIVEN** the host runs an unsupported distro or lacks root privileges
- **WHEN** install runs
- **THEN** the engine MUST return a structured error with a fix suggestion
- **AND** no file, directory, user, or service on the host MUST have been created or modified

### Requirement: System preparation installs required packages and Docker
The install engine SHALL update apt and ensure the packages `apt-transport-https`, `ca-certificates`, `curl`, `gnupg`, `lsb-release`, `fping`, `vsftpd`, `tftpd-hpa`, and `jq` are installed. When Docker is not present, it SHALL install Docker from the official repository (GPG key from `https://download.docker.com/linux/{distro}/gpg`, packages `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-compose-plugin`) and then start and enable the service via systemctl. All commands MUST run through the `CommandRunner` seam.

#### Scenario: Docker not installed
- **GIVEN** Docker detection reports Docker as not installed
- **WHEN** the system preparation step runs
- **THEN** the engine MUST add the official Docker repository for the detected distro
- **AND** install `docker-ce docker-ce-cli containerd.io docker-compose-plugin`
- **AND** run `systemctl start docker` and `systemctl enable docker`

#### Scenario: Docker already installed
- **GIVEN** Docker detection reports Docker installed and running
- **WHEN** the system preparation step runs
- **THEN** the engine MUST skip Docker installation
- **AND** MUST NOT modify the existing Docker installation

### Requirement: Persistent data directories are created with exact ownership and never clobbered
The install engine SHALL ensure `/data/mongodb`, `/data/influxdb2`, and `/data/redis` exist with ownership `1000:1000` and mode 755, and `/data/files` exists with ownership `tftp:tftp` and mode 755. Directories that already contain data MUST NOT have their contents, ownership, or permissions modified, and the engine SHALL report their existing sizes.

#### Scenario: Clean host
- **GIVEN** `/data` does not exist
- **WHEN** the directories step runs
- **THEN** the engine MUST create `/data/mongodb`, `/data/influxdb2`, `/data/redis` owned by `1000:1000`
- **AND** create `/data/files` owned by `tftp:tftp`

#### Scenario: Existing data preserved
- **GIVEN** `/data/mongodb` already exists and contains database files
- **WHEN** the directories step runs
- **THEN** the engine MUST NOT modify the directory contents, ownership, or permissions
- **AND** the result MUST flag the run as a re-installation over existing data and report the directory size

### Requirement: FTP and TFTP services are configured with remote config and embedded fallback
The install engine SHALL configure vsftpd by downloading `https://cnetworkspace.nyc3.digitaloceanspaces.com/resources/vsftpd.conf` to `/etc/vsftpd.conf`, falling back to the embedded default configuration when the download fails. It SHALL configure tftpd-hpa by downloading `https://cnetworkspace.nyc3.digitaloceanspaces.com/resources/tftpd-hpa` to `/etc/default/tftpd-hpa`, falling back to the embedded default (`TFTP_USERNAME="tftp"`, `TFTP_DIRECTORY="/data/files"`, `TFTP_ADDRESS=":69"`, `TFTP_OPTIONS="--secure --create"`). After writing each config the engine MUST restart and enable the corresponding service.

#### Scenario: Remote config download succeeds
- **GIVEN** the Spaces URLs respond successfully
- **WHEN** the FTP/TFTP step runs
- **THEN** the downloaded contents MUST be written to `/etc/vsftpd.conf` and `/etc/default/tftpd-hpa`
- **AND** the engine MUST run `systemctl restart vsftpd && systemctl enable vsftpd` and the tftpd-hpa equivalents

#### Scenario: Remote config download fails
- **GIVEN** the Spaces endpoint is unreachable
- **WHEN** the FTP/TFTP step runs
- **THEN** the engine MUST write the embedded default configurations instead
- **AND** the result MUST include a warning that defaults were used
- **AND** the install MUST continue

### Requirement: Backups user exists with the prescribed home
The install engine SHALL create a `backups` system user with home directory `/data/files` when it does not already exist, and ensure `/data/files` has mode 755. Creating the user MUST be skipped without error when it already exists.

#### Scenario: User already exists
- **GIVEN** the `backups` user exists
- **WHEN** the backups-user step runs
- **THEN** the engine MUST NOT attempt to recreate the user
- **AND** the step MUST be reported as skipped, not failed

### Requirement: Environment file is generated with random credentials and strict permissions
The install engine SHALL generate `.env` in the install directory with mode 600 containing exactly: `INFLUXDB_TOKEN`, `DOCKER_INFLUXDB_INIT_ADMIN_TOKEN` (both set to the same generated Influx token), `MONGODB_INITDB_ROOT_USERNAME=cnetwork_admin`, `MONGODB_INITDB_ROOT_PASSWORD` (random 32-character alphanumeric), `REDIS_PASSWORD` (random 32-character alphanumeric), `CNETWORK_API_URL=http://localhost:8000`, and `CNETWORK_API_TOKEN=your-api-token-here`. The InfluxDB token MUST be generated with a cryptographically secure random source per installation and MUST NOT be the hardcoded legacy value `24dee4f6135c7b08643362e4c1fe9b313b045843f02263fd632d20d380ad1ffa`. This is an intentional divergence from the bash scripts.

#### Scenario: Fresh .env generation
- **GIVEN** no `.env` exists in the install directory
- **WHEN** the env step runs
- **THEN** a `.env` MUST be written with mode 600 and all variables listed above
- **AND** the Mongo and Redis passwords MUST each be 32 alphanumeric characters from a secure random source
- **AND** the Influx token MUST differ from the legacy hardcoded value

#### Scenario: Existing .env preserved
- **GIVEN** a `.env` already exists in the install directory
- **WHEN** the env step runs
- **THEN** the engine MUST NOT overwrite or regenerate any credential
- **AND** the existing `.env` MUST be used for the rest of the install
- **AND** compatibility with installations using the legacy hardcoded token MUST be preserved

### Requirement: Self-signed certificates match the legacy parameters
The install engine SHALL generate self-signed certificates for the backend (`c-network-agent-back/certs/`) and frontend (`c-network-agent-front/certs/`) cert directories: RSA 4096, validity 365 days, subject `/C=US/ST=State/L=City/O=Crenein/OU=IT/CN=localhost`, and subjectAltName `DNS:localhost,DNS:*.localhost,IP:127.0.0.1,IP:0.0.0.0`. `cert.pem` MUST have mode 644 and `key.pem` mode 600. Existing certificates MUST NOT be regenerated.

#### Scenario: Certificates generated on clean install
- **GIVEN** no certificates exist in the cert directories
- **WHEN** the certificate step runs
- **THEN** both cert directories MUST contain `cert.pem` (644) and `key.pem` (600)
- **AND** the certificates MUST be RSA 4096, valid 365 days, with the exact subject and SANs above

#### Scenario: Existing certificates preserved
- **GIVEN** valid `cert.pem`/`key.pem` files already exist
- **WHEN** the certificate step runs
- **THEN** the engine MUST skip generation and report the step as skipped

### Requirement: docker-compose.yml is rendered from the embedded template with the exact stack definition
The install engine SHALL render `docker-compose.yml` from the `go:embed` template with: compose version 3.8, bridge network `agent-network`, named volumes `mongodb_data`, `influxdb_data`, `redis_data`; service `agent` (image `crenein/c-network-agent-back:latest`, ports `8000:8000` and `8443:8443`, volumes `/data/files:/app/files` and `./c-network-agent-back/certs:/root/certs:ro`, restart unless-stopped, depends_on mongodb/influxdb/redis, and the full environment variable set documented in the technical report); service `frontend` (image `crenein/c-network-agent-front:latest`, ports `80:80` and `443:443`, certs volume read-only, depends_on agent); service `mongodb` (parameterized image, NO externally exposed port, volume `mongodb_data:/data/db`, root username/password from `.env`); service `influxdb` (image `influxdb:2.7`, port `8086:8086`, volume `/data/influxdb2:/var/lib/influxdb2`, init mode setup, org `crenein`, init bucket `fping`, admin user `admin`/`adminpassword`, admin token from `.env`); service `redis` (image `redis:7-alpine`, NO exposed port, command `redis-server --appendonly yes --requirepass ${REDIS_PASSWORD}`, volume `redis_data:/data`). Credential values MUST appear in the rendered file only as `${VAR}` references resolved from `.env`.

#### Scenario: Rendered compose matches the contract
- **GIVEN** install parameters for an AVX host
- **WHEN** the compose template is rendered
- **THEN** the output MUST define exactly the services `agent`, `frontend`, `mongodb`, `influxdb`, `redis` with the images, ports, volumes, and env vars above
- **AND** MongoDB port 27017 and Redis port 6379 MUST NOT be mapped to the host
- **AND** no literal password or token value MUST appear in the rendered file

### Requirement: Stack startup is verified service by service
The install engine SHALL start the stack with compose `up -d` (dispatched through dockerx using the detected compose variant) and verify that each of `mongodb`, `influxdb`, `redis`, `agent`, and `frontend` reaches running state, replacing the legacy fixed `sleep 10` with bounded polling.

#### Scenario: All services running
- **GIVEN** compose up succeeds
- **WHEN** service verification runs
- **THEN** each of the five services MUST be confirmed running via the dockerx client
- **AND** the result MUST record per-service status

#### Scenario: A service fails to start
- **GIVEN** one service does not reach running state within the verification window
- **WHEN** service verification runs
- **THEN** the engine MUST return a structured error naming the failing service
- **AND** the error MUST include a fix suggestion to inspect that service's logs

### Requirement: InfluxDB readiness gates post-install configuration
The install engine SHALL wait for InfluxDB health at `http://localhost:8086/health` until the response contains `"status":"pass"`, with a 60-second timeout checking every 3 seconds, before attempting bucket creation.

#### Scenario: InfluxDB becomes healthy
- **GIVEN** InfluxDB returns `"status":"pass"` within 60 seconds
- **WHEN** the health wait runs
- **THEN** the engine MUST proceed to bucket creation

#### Scenario: InfluxDB health timeout
- **GIVEN** InfluxDB does not return `"status":"pass"` within 60 seconds
- **WHEN** the health wait runs
- **THEN** the engine MUST record a warning and continue with the documented manual instructions in the result
- **AND** bucket creation MUST still attempt its fallback chain

### Requirement: Admin user is created via API with bounded retries
The install engine SHALL create the initial admin user by calling `POST https://localhost:8000/api/v1/admins/register` (TLS verification disabled, header `Content-Type: application/json`) with body `{"email": "admin@example.com", "password": "admin123"}`, retrying up to 3 times with 3 seconds between attempts. On exhaustion the install MUST continue with a warning containing manual creation instructions.

#### Scenario: Admin created on retry
- **GIVEN** the first attempt fails and the second succeeds
- **WHEN** the admin-user step runs
- **THEN** the step MUST be reported as successful
- **AND** the access summary MUST include `admin@example.com / admin123`

#### Scenario: All attempts fail
- **GIVEN** all 3 attempts fail
- **WHEN** the admin-user step runs
- **THEN** the install MUST NOT abort
- **AND** the result MUST include a warning with the manual registration command

### Requirement: InfluxDB buckets are created through the 4-method fallback chain
The install engine SHALL ensure buckets `fping` and `devices` exist (org `crenein`, infinite retention), using this ordered fallback chain authenticated with the generated Influx token: (1) REST `GET http://localhost:8086/api/v2/orgs` with header `Authorization: Token <token>` to resolve the org ID, up to 10 attempts 3 seconds apart; (2) CLI in the container, `docker exec <influxdb-container> influx org list --skip-verify --token <token>`, up to 5 attempts; (3) direct CLI creation, `docker exec <influxdb-container> influx bucket create --skip-verify --token <token> --org crenein --name <bucket> --retention 0`; (4) REST `POST http://localhost:8086/api/v2/buckets` with body `{"orgID": <id>, "name": <bucket>, "retentionRules": [{"type":"expire","everySeconds":0}]}`, up to 5 attempts per bucket. If every method fails, the install MUST continue with a warning containing manual instructions, because the buckets are critical for fping and device polling.

#### Scenario: Org resolved via REST, buckets via REST
- **GIVEN** the orgs endpoint returns the `crenein` org ID
- **WHEN** bucket creation runs
- **THEN** both `fping` and `devices` MUST be created with infinite retention (`everySeconds: 0`)

#### Scenario: REST fails, CLI fallback succeeds
- **GIVEN** the REST orgs endpoint fails 10 times
- **AND** the in-container CLI works
- **WHEN** bucket creation runs
- **THEN** the engine MUST fall back to `docker exec ... influx` commands in the documented order
- **AND** both buckets MUST exist after the step

#### Scenario: Entire chain fails
- **GIVEN** all four methods fail
- **WHEN** bucket creation runs
- **THEN** the install MUST complete with a prominent warning
- **AND** the result MUST include the exact manual commands to create both buckets

### Requirement: Install is idempotent over existing installations
When the install engine detects an existing installation (a `docker-compose.yml` referencing the agent image, an existing `.env`, or non-empty `/data/*` directories), it SHALL operate in re-install mode: preserve `.env` credentials, preserve all `/data` contents, preserve certificates, and never reset database volumes. The result MUST clearly state which components were reused.

#### Scenario: Re-run over a complete installation
- **GIVEN** a previous install left compose, `.env`, certs, and populated `/data` directories
- **WHEN** install runs again
- **THEN** no credential MUST change, no data directory MUST be cleared, and no cert MUST be regenerated
- **AND** the result MUST list each reused component

#### Scenario: Resume after partial failure
- **GIVEN** a previous install failed after writing `.env` but before starting the stack
- **WHEN** install runs again
- **THEN** the engine MUST reuse the existing `.env` and complete the remaining steps
- **AND** the host MUST end in the same state as a single successful install

### Requirement: Install result includes the access summary
On success the install engine SHALL return an access summary equivalent to the legacy final report: backend API `https://<vm-ip>:8000`, frontend `https://<vm-ip>:443` and `http://<vm-ip>:80`, admin credentials `admin@example.com / admin123`, InfluxDB `http://<vm-ip>:8086` (`admin/adminpassword`), persistent data under `/data/*`, and certificate locations with their 365-day validity. The summary MUST be structured data, not pre-formatted text.

#### Scenario: Successful install summary
- **GIVEN** install completes successfully
- **WHEN** the result is returned
- **THEN** it MUST contain every access entry listed above as structured fields consumable by the future TUI, CLI, and `--json` output
