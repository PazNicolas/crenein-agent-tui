## Context

Phases 2 and 3 deliver a UI-agnostic engine: `internal/engine` (install, update, doctor, backup/rollback), `internal/detect` (AVX, distro, Docker, compose v1/v2, connectivity, disk), `internal/dockerx` (docker / docker compose wrapper), and `internal/release` (GitHub Releases + `versions.json` manifest client). The headless subcommands already drive that engine with plain output. This change adds the interactive presentation layer: a full-screen dashboard that is the primary mode when `crenein-agent` runs with no arguments on a real terminal.

The audience is the non-technical client on a VM console: xterm over SSH, `screen`, or a poor cloud web console. The UI must therefore work at 80x24, degrade without color, and never be the only way to perform an operation.

## Goals / Non-Goals

**Goals:**
- One full-screen bubbletea program with five views: Status (home), Install wizard, Update wizard, Doctor, Logs.
- The TUI calls the exact same engine functions as the headless subcommands — zero business logic in `internal/tui`.
- Live progress for long-running operations (install, update, doctor, logs) without blocking the UI loop.
- Graceful degradation: non-TTY / `TERM=dumb` → helpful message, not a crash; `NO_COLOR` → monochrome; minimum 80x24.
- Deterministic teatest coverage per view.

**Non-Goals:**
- No engine, detect, or dockerx changes (owned by `add-engine-detectors`).
- No changes to headless subcommand contracts (owned by `add-headless-commands`).
- No self-update or manifest generation logic (owned by `add-selfupdate-version-manifest`); this change only consumes the manifest when present.
- No mouse support, no alternate themes, no i18n in this change (UI text is English/Spanish-neutral labels matching the bash scripts' summary content).

## Decisions

### AD-1: bubbletea (Elm architecture) + lipgloss for rendering

Use `github.com/charmbracelet/bubbletea` as the TUI runtime and `github.com/charmbracelet/lipgloss` for styling/layout, with `bubbles` components (spinner, viewport, textinput, table) where they fit.

Rationale: bubbletea's Elm architecture (`Model`, `Update(Msg) (Model, Cmd)`, `View() string`) makes every state transition an explicit, pure function of messages — which is exactly what teatest exploits for deterministic tests, and what keeps long-running engine work out of the render loop. lipgloss gives terminal-capability-aware styling (it honors `NO_COLOR` and color-profile detection out of the box). The ecosystem is pure Go, preserving the static cross-compiled binary from goreleaser.

Alternatives considered: `tview`/`tcell` (imperative widget tree; harder to test deterministically, styling less composable) and a hand-rolled ANSI renderer (cost without benefit). The plan already fixed bubbletea as a closed decision; this AD records why it is also the right one.

### AD-2: Single root model with a view stack for navigation

One `tea.Program` owns a root model that holds: shared context (engine handles, detected install dir, versions), the active view, and a small navigation stack. Each view is its own sub-model implementing the bubbletea triad. Navigation messages (`navigateToMsg{view}`, `navigateBackMsg{}`) are handled only by the root; views never switch each other directly.

Key map at the root: `s` Status, `i` Install, `u` Update, `d` Doctor, `l` Logs, `esc` back to Status, `q` / `ctrl+c` quit (with confirmation while an operation is running). Views receive all other keys.

Rationale: a single program avoids terminal mode churn (enter/exit alt-screen between "screens"), the stack gives predictable `esc` semantics, and centralizing the key map prevents views from shadowing global shortcuts. Wizards internally manage their own step index — steps are wizard state, not navigation state.

### AD-3: The TUI consumes the engine through the same calls as headless, with progress as tea.Msg

Engine operations are started inside `tea.Cmd` goroutines and stream progress through the engine's existing event mechanism (each engine operation accepts `context.Context` and emits step events on a channel, per the engine contract). The TUI adapts each channel event into a `tea.Msg` (`stepStartedMsg`, `stepProgressMsg`, `stepDoneMsg`, `stepFailedMsg`, `operationFinishedMsg`) via a listen-loop `tea.Cmd` that returns one message per receive, re-issuing itself until the channel closes.

Rationale: this is the standard bubbletea pattern for external event streams, it guarantees the headless subcommands and the TUI execute literally the same engine code path (the headless renderer consumes the same channel and prints lines), and it makes the UI testable by injecting fake engines that emit scripted event sequences. Cancellation maps cleanly: `q`/`ctrl+c` during an operation prompts, and confirmation cancels the operation's `context.Context`.

Behavior parity vs legacy bash: the install wizard's final access summary reproduces the data of `install-agent.sh`'s closing block (backend `https://<IP>:8000`, frontend `https://<IP>:443` / `http://<IP>:80`, admin `admin@example.com/admin123`, InfluxDB `http://<IP>:8086`, persistent data under `/data/*`, cert paths); the update wizard reproduces `update-agent.sh`'s backup → pull → recreate → health → (rollback) sequence, with the intentional divergence that the version and release notes are shown before confirmation instead of a blind `:latest` pull.

### AD-4: TERM degradation is decided before the program starts

`cmd` decides the mode before constructing the `tea.Program`:

1. stdout is not a TTY, or `TERM` is `dumb` or empty → do not start bubbletea; print a short plain-text notice listing the headless subcommands (`crenein-agent status|install|update|doctor|logs|self-update`) and exit `0` (it is guidance, not an error).
2. TTY present but window smaller than 80x24 → start the TUI and render a "terminal too small (have WxH, need 80x24)" screen until a `tea.WindowSizeMsg` reports a sufficient size.
3. `NO_COLOR` set (any value) or color profile is ASCII → force lipgloss to the no-color profile; status glyphs keep textual fallbacks (`[OK]`, `[WARN]`, `[FAIL]`) alongside or instead of `✅/⚠️/❌` so meaning never depends on color.

Rationale: checking before program start is the only way to avoid bubbletea's renderer touching a non-terminal (cron, pipes, provisioning tools); pointing to headless keeps automation users on the supported path. The in-TUI "too small" screen (instead of exiting) tolerates resizable cloud consoles.

### AD-5: Responsive layout from tea.WindowSizeMsg, single breakpoint

The root model stores width/height from `tea.WindowSizeMsg` and passes them to the active view. Layout rules: a persistent one-line header (app name, CLI version, active view) and one-line footer (context-sensitive key hints); the body gets `height-2`. At width ≥ 100 the Status view renders the service table beside the versions/updates panel (lipgloss `JoinHorizontal`); below 100 the panels stack vertically. Logs and Doctor detail use `bubbles/viewport` sized to the body. No other breakpoints.

Rationale: one breakpoint covers the real fleet (80-col consoles and modern wide terminals) without a layout-engine rabbit hole; everything must remain fully functional at exactly 80x24, so the narrow layout is the canonical one and teatest golden files are captured at 80x24.

### AD-6: Update-available indicators degrade to unknown

The Status view asks `internal/release` for the manifest (`versions.json` from the `PazNicolas/crenein-agent-tui` releases, 24h local cache in `~/.crenein/version-cache.json`). Three outcomes per component (CLI, agent): up-to-date, `update available → X.Y.Z`, or `version check unavailable` when the manifest cannot be fetched or does not exist yet (pre `add-selfupdate-version-manifest`). Fetch failures never block or delay rendering — the check runs as a background `tea.Cmd` and the indicator fills in when it resolves.

Rationale: this change ships before the manifest pipeline; specifying the consumer contract now (including the unknown state) means no TUI rework when `add-selfupdate-version-manifest` lands.

## Risks / Trade-offs

- Poor terminals in the field (cloud serial consoles, old `screen`) may still misrender → mitigate with the 80x24 canonical layout, no-color fallbacks, the pre-start TTY/TERM gate, and Phase 6 pilot validation on real client-like VMs.
- Long engine operations could leave the UI stale if an event channel stalls → the listen-loop pattern plus spinner ticks keeps the loop alive; operations honor `context.Context` cancellation.
- Golden files are sensitive to style changes → keep styles centralized in one `internal/tui/styles` package so visual tweaks touch a known set of goldens; capture goldens only at 80x24.
- Wizard state vs engine state drift (e.g., user resizes or navigates away mid-install) → navigation away from a running wizard is blocked; only the quit-confirmation path can cancel the operation context.

## Migration Plan

1. Land this change after `add-engine-detectors` (hard dependency) and after `add-headless-commands` has validated the engine in the field (conceptual dependency, per Phase 3 → Phase 4 order).
2. Ship in a CLI minor release; `crenein-agent` without arguments switches from printing help to opening the dashboard on TTYs. Non-TTY callers see the guidance notice, so existing scripts that invoked the bare binary (none are known) fail soft.
3. When `add-selfupdate-version-manifest` lands, the Status view indicators start resolving automatically — no TUI change required.
4. Rollback: revert the `cmd` dispatch and `internal/tui`; headless subcommands are unaffected.

## Open Questions

- None. View scope, keys, degradation rules, and the engine event contract are fixed by the plan and the engine change.
