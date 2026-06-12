## Context

The CLI binary lives at `/usr/local/bin/crenein-agent` on client VMs, installed as root by `install.sh`. Releases are published by goreleaser to GitHub Releases of `PazNicolas/crenein-agent-tui` with per-platform archives and a `checksums.txt` (SHA256) — that pipeline is delivered by `add-cli-scaffold-distribution` and is a dependency of this change. The agent backend today versions itself only through `version.txt` (currently `1.8.3`) and `build-and-push.sh`, which tags `crenein/c-network-agent-back:latest` and `:X.Y.Z`; `GET /health` returns only `{"status","message"}` with no version. The sibling change `add-agent-health-version` in `c-network-agent-back` adds `"version"` to `/health` via `-ldflags` injection. The legacy `update-agent.sh` detects "what is running" by Docker image ID/digest only, and updates blindly to `:latest`.

## Goals / Non-Goals

**Goals:**
- `crenein-agent self-update` replaces the running binary safely (verify-then-swap, never verify-after-swap) with semver-aware checks, explicit pin/downgrade, and a check-only mode for cron/monitoring.
- A machine-readable `versions.json` manifest, published with every CLI release, that maps every agent and CLI version to its date, Docker image, Mongo image pairing, and release notes.
- `internal/release/` resolves "which agent version to install/update to" and "which Mongo image for this CPU" from the manifest, replacing blind `:latest`.
- The CLI/TUI know the running agent version (via `/health`, with a Docker fallback) and notify when CLI or agent updates are available.

**Non-Goals:**
- Do not implement the `/health` version field itself — that is `add-agent-health-version` in `c-network-agent-back` (coordination dependency only).
- Do not implement the update/rollback engine — `add-engine-detectors` owns `internal/engine/update.go`; this change only feeds it resolved versions, images, and notes.
- Do not build the goreleaser/workflow scaffold — `add-cli-scaffold-distribution` owns it; this change extends the release workflow with one manifest step.
- No auto-update daemon: self-update only runs when invoked (interactively, via cron with `--yes`, or via `--check`).
- No signature scheme beyond SHA256 (no Sigstore/GPG in this phase).

## Decisions

### AD-1: Atomic swap via temp file in the same directory + rename

`self-update` SHALL download and verify into a temporary file created **in the same directory as the target binary** (e.g. `/usr/local/bin/.crenein-agent.new-<random>`), `chmod 0755` it, then `os.Rename` it over the target. `rename(2)` is atomic only within a single filesystem; using the system temp dir (`/tmp`) would risk `EXDEV` cross-device failures and a non-atomic copy fallback. A stale temp file from a previous interrupted run is removed before starting, and the temp file is removed on any failure path (`defer`).

Rationale: at no point does a reader of `/usr/local/bin/crenein-agent` observe a partial binary — they see either the old or the new file. The running process keeps its old inode (Linux allows renaming over a running executable), so self-update from within the binary itself is safe.

Alternative considered: write to the target with `O_TRUNC` then restore on failure — rejected, because a crash mid-write leaves a corrupt binary with no recovery path on a remote client VM.

### AD-2: SHA256 verification is mandatory and happens before the swap

The release `checksums.txt` asset is the integrity source. `self-update` SHALL download `checksums.txt`, locate the line for the exact asset name for the current `GOOS/GOARCH`, stream-hash the downloaded asset, and compare. Any mismatch — or a missing checksum entry for the asset — aborts with a non-zero exit and the current binary untouched. There is no flag to skip verification.

Rationale: the repo is public and downloads traverse the open internet from ~20 client networks; checksums close the corrupted-download and (with HTTPS) most tampering windows. Making it non-optional removes the "it failed so I skipped it" foot-gun on client machines.

### AD-3: Permission handling — detect early, fail with a sudo suggestion, no privilege escalation

Before downloading anything, `self-update` SHALL resolve the real binary path (`os.Executable` + `filepath.EvalSymlinks`) and probe writability of its directory and the file itself. If not writable, it exits with code `1` and a message such as: `cannot write to /usr/local/bin/crenein-agent: permission denied — re-run with: sudo crenein-agent self-update`. The CLI does NOT self-elevate (no automatic sudo re-exec).

Rationale: `/usr/local/bin` is root-owned; most invocations on client VMs are already root (install/update require it), so the common path just works. Auto-re-execing under sudo from a downloaded binary is a security anti-pattern and breaks non-interactive cron usage. An early probe avoids wasting a download (and a rate-limited API call) only to fail at swap time.

### AD-4: Local version cache with 24h TTL to respect GitHub rate limits

Unauthenticated GitHub API calls are limited to 60/hour/IP. Release lookups (latest release metadata + manifest) SHALL be cached in `~/.crenein/version-cache.json` (`0600`, directory `~/.crenein/` created `0700` if missing) with a `fetched_at` timestamp and a 24h TTL. The cache stores the resolved latest CLI version, latest agent version, and the parsed manifest. `--force-check` (and `--version X.Y.Z` pinning, which must hit the API for that tag) bypass the cache; every successful live fetch rewrites it. A corrupt or unparseable cache file is treated as absent, not fatal. Background "update available" checks in the TUI read ONLY the cache or a fresh fetch that respects the TTL — the TUI never hammers the API on every render.

Rationale: 20 clients × TUI sessions × cron checks would burn the 60/h budget fast; a 24h TTL matches the practical release cadence and bounds staleness, with `--force-check` as the escape hatch.

### AD-5: `versions.json` schema and publication

The manifest is a single JSON document, generated by the CLI release workflow and attached as the `versions.json` asset on every GitHub Release of `PazNicolas/crenein-agent-tui`. The release tagged latest always carries the current manifest, so consumers fetch it from the latest release (`releases/latest` asset URL) without guessing tags. Schema (authoritative, also embedded in the `version-manifest` spec):

```json
{
  "agent": {
    "latest": "1.8.3",
    "releases": {
      "1.8.3": {
        "date": "2026-06-12",
        "image": "crenein/c-network-agent-back:1.8.3",
        "mongo": {"7": "mongodb/mongodb-community-server:7.0-ubuntu2204", "4": "mongo:4.4"},
        "notes": "Fixes bugs 1&2 Telegram"
      },
      "1.8.2": {
        "date": "2026-05-28",
        "image": "crenein/c-network-agent-back:1.8.2",
        "mongo": {"7": "mongodb/mongodb-community-server:7.0-ubuntu2204", "4": "mongo:4.4"},
        "notes": "anomaly: one notification per cycle"
      }
    }
  },
  "cli": {
    "latest": "0.1.0",
    "releases": {
      "0.1.0": {
        "date": "2026-06-12",
        "notes": "Initial release"
      }
    }
  }
}
```

Key points:
- `agent.releases` keys are exact backend versions (`version.txt` values); `image` is the fully qualified Docker tag — consumers never synthesize tags by string concatenation.
- `mongo` maps the major family to the exact image: `"7"` → image for AVX-capable CPUs, `"4"` → image for non-AVX CPUs. The values MUST match the images the install/update engine actually consumes (`mongodb/mongodb-community-server:7.0-ubuntu2204` and `mongo:4.4`, per the `system-detection` spec of `add-engine-detectors`). Per-release mapping (rather than global) lets a future agent release change its Mongo pairing without a schema break.
- `notes` is a short human-readable summary used verbatim in the update preview.
- The frontend (`crenein/c-network-agent-front`) is deliberately NOT versioned in `versions.json` in v1: updates keep pulling its `:latest` tag, matching the legacy flow. Versioning the frontend in the manifest is future work — a documented trade-off, not an omission.
- The workflow sources agent release data from a checked-in seed file in this repo (updated when the backend publishes a release) merged with the CLI's own tag being released; the workflow fails the release if the manifest does not validate against the schema.

Rationale: one file, one fetch, answers every version question the CLI has (what's latest, what image, which Mongo, what changed). Attaching it to releases reuses the existing distribution channel — no extra hosting, same availability as the binaries themselves.

Alternative considered: serving the manifest from a raw `main` branch URL — rejected because it decouples manifest state from release state (a half-merged main could advertise versions with no published binaries).

### AD-6: Running-agent version detection — `/health` first, Docker inspection fallback

To answer "what agent version is running", `internal/release/` SHALL:
1. Call the public root `GET /health` on the local backend — no `X-API-Key`, added by the coordinated `c-network-agent-back` change `add-agent-health-version` — probing `https://localhost:8000/health` with insecure TLS (self-signed certificate) and falling back to `http://localhost:8000/health`, mirroring the legacy probe order. If the response is HTTP 200 and the JSON includes a non-empty `"version"`, that wins. Legacy backends (e.g. `1.8.3`) predate the root endpoint and answer 404 there; a 404 (or any non-200) MUST route to the Docker fallback below and MUST NOT be misread as a healthy versionless response.
2. Otherwise, fall back to Docker inspection through `internal/dockerx`: read the running agent container's image reference; a tag matching `crenein/c-network-agent-back:X.Y.Z` yields `X.Y.Z`; a `:latest` tag yields the digest, reported as version `unknown (digest sha256:…)` and matched against nothing.
3. If both fail (agent down, Docker unreachable), the version is `unknown`, and update-available logic for the agent is suppressed (never claim "up to date" without evidence).

Rationale: `/health` is authoritative once `add-agent-health-version` ships, but the 20 existing installs will run pre-version backends for a while; the digest fallback keeps `status`/`update` honest during the transition without making the sibling change a hard blocker.

### AD-7: Exit codes and engine/UI separation

`self-update --check` is designed for cron/monitoring: exit `0` = up to date, exit `10` = update available (nothing modified), exit `1` = any error (network, parse, permissions). The plain `self-update` run exits `0` on success or already-up-to-date, `1` on any failure. All logic lives in `internal/selfupdate` and `internal/release` as UI-agnostic, `context.Context`-first functions returning structured results; both the cobra subcommand and the TUI render those results. HTTP and filesystem access sit behind narrow interfaces so unit tests run without network or a real binary swap (per project convention: engine testable without Docker/TUI).

Rationale: a distinct "update available" code (`10`) lets scripts distinguish actionable state from errors without parsing output; keeping it out of the 1–2 range avoids collision with generic shell/tool failures.

## Sequence

```text
crenein-agent self-update          GitHub API              filesystem
        |                              |                        |
        | resolve binary path, probe write access              |
        |----------------------------------------------------->|
        | cache fresh? (~/.crenein/version-cache.json, 24h)    |
        |--(stale or --force-check)--->|                        |
        |  GET releases/latest         |                        |
        |<-----------------------------|                        |
        | semver compare local vs latest                        |
        |--(newer)--> download asset for GOOS/GOARCH + checksums.txt
        |  write to /usr/local/bin/.crenein-agent.new-XXXX     |
        |----------------------------------------------------->|
        |  SHA256(tmp) == checksums.txt entry?  NO -> rm tmp, exit 1
        |  chmod 0755 tmp; rename(tmp, crenein-agent)  [atomic]|
        |----------------------------------------------------->|
        |  report "updated 0.1.0 -> 0.2.0"                      |
```

## Risks / Trade-offs

- **A broken CLI release propagates to client VMs** → highest-impact risk. Mitigations: SHA256 gate, atomic swap (no partial states), explicit `--version` downgrade path, 24h cache delays uptake (time to yank a bad release), and Phase 6 pilots before broad rollout.
- **Manifest drifts from backend reality** (workflow publishes a version whose image was never pushed) → manifest generation validates schema and the workflow treats validation failure as release failure; `update` refuses a malformed manifest instead of falling back to `:latest`.
- **GitHub rate limiting during incidents** (everyone runs `--force-check`) → acceptable: the error is explicit and retryable; cache absorbs normal operation.
- **`/health` reachable over self-signed TLS only** → reuse the legacy probe order (`https -k` then `http`), already proven by `update-agent.sh`.
- **Symlinked or relocated binary** → `EvalSymlinks` resolves the real target; the temp file is created next to the resolved path so the rename stays on one filesystem.
- **Stale 24h cache hides a fresh release** → bounded by TTL; `--force-check` documented in `self-update --help` and surfaced by the TUI's "last checked" timestamp.

## Migration Plan

1. Ship `add-cli-scaffold-distribution` (dependency): goreleaser + `checksums.txt` + release workflow must exist first.
2. Land this change: manifest generation in the release workflow, `internal/release` manifest client + version detection, `internal/selfupdate`, `self-update` subcommand, TUI notifications.
3. Coordinate `add-agent-health-version` in `c-network-agent-back`: once deployed to a client, `/health` version supersedes the Docker fallback automatically — no CLI change needed in either order.
4. Cut a real release with `versions.json` attached; validate on a test VM: fresh check, `--check` exit codes, full self-update, checksum-mismatch abort (tampered asset), permission-denied path (non-root), pinned downgrade.
5. Rollback: yank the bad release / republish a corrected `versions.json`; clients recover via `self-update --version <previous>` or the pinned `install.sh` one-liner. No client VM state beyond the binary and the cache file is ever modified by this change.

## Open Questions

- None. The manifest's agent-release seed data comes from the backend's existing `version.txt` history; automating backend→manifest sync can be a follow-up change.
