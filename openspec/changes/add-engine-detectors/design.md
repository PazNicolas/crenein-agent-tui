## Context

Three battle-tested bash scripts run the entire operational lifecycle of the C-Network agent on client VMs. They encode years of accumulated fixes: InfluxDB bucket creation has a 4-method fallback chain, updates recreate only `agent` and `frontend` with `--no-deps` so databases never restart, and backups retain image IDs for rollback. The scripts' weaknesses are structural, not logical: duplicated install variants per MongoDB version, blind `:latest` pulls, no machine-readable diagnostics, and zero testability. This change ports the logic to Go packages that the future CLI subcommands and TUI will share. The legacy scripts in `c-network-agent-back` remain the behavioral reference; the technical report extracted from them is the source of truth for commands, paths, env vars, and timeouts.

## Goals / Non-Goals

**Goals:**
- One engine, two consumers: `internal/engine` exposes pure operations that both cobra subcommands and the bubbletea TUI will invoke without modification.
- Full behavior parity with the bash scripts except where a divergence is explicitly decided below.
- Every engine package unit-testable without Docker, root, or network access.
- Structured progress and results so the TUI can render steps and `--json` can serialize reports.

**Non-Goals:**
- No cobra subcommands, no TUI, no `--json` flag wiring (those changes consume this engine).
- No GitHub Releases client, no `versions.json` manifest, no self-update (`add-selfupdate-version-manifest`).
- No deprecation of the bash scripts (Phase 6 of the master plan).
- No support for distros other than Ubuntu/Debian (matches the scripts).

## Decisions

### AD-1: Engine is pure and UI-agnostic; progress flows through a callback interface

`internal/engine` functions never print, never prompt, and never read stdin. Each long-running operation takes a `Reporter` (or equivalent event sink) interface that receives step-started/step-finished/warning events, and returns a structured result (`InstallResult`, `UpdateResult`, `DoctorReport`). Interactive decisions (e.g. "existing installation found, proceed?") are resolved BEFORE the engine runs, via an options struct (`InstallOptions{ForceMongoImage, AdminEmail, ...}`, `UpdateOptions{Version, DryRun, SkipFrontend, Force, NoCleanup}`).

Rationale: the master plan's core requirement is that TUI and headless subcommands run the SAME engine. Any `fmt.Println` or prompt inside the engine would break the TUI and make `--json` output impossible. This also makes table-driven tests trivial.

Alternative considered: returning a channel of events. Rejected — a callback interface is simpler to fake in tests and does not impose goroutine lifecycle management on callers.

### AD-2: All external effects go through narrow interfaces (`dockerx`, filesystem, network, command runner)

`internal/dockerx` defines a `Client` interface (compose up/ps/pull/exec, image inspect/tag/prune, daemon ping) implemented by shelling out to the real CLIs, plus a `CommandRunner` seam for `apt-get`, `systemctl`, `openssl`, `useradd`, etc. Filesystem access for VM paths (`/data`, `/etc/vsftpd.conf`, `.env`) goes through a small `FS` interface (or rooted abstraction) and HTTP probes through an injected `http.Client`/prober. The engine never calls `exec.Command` or `os.WriteFile` directly.

Rationale: this is the project convention ("shell out only through internal/dockerx"; "engine testable without Docker") and the only way to unit-test root-only flows like vsftpd configuration on a developer laptop. Fakes record invocations so tests can assert exact command sequences against the bash reference.

Alternative considered: Docker SDK (`github.com/docker/docker/client`) instead of CLI shelling. Rejected for v1: compose v1/v2 dispatch, `docker exec influx ...` and image tag/prune semantics are already proven via CLI in the scripts; the SDK adds a heavy dependency and diverges from the audited command surface. The `Client` interface leaves the door open.

### AD-3: Compose file is a `go:embed` text/template, not a generated string

`internal/compose` embeds one `docker-compose.yml.tmpl` rendered with `text/template` over a typed params struct (`MongoImage`, bucket names, etc.). The template carries verbatim the services, images, ports, volumes, env vars, network `agent-network`, and named volumes `mongodb_data`/`influxdb_data`/`redis_data` from the legacy scripts. Credentials are NOT rendered into the compose file — they stay in `.env` exactly as the scripts do (`${REDIS_PASSWORD}` style interpolation by compose).

Rationale: a single template with one substitution point (the Mongo image) is what collapses the two 700-line install scripts into one code path. Embedding guarantees the static binary needs no companion files. Keeping secrets in `.env` preserves the scripts' contract and lets `update` back up credentials independently of the compose file.

### AD-4: MongoDB image selection is automatic via AVX detection, with explicit override

`detect.AVX(ctx)` reads `/proc/cpuinfo` and reports whether any processor lists the `avx` flag. Install selects `mongodb/mongodb-community-server:7.0-ubuntu2204` when AVX is present and `mongo:4.4` otherwise. `InstallOptions.MongoImageOverride` lets the operator force either. Update NEVER changes the Mongo image: it reads the current image from the existing `docker-compose.yml` (fallback: `docker inspect` of the running container) and treats it as immutable.

Rationale: MongoDB >= 5.0 requires AVX; today the client chooses between two scripts manually with a 50/50 failure mode. Automatic detection removes the foot-gun; the override covers exotic cases (e.g. cpuinfo unavailable in some virtualized environments). Mongo immutability on update matches the scripts and prevents accidental data-format jumps.

### AD-5: InfluxDB token is generated randomly per install (intentional divergence)

The bash scripts hardcode `INFLUX_TOKEN=24dee4f6135c7b08643362e4c1fe9b313b045843f02263fd632d20d380ad1ffa` — the same admin token on every client VM, a known security problem. The Go installer generates a cryptographically random token per installation and writes it to `.env` (`INFLUXDB_TOKEN`, `DOCKER_INFLUXDB_INIT_ADMIN_TOKEN`). Existing installations are unaffected: update reads `.env` and never regenerates credentials, so backwards compatibility with the hardcoded token is preserved on already-installed VMs.

Rationale: shipping a shared secret in a public repo is unacceptable, and the project rule forbids embedding secrets. The fallback-chain bucket logic already parameterizes the token, so randomness costs nothing.

### AD-6: Update pulls an explicit version tag, never blind `:latest` (intentional divergence)

`UpdateOptions.Version` is required (e.g. `1.8.4`) and the engine pulls `crenein/c-network-agent-back:X.Y.Z`. Resolution of "what is the latest version" belongs to `internal/release` (separate change); this engine only consumes a concrete tag. After pull, the engine compares image IDs and exits as a successful no-op when nothing changed (unless `Force`).

Rationale: the single biggest operational risk today is `docker pull ...:latest` with unknown content. Explicit tags make updates reproducible, auditable in `image-state.txt`, and roll-back-able to a named version.

### AD-7: Doctor returns a typed report, not formatted text

`doctor.Run(ctx, deps)` returns a `DoctorReport{Checks []Check, Summary Status}` where each `Check` has `ID`, `Name`, `Status` (OK/WARNING/CRITICAL), `Detail`, and `FixSuggestion` (e.g. `sudo usermod -aG docker $USER`). Rendering (colors, emoji, `--json`) is the consumer's job.

Rationale: the same report must drive three frontends (TUI list, colored CLI output, JSON for automation). Encoding presentation in the engine would violate AD-1.

### AD-8: Structured errors with fix suggestions

A shared error type (e.g. `engine.Error{Op, Cause, FixSuggestion}`) wraps every failure that a client-facing user can act on. Detection failures (no AVX info, unsupported distro, no docker socket access) and operation failures (pull failed, health check timed out) all carry an actionable suggestion.

Rationale: project convention ("structured errors with user-facing fix suggestions") and the doctor/TUI need to display remediation, not stack traces.

## Behavior parity: bash → Go

| # | Bash behavior (scripts) | Go behavior (this change) | Parity |
|---|---|---|---|
| 1 | `/etc/os-release` `ID` must be `ubuntu`/`debian`, else fatal | `detect.Distro` same rule | Same |
| 2 | Manual choice between two install scripts for Mongo 7.0 vs 4.4 | Automatic AVX detection in `/proc/cpuinfo` + override | **Divergence (AD-4)** |
| 3 | apt packages: apt-transport-https, ca-certificates, curl, gnupg, lsb-release, fping, vsftpd, tftpd-hpa, jq | Same list via CommandRunner (jq no longer needed at runtime but kept for operator parity) | Same |
| 4 | Docker install: GPG key from download.docker.com, official repo, docker-ce + cli + containerd.io + docker-compose-plugin, systemctl start/enable | Same sequence, skipped when Docker already present | Same |
| 5 | `/data/{mongodb,influxdb2,redis}` 1000:1000, `/data/files` tftp:tftp, never touch dirs with existing data | Same | Same |
| 6 | vsftpd/tftpd-hpa config downloaded from cnetworkspace.nyc3.digitaloceanspaces.com, fallback to inline default | Same URLs, defaults embedded via go:embed | Same |
| 7 | `backups` user, home `/data/files`, 755 | Same | Same |
| 8 | `.env` chmod 600, Mongo `cnetwork_admin` + random 32-char password, random 32-char Redis password | Same | Same |
| 9 | `INFLUX_TOKEN` hardcoded shared value | Random token per install; existing `.env` never regenerated | **Divergence (AD-5)** |
| 10 | Certs: openssl RSA 4096, 365 days, subj `/C=US/ST=State/L=City/O=Crenein/OU=IT/CN=localhost`, SANs localhost/*.localhost/127.0.0.1/0.0.0.0, cert 644 / key 600 | Same parameters (via CommandRunner or crypto/x509 producing equivalent certs) | Same |
| 11 | compose v3.8: agent (8000/8443), frontend (80/443), mongodb (not exposed), influxdb (8086), redis (not exposed, appendonly + requirepass) with exact env vars | Same, single go:embed template, Mongo image parameterized | Same |
| 12 | `docker compose up -d` + sleep 10 + `docker ps --filter` per service | Same via dockerx, polling instead of fixed sleep where safe | Same (mechanism refined) |
| 13 | Admin user `POST https://localhost:8000/api/v1/admins/register` (`admin@example.com`/`admin123`), 3 retries, 3s apart, manual instructions on failure | Same | Same |
| 14 | Influx buckets `fping`+`devices`: 4-method fallback (REST orgs ×10 → CLI org list ×5 → CLI bucket create → REST bucket create ×5), warn + manual instructions if all fail | Same chain, same retry counts, jq replaced by native JSON parsing | Same |
| 15 | Influx health `GET :8086/health` expects `"status":"pass"`, 60s timeout, 3s interval | Same | Same |
| 16 | Update pre-flight: root, docker daemon, find_install_dir CWD→/root→/home/*, `.env` exists, ≥2048 MB free, connectivity to registry-1.docker.io + hub.docker.com (10s timeout) | Same | Same |
| 17 | `docker pull crenein/c-network-agent-back:latest` | Pull explicit `:X.Y.Z` tag | **Divergence (AD-6)** |
| 18 | Backup `.backups/${TIMESTAMP}/` (compose, `.env` 600, image-state.txt), keep last 5 | Same | Same |
| 19 | Recreate `docker compose up -d --no-deps --force-recreate agent frontend`; Mongo never changed | Same | Same |
| 20 | Backend health 60s/3s; rollback = re-tag IDs from image-state.txt + recreate | Same, automated identically | Same |
| 21 | `docker image prune -f` unless `--no-cleanup`; log to `/var/log/c-network-agent-update.log` `[YYYY-MM-DD HH:MM:SS] LEVEL: msg` | Same | Same |
| 22 | Interactive `S/n` confirmation inside the script | Engine takes pre-resolved options; confirmation handled by CLI/TUI consumers | **Divergence (AD-1)** |
| 23 | compose v2 plugin required; v1 unsupported | Both supported: detection + dispatch in dockerx, v2 preferred | **Divergence (improvement)** |

## Risks / Trade-offs

- **Porting drift**: a missed flag or path silently changes client VMs → mitigate with the parity table above as review checklist, command-sequence assertions in fake-based tests, and Phase 6 pilot installs side-by-side with the bash scripts.
- **CommandRunner surface grows large** (apt, systemctl, openssl, useradd, chown) → keep it a dumb `Run(ctx, name, args...)` seam; semantic helpers live in the engine, not the interface, so fakes stay one type.
- **Compose v1 support is new, untested territory** (the scripts require v2) → dockerx dispatches the binary name only; argument shape is identical for the subcommands used (`up`, `ps`, `pull`, `exec`). Flag a real-VM validation task on a compose-v1 host.
- **Random Influx token vs fleet uniformity**: support tooling that assumed the shared token will break on NEW installs only → acceptable; documented divergence, and `.env` on existing installs is never rewritten.
- **Native cert generation vs openssl CLI**: crypto/x509 removes an external dependency but risks subtle differences (extensions, key usage) → decision deferred to implementation with the constraint that SANs, key size, validity, and file modes match the spec exactly; openssl CLI via CommandRunner is the safe default.

## Migration Plan

1. This change merges engine + detectors with unit tests only — nothing executes on client VMs yet.
2. `add-headless-commands` wires cobra subcommands to the engine; first real-VM validation happens there.
3. `add-tui-dashboard` and `add-selfupdate-version-manifest` build on the same engine.
4. Legacy bash scripts remain the production path until Phase 6 pilot completes; rollback at any point is "keep using the scripts".

## Open Questions

- None blocking. Cert generation backend (openssl CLI vs crypto/x509) is an implementation-time choice constrained by the spec; the InfluxDB initial admin password (`adminpassword`) is kept as-is for parity and flagged for a future hardening change.
