## Why

Today the operator-facing experience for installing and updating the C-Network agent is two ~700-line bash scripts plus a blind `docker pull :latest` update, and ~20 client VMs are managed by non-technical users over SSH/RDP. Phases 2 and 3 deliver the engine (`internal/engine`, `internal/detect`) and the headless subcommands, but plain CLI output is still hostile to the non-technical client: no live service overview, no guided install, no version preview before update, and diagnostics that require reading raw terminal scrollback. The master plan defines a full-screen TUI dashboard as the primary mode of `crenein-agent` (Phase 4) so a client can install, update, diagnose, and watch logs through a guided visual interface, while scripts and cron keep using the headless subcommands.

## What Changes

- Add `internal/tui/` (bubbletea + lipgloss): running `crenein-agent` with no arguments opens a full-screen dashboard; running it with any subcommand keeps the existing headless behavior unchanged.
- Add a **Status view** (home): live state of the compose services (`agent`, `frontend`, `mongodb`, `influxdb`, `redis`) with status/version/uptime, the CLI version, the agent version, "update available" indicators (consumer integration with `add-selfupdate-version-manifest` when it lands), and keyboard navigation to the other views.
- Add an **Install wizard view**: progressive steps — system checks (detect), configuration form (credentials/ports/paths with defaults), preview of planned actions, live step-by-step execution progress, and a final access summary equivalent to the one printed by `install-agent.sh`. If an installation already exists, the wizard says so and refuses to overwrite it.
- Add an **Update wizard view**: current → available version with release notes ("1.8.3 → 1.8.4, changes: ..."), explicit confirmation, live progress (backup → pull → recreate → health), and a result screen that makes automatic rollback visible when health checks fail.
- Add a **Doctor view**: checklist running live with ✅/⚠️/❌ per item, detail plus fix suggestion when an item is selected, and re-run with a key.
- Add a **Logs view**: live follow of compose logs, filter by service, pause/scroll, configurable tail.
- Add **graceful degradation**: `TERM=dumb` or non-TTY prints a clear message pointing to the headless subcommands (no crash), minimum supported size 80x24, no color when `NO_COLOR` is set.
- Add **teatest coverage** (golden files / simulated interaction) for every view.

## Capabilities

### New Capabilities
- `tui-dashboard`: Full-screen bubbletea dashboard as the primary interactive mode of `crenein-agent`, with Status, Install wizard, Update wizard, Doctor, and Logs views, keyboard navigation, terminal degradation rules, and teatest coverage.

### Modified Capabilities
- None. The TUI is a new presentation layer over the existing engine; headless subcommand contracts are untouched.

## Impact

- Affected packages: `internal/tui` (new: dashboard root model, one sub-model per view, shared styles), `cmd` (root command opens the TUI when invoked without arguments and a TTY is present), and read-only consumption of `internal/engine`, `internal/detect`, `internal/dockerx`, and `internal/release`. No engine logic is added or modified here — the TUI calls the same engine functions the headless subcommands call.
- Dependencies: requires `add-engine-detectors` (engine + detect logic) and conceptually follows `add-headless-commands` (Phase 3 validates the logic before the UI). The "update available" indicators consume the manifest from `add-selfupdate-version-manifest`; until that change ships, the indicators degrade to "unknown" without error.
- Client VM files: the TUI itself writes nothing new — `.env`, `docker-compose.yml`, `/data/*`, certs, and `.backups/${TIMESTAMP}/` are only touched through the same engine operations already specified for install and update. Flagged because the wizards trigger those operations interactively.
- Coordination with `c-network-agent-back`: none required by this change directly. The Status view shows the agent version from the public root `GET /health` (no auth) once the coordinated `add-agent-health-version` backend change is deployed; legacy agents answer 404 on that route, and the view falls back to the image tag/digest or shows `unknown`.
- New Go dependencies: `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/bubbles`, and `github.com/charmbracelet/x/exp/teatest` (test-only). All are pure-Go and keep the static binary build.
- Rollback plan: the TUI is additive and isolated in `internal/tui` plus a small dispatch in `cmd`. Reverting the change restores the previous root-command behavior (help text), and all install/update/doctor/logs functionality remains available through the headless subcommands. No client VM state migration is involved; risky operations (install, update) reuse the engine's existing backup + automatic rollback guarantees.
