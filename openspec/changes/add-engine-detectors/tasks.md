## 1. Detection (`internal/detect/`)

- [ ] 1.1 Implement `detect.AVX(ctx)` reading `/proc/cpuinfo` through the FS seam: returns true when any processor lists the `avx` flag; structured error with fix suggestion when cpuinfo is unreadable.
- [ ] 1.2 Implement `detect.Distro(ctx)` parsing `/etc/os-release` `ID`; only `ubuntu` and `debian` are supported, anything else returns a structured unsupported-distro error.
- [ ] 1.3 Implement `detect.Docker(ctx)`: binary present in PATH and daemon responding (`docker info` via dockerx ping), distinguishing not-installed vs not-running vs no-socket-permission.
- [ ] 1.4 Implement `detect.Compose(ctx)`: probe `docker compose version` (v2) then `docker-compose --version` (v1); report which variant is available, preferring v2 when both exist.
- [ ] 1.5 Implement `detect.Connectivity(ctx)` probing `https://registry-1.docker.io/v2/`, `https://hub.docker.com`, and `https://core.crenein.com` with a 10-second timeout per endpoint, returning per-endpoint results.
- [ ] 1.6 Implement `detect.DiskSpace(ctx, path)` returning free megabytes and a pass/fail against the 2048 MB minimum.
- [ ] 1.7 Implement `detect.Permissions(ctx)`: effective root/sudo check and docker socket accessibility check, each with its fix suggestion (`run with sudo`, `usermod -aG docker`).
- [ ] 1.8 Define the shared structured error type (`Op`, `Cause`, `FixSuggestion`) used by detect and engine packages.

## 2. Docker wrapper (`internal/dockerx/`)

- [ ] 2.1 Define the `dockerx.Client` interface: compose Up/Ps/Pull/Exec (with `--no-deps --force-recreate` support), image Inspect/Tag/Prune, container list/filter, daemon ping.
- [ ] 2.2 Define the `CommandRunner` seam for non-docker system commands (apt-get, systemctl, openssl, useradd, chown, chmod) and the FS/HTTP prober seams used by detect and engine.
- [ ] 2.3 Implement the real CLI-backed client with compose v1/v2 dispatch (`docker compose ...` vs `docker-compose ...`) selected from `detect.Compose`.
- [ ] 2.4 Implement recording fakes for Client, CommandRunner, FS, and HTTP prober that capture invocation sequences for test assertions.

## 3. Compose templates (`internal/compose/`)

- [ ] 3.1 Author `docker-compose.yml.tmpl` (version 3.8, network `agent-network`, named volumes `mongodb_data`/`influxdb_data`/`redis_data`) with the exact services, images, ports, volumes, and env vars from the technical report; Mongo image is the template parameter.
- [ ] 3.2 Embed the template plus default `vsftpd.conf` and `tftpd-hpa` configs via `go:embed`; expose `compose.Render(params)`.
- [ ] 3.3 Golden-file tests: render with Mongo 7.0 and Mongo 4.4 params and assert the full output, including that no credential values appear in the rendered compose (only `${VAR}` references).

## 4. Install engine (`internal/engine/install.go`)

- [ ] 4.1 Define `InstallOptions` (Mongo image override, install dir, admin credentials override) and `InstallResult` (steps, access summary, warnings); wire the `Reporter` event sink.
- [ ] 4.2 Implement pre-flight: distro, root, disk, connectivity, AVX detection → Mongo image selection.
- [ ] 4.3 Implement system preparation: `apt-get update -y`, required packages (apt-transport-https, ca-certificates, curl, gnupg, lsb-release, fping, vsftpd, tftpd-hpa, jq), Docker installation (GPG key, repo, docker-ce/docker-ce-cli/containerd.io/docker-compose-plugin, systemctl start+enable) skipped when Docker is already present.
- [ ] 4.4 Implement persistent directories: create `/data/{mongodb,influxdb2,redis}` 1000:1000 and `/data/files` only when missing; report sizes of existing data and never modify ownership of non-empty directories.
- [ ] 4.5 Implement vsftpd and tftpd-hpa configuration: download from the DigitalOcean Spaces URLs with fallback to embedded defaults; write `/etc/vsftpd.conf` and `/etc/default/tftpd-hpa`; `chown tftp:tftp /data/files`; restart + enable both services.
- [ ] 4.6 Implement `backups` user creation (skip if exists), home `/data/files`, 755 `backups:backups`.
- [ ] 4.7 Implement `.env` generation: random 32-char alphanumeric Mongo and Redis passwords, cryptographically random InfluxDB token (divergence AD-5), exact variable set from the report, chmod 600; never regenerate when a valid `.env` already exists.
- [ ] 4.8 Implement self-signed certificate generation for backend and frontend cert dirs: RSA 4096, 365 days, exact subject and SANs, cert.pem 644 / key.pem 600; skip when certs already exist.
- [ ] 4.9 Implement compose render + `docker compose up -d` + per-service running verification (mongodb, influxdb, redis, agent, frontend).
- [ ] 4.10 Implement post-install: InfluxDB health wait (`"status":"pass"`, 60s/3s), admin user registration via API with 3 retries 3s apart, InfluxDB buckets `fping`+`devices` with the 4-method fallback chain (REST orgs ×10 → CLI org list ×5 → CLI bucket create → REST bucket create ×5) and manual-instruction warnings on total failure.
- [ ] 4.11 Implement idempotency: detect existing installation (compose + `.env` + `/data` data) and return a re-install plan that preserves data and credentials; never overwrite `.env` or non-empty `/data` dirs.
- [ ] 4.12 Build the final access summary (backend/frontend URLs, admin credentials, InfluxDB URL, data paths, cert location) into `InstallResult`.

## 5. Update engine (`internal/engine/update.go`)

- [ ] 5.1 Define `UpdateOptions` (Version required, DryRun, SkipFrontend, Force, NoCleanup) and `UpdateResult` (previous/new image IDs, backup path, rolled-back flag).
- [ ] 5.2 Implement pre-flight: root, docker daemon, `find_install_dir` (CWD → `/root` → `/home/*`, validating the compose references the agent image), `.env` exists, ≥2048 MB free, connectivity to registry-1.docker.io and hub.docker.com (10s timeout).
- [ ] 5.3 Implement `detect_current_state`: agent and frontend image IDs + digests, Mongo image/version from compose (fallback `docker inspect`), running services, `/data` presence. Mongo image is read-only state — never modified.
- [ ] 5.4 Implement backup: `.backups/${TIMESTAMP}/` with `docker-compose.yml`, `.env` (chmod 600), `image-state.txt` (agent + frontend image IDs); prune to the 5 most recent backups.
- [ ] 5.5 Implement pull by explicit tag `crenein/c-network-agent-back:X.Y.Z` (+ frontend unless SkipFrontend) and image-ID comparison: unchanged + no Force → successful no-op exit.
- [ ] 5.6 Implement recreate: `docker compose up -d --no-deps --force-recreate agent frontend` (databases untouched).
- [ ] 5.7 Implement health checks: backend `/health` HTTPS-then-HTTP (60s timeout, 3s interval), frontend 443-then-80, databases via compose ps.
- [ ] 5.8 Implement automatic rollback on up/health failure: re-tag previous IDs from `image-state.txt`, recreate agent+frontend, re-verify health, mark result rolled-back.
- [ ] 5.9 Implement cleanup (`docker image prune -f` unless NoCleanup) and structured logging to `/var/log/c-network-agent-update.log` in `[YYYY-MM-DD HH:MM:SS] LEVEL: message` format.
- [ ] 5.10 Implement DryRun: execute pre-flight and state detection, report the would-be plan, perform no writes, pulls, or recreates.

## 6. Doctor engine (`internal/engine/doctor.go`)

- [ ] 6.1 Define `DoctorReport`/`Check` types (ID, Name, Status OK|WARNING|CRITICAL, Detail, FixSuggestion) with a worst-status summary, JSON-serializable for future `--json`.
- [ ] 6.2 Implement host checks reusing `internal/detect`: Docker installed + daemon running, compose v1/v2 availability, Docker Hub and core.crenein.com connectivity, disk space > 2 GB.
- [ ] 6.3 Implement permission checks: `.env` mode 600, compose readable, cert key modes, docker socket access — each with a concrete fix command.
- [ ] 6.4 Implement stack checks: agent services status via compose ps (missing installation is reported, not fatal) and error scan of the last 50 log lines per agent service.
- [ ] 6.5 Guarantee doctor is read-only and degrades gracefully: every check runs even when earlier ones fail, individual check errors become CRITICAL entries instead of aborting the run.

## 7. Tests

- [ ] 7.1 detect: table-driven tests with fake FS/runner/prober — cpuinfo with/without `avx`, os-release variants (ubuntu, debian, fedora, missing), docker absent/stopped/no-permission, compose v2-only/v1-only/both/none, connectivity timeout, disk under/over 2048 MB.
- [ ] 7.2 dockerx: dispatch tests asserting v2 (`docker compose ...`) vs v1 (`docker-compose ...`) argument construction for up/ps/pull/exec.
- [ ] 7.3 compose: golden-file render tests for both Mongo images (covered by 3.3, keep in CI).
- [ ] 7.4 install: fake-based flow tests — clean install command sequence vs parity table, AVX branch selects correct Mongo image, existing-data idempotency (no `.env`/data overwrite), vsftpd/tftpd download-failure fallback, bucket fallback chain method-by-method, admin-user retry exhaustion, `.env` content and mode assertions, random token ≠ legacy hardcoded token.
- [ ] 7.5 update: fake-based flow tests — happy path with explicit tag, no-op on identical image ID, rollback triggered by failed health check (assert re-tag + recreate sequence), backup retention pruning to 5, dry-run performs zero writes, find_install_dir search order.
- [ ] 7.6 doctor: report shape tests — all-OK, mixed WARNING/CRITICAL with fix suggestions present, no installation found, log-error detection.
- [ ] 7.7 Quality gates: `go build ./...`, `go vet ./...`, `gofmt -l .` clean, `go test ./...` green.
- [ ] 7.8 ⚠ Real-VM validation (deferred to `add-headless-commands` execution but listed for traceability): clean Ubuntu VM install, no-AVX VM install (mongo:4.4), compose-v1-only host update.
