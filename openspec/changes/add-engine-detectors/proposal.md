## Why

The C-Network agent stack is currently installed and updated on ~20 client VMs through three bash scripts (`install-agent.sh`, `install-agent-mongo4.sh`, `update-agent.sh`, ~700-750 lines each) that live in the private `c-network-agent-back` repo. The two install scripts are 99% duplicated code whose only meaningful difference is the MongoDB image (`mongodb/mongodb-community-server:7.0-ubuntu2204` vs `mongo:4.4` for CPUs without AVX), and the client must manually pick the right one — a 50/50 risk of choosing wrong. The update script pulls `crenein/c-network-agent-back:latest` blindly, so the client never knows which version it will receive, and a failed mid-step install leaves the VM in a corrupt state with no diagnostic tooling. This change ports ALL of that operational logic into testable, UI-agnostic Go packages (Phase 2 of the master plan), so subsequent changes can layer cobra subcommands and the bubbletea TUI on top of an engine that is already proven by unit tests.

## What Changes

- Add `internal/detect/` — system detection: AVX flag in `/proc/cpuinfo` (selects MongoDB 7.0 vs 4.4), distro via `/etc/os-release` (only `ubuntu`/`debian` supported), Docker installed and daemon running, docker compose v2 (`docker compose`) vs v1 (`docker-compose`) with v2 preferred, connectivity checks (`registry-1.docker.io`, `hub.docker.com`, `core.crenein.com` with 10s timeout each), free disk space (2 GB minimum), and permissions (root/sudo, docker socket access).
- Add `internal/dockerx/` — an interface-based wrapper over the `docker` and `docker compose` / `docker-compose` CLIs so every engine operation is mockable in tests and no engine package shells out directly.
- Add `internal/compose/` — the `docker-compose.yml` template embedded via `go:embed`, parameterized for the MongoDB image, with the exact services, images, ports, volumes, and environment variables documented in the legacy scripts.
- Add `internal/engine/install.go` — a single installer that unifies `install-agent.sh` and `install-agent-mongo4.sh` behind an AVX-detection branch: apt packages, Docker installation, `/data/*` persistent directories with exact ownership, vsftpd and tftpd-hpa configuration (remote config download with fallback to embedded defaults), `backups` user, `.env` generation with random 32-char credentials (chmod 600), self-signed RSA 4096 certificates, compose rendering, `docker compose up -d`, service verification, admin user creation via API (3 retries), InfluxDB bucket creation with the 4-method fallback chain, health checks, idempotency against existing installations, and a final access summary.
- Add `internal/engine/update.go` — ports `update-agent.sh`: pre-flight checks, install-dir discovery (CWD → `/root` → `/home/*`), current-state detection (image IDs, digest, Mongo version — never changed on update), timestamped backups under `.backups/` (keep last 5), pull by EXPLICIT version tag, image-ID no-op detection, recreate with `--no-deps --force-recreate agent frontend` (databases untouched), health checks with automatic rollback from `image-state.txt`, image cleanup, structured logging to `/var/log/c-network-agent-update.log`, and dry-run support.
- Add `internal/engine/doctor.go` — structured diagnostics: Docker installed/running, compose availability, Docker Hub and `core.crenein.com` connectivity, disk space, file permissions (`.env`, compose, certs, docker socket), agent service status, and recent log errors. Every check returns OK/WARNING/CRITICAL plus an actionable fix suggestion, in a structure consumable by the future TUI, CLI, and `--json` output.
- Cross-cutting: all operations accept `context.Context` as first parameter; all failures are structured errors carrying a user-facing fix suggestion; the whole engine is testable without a real Docker daemon, filesystem root, or network (interfaces + fakes).

## Capabilities

### New Capabilities
- `system-detection`: Detects CPU AVX support, distro, Docker/compose availability, connectivity, disk space, and permissions before any operation runs.
- `install-engine`: Installs the full C-Network agent stack on a clean or partially-installed Ubuntu/Debian VM, unifying both legacy install scripts behind automatic AVX detection.
- `update-engine`: Updates the agent backend and frontend containers to an explicit version with backup, health checks, and automatic rollback, without ever touching the databases.
- `doctor-engine`: Runs structured diagnostics over the host and the installed stack, returning per-check status and actionable fix suggestions.

### Modified Capabilities
- None.

## Impact

- Affected packages: `internal/detect`, `internal/engine`, `internal/compose`, `internal/dockerx` (all new). No changes to `cmd/`, `internal/tui`, `internal/release`, or `internal/selfupdate` — subcommands, TUI, and the versions.json client are separate changes (`add-headless-commands`, `add-tui-dashboard`, `add-selfupdate-version-manifest`). Depends on `add-cli-scaffold-distribution` for the repo skeleton.
- Client VM files touched (flagged per project rules): the install engine creates/modifies `/data/*`, `/etc/vsftpd.conf`, `/etc/default/tftpd-hpa`, `.env`, `docker-compose.yml`, and `certs/` under the install directory; the update engine modifies `docker-compose.yml`-managed containers, writes `.backups/${TIMESTAMP}/` and `/var/log/c-network-agent-update.log`. The doctor engine is strictly read-only.
- Coordination with `c-network-agent-back`: none required by this change. Pull-by-explicit-tag relies on the existing `crenein/c-network-agent-back:X.Y.Z` tags already published by `build-and-push.sh`. The `/health` version field and `versions.json` manifest are handled by `add-selfupdate-version-manifest`.
- Intentional divergences from the bash scripts (detailed in design): the InfluxDB token is generated randomly per install instead of the hardcoded value shipped in the scripts, and the Mongo image is chosen by automatic AVX detection (with override) instead of manual script selection.
- Rollback plan (this change ships engine code only — no client VM is touched until `add-headless-commands` exposes it, but the engine's own safety contract is part of this change):
  - **Code rollback**: revert the change; no migrations, no persisted state, no published artifacts are produced by this change alone.
  - **Install rollback**: install is idempotent and data-preserving — it MUST detect existing `/data/*` data and an existing `.env`/compose and never overwrite credentials or database files; a failed install can be re-run safely or cleaned by `docker compose down` (volumes and `/data` survive).
  - **Update rollback**: automatic — before recreate, image IDs are saved to `.backups/${TIMESTAMP}/image-state.txt` together with `docker-compose.yml` and `.env`; on failed health check the engine re-tags the previous image IDs and recreates `agent` and `frontend`. Manual recovery uses the same backup directory (last 5 retained).
  - **Doctor rollback**: not applicable (read-only).
