## Why

The engine and detector packages delivered by `add-engine-detectors` contain all the install/update/doctor logic ported from the legacy bash scripts, but there is no way to invoke them from a terminal yet. Phase 3 of the master plan requires a fully functional headless CLI **before** the TUI is built, so the ported logic can be validated end-to-end on real client-like VMs, used from cron jobs and CI scripts, and kept as the permanent automation surface once the TUI ships. Today, automation against the legacy `update-agent.sh` relies on fragile conventions (`--force` to skip prompts, colored text scraping, no JSON, no documented exit codes), which makes fleet-wide scripting across ~20 client VMs unreliable.

## What Changes

- Add `cobra` as the CLI framework and a `cmd/` package tree with the subcommands `install`, `update`, `doctor`, `status`, `logs`, and `rollback`, all wired to the existing `internal/engine` packages (no business logic in `cmd/`).
- `crenein-agent install`: plain interactive mode (sequential prompts, no TUI) when a TTY is present, and a fully non-interactive mode driven by `--yes` plus value flags / `CRENEIN_*` environment variables, compatible with `expect` and CI automation.
- `crenein-agent update`: feature parity with the legacy `update-agent.sh` flags (`--dry-run`, `--skip-frontend`, `--no-cleanup`, `--force`) plus the new `--yes` and `--version X.Y.Z` (explicit tag from the `versions.json` manifest; default is the manifest's latest).
- `crenein-agent doctor`: human-readable colored report and a `--json` mode with a stable, versioned output shape; exit codes encode the diagnosis (0 = all OK, 1 = warnings, 2 = critical checks failed).
- `crenein-agent status`: services, versions, and uptime of the compose stack, with optional `--json`.
- `crenein-agent logs [service]`: follow (`-f/--follow`) and tail (`--tail N`) of compose service logs, with per-service filtering.
- `crenein-agent rollback`: restore the previous agent/frontend images from the most recent `.backups/${TIMESTAMP}/` snapshot, with confirmation unless `--yes`.
- Global output conventions shared by every subcommand: `--json` disables colors and spinners; without a TTY no prompts are ever shown (missing input fails fast with a documented exit code); errors go to stderr and data to stdout; `--quiet` suppresses non-essential output; exit codes are documented per command and frozen as an automation contract.
- Integration tests: bash scripts invoke the compiled CLI and assert exit codes and JSON shapes with `jq`.

## Capabilities

### New Capabilities
- `headless-cli`: Defines the subcommand surface, flags, TTY/pipe behavior, stdout/stderr discipline, exit codes, and stable JSON output contracts of the `crenein-agent` binary when used without the TUI.

### Modified Capabilities
- None.

## Impact

- Affected packages: `cmd/` (new: root command plus one file per subcommand), `internal/engine` (consumed — install, update, doctor, rollback entry points), `internal/detect` (consumed — TTY-independent checks), `internal/dockerx` (consumed — compose logs/ps), `internal/release` (consumed — manifest resolution for `update --version`), `main.go`, `go.mod` (adds `spf13/cobra`, `golang.org/x/term`, a color library such as `fatih/color`).
- Client VM files: `install` writes `.env`, `docker-compose.yml`, certs, and `/data/*` directories; `update` and `rollback` write `.backups/${TIMESTAMP}/` and retag/recreate containers. All of these behaviors live in the engine (`add-engine-detectors`) — this change only exposes them; it adds no new file mutations of its own.
- Coordination with `c-network-agent-back`: none required for this change. `status` reads the agent version from `GET /health` when the deployed backend already exposes `version` and degrades to image-tag inspection otherwise (the `/health` version field is coordinated by `add-selfupdate-version-manifest`).
- Dependency: requires `add-engine-detectors` to be implemented (the subcommands are thin adapters over that engine). TUI (`add-tui-dashboard`) and self-update (`add-selfupdate-version-manifest`) are explicitly out of scope.
- Rollback plan: the CLI itself is additive — reverting the change removes the subcommands and restores the previous binary behavior; no client VM state depends on the CLI being present. Operations started by the CLI keep the engine's own safety net: `update` snapshots to `.backups/` before mutating and auto-rolls-back on failed health checks, and `crenein-agent rollback` (or the documented manual `docker tag` + `docker compose up -d --no-deps --force-recreate agent frontend` procedure) restores the previous images if a release misbehaves. JSON shapes carry a `schema_version` field so any future breaking output change is detectable by consumers instead of silently corrupting automation.
