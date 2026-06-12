## Context

`add-engine-detectors` ports the legacy bash logic (`install-agent.sh`, `install-agent-mongo4.sh`, `update-agent.sh`) into UI-agnostic Go packages under `internal/engine`, `internal/detect`, `internal/compose`, `internal/dockerx`, and `internal/release`. Phase 3 puts a command-line surface on top of that engine so the logic can be exercised, tested, and automated before any TUI exists. The same subcommands remain the permanent automation interface (cron, CI, fleet scripts) after `add-tui-dashboard` ships; running `crenein-agent` with no arguments will later open the TUI, while any subcommand stays headless forever.

The legacy `update-agent.sh` already defines a de-facto contract on client VMs: flags `--dry-run`, `--skip-frontend`, `--no-cleanup`, `--force`, colored log lines, confirmation prompt, log file at `/var/log/c-network-agent-update.log`. Operators and existing runbooks expect that behavior, so the CLI must offer parity where it matters and document every intentional divergence.

## Goals / Non-Goals

**Goals:**
- Expose `install`, `update`, `doctor`, `status`, `logs`, and `rollback` as cobra subcommands that call the engine and add zero business logic of their own.
- Make every subcommand safe to run from scripts: deterministic exit codes, stable `--json` shapes, no prompts without a TTY, errors on stderr, data on stdout.
- Keep flag parity with `update-agent.sh` so existing runbooks translate one-to-one.
- Provide plain interactive prompts (sequential, line-based) for `install` so a human can use the CLI before the TUI exists and on terminals where the TUI will never work.

**Non-Goals:**
- No TUI rendering (bubbletea/lipgloss) — that is `add-tui-dashboard`.
- No install/update/doctor/rollback internals — those requirements live in `add-engine-detectors`; this change specifies only how they are invoked and how results are presented.
- No `self-update` subcommand and no `versions.json` publishing — that is `add-selfupdate-version-manifest`.
- No remote/fleet orchestration; the CLI operates on the local VM only.

## Decisions

### AD-1: Use cobra for the subcommand tree

Use `spf13/cobra` with one file per subcommand under `cmd/` (`cmd/root.go`, `cmd/install.go`, `cmd/update.go`, `cmd/doctor.go`, `cmd/status.go`, `cmd/logs.go`, `cmd/rollback.go`). Persistent flags on the root command carry the global conventions (`--quiet`, `--no-color`); per-command flags carry the operation parameters. `SilenceUsage` and `SilenceErrors` are enabled so error rendering and exit codes stay under our control instead of cobra's defaults.

Rationale: cobra is the de-facto Go standard (kubectl, docker, gh), gives `--help` generation, flag parsing, and shell completion for free, and its command tree maps one-to-one onto the planned TUI views, keeping the mental model identical across both modes.

Alternative considered: stdlib `flag` with manual dispatch — fewer dependencies but reimplements help text, nested flags, and usage errors that cobra already solves; not worth it for six subcommands that will grow (`self-update` is already planned).

### AD-2: Strict engine/presentation separation

Each `cmd/*.go` file is a thin adapter: parse flags → build an engine request struct → call the engine with a `context.Context` → render the engine's typed result. Engines never print; they return typed results and report progress through a narrow interface (e.g. `engine.Reporter` with `Step`, `Info`, `Warn` callbacks) that the CLI implements twice: a human presenter (colored lines, progress text) and a machine presenter (silent until the final JSON document). Prompting is also inverted: the engine declares the inputs it needs (`InstallParams` with unset fields); the CLI resolves each one through the precedence chain flag → environment variable → interactive prompt (TTY only) → default, and the engine receives only fully-resolved values.

Rationale: this is the contract the whole project rests on (`config.yaml`: "Both the TUI and the headless subcommands run the SAME engine"). If `cmd/` contained logic, `add-tui-dashboard` would have to duplicate it. It also keeps engines testable without a terminal and the CLI testable with a fake engine.

### AD-3: Stable, versioned output contracts for automation

Machine-readable output is a public API. Every `--json` document carries a top-level `schema_version` (integer, starting at `1`); fields are only ever added, never renamed or removed, within the same schema version. Exit codes follow a single global table (see the spec) with one deliberate exception: `doctor` encodes the diagnosis itself (0 = OK, 1 = warnings, 2 = critical), because that is the single most useful thing a cron job can branch on. Usage errors exit with code `64` (`EX_USAGE`) on every command — including `doctor` — so they can never be confused with a semantic result. Exactly one JSON document is written to stdout per invocation; progress, prompts, warnings, and errors go to stderr, so `crenein-agent doctor --json | jq .` always parses.

Rationale: 20 client VMs will be driven by scripts written once and rarely revisited. The cheapest way to keep them working is to freeze the contract in the spec, version the shape, and test it with `jq` in CI. Reserving 64 for usage keeps the 0/1/2 doctor semantics unambiguous.

Alternative considered: reusing exit 2 for usage errors (Go `flag` convention) — rejected because `doctor` needs 2 for "critical checks failed" per the product requirement, and a split convention (2 = usage everywhere except doctor) is exactly the kind of trap this contract exists to avoid.

### AD-4: TTY detection governs prompts, color, and spinners

On startup the CLI checks `term.IsTerminal` independently for stdin, stdout, and stderr:
- Prompts require an interactive stdin AND stderr. Without them, any value that would have been prompted for becomes a hard usage error (exit 64) naming the missing flag/env var — the process never blocks waiting for input that will not come.
- Color and spinners require a terminal on the target stream and are additionally disabled by `--json`, `--no-color`, `--quiet`, and the `NO_COLOR` environment variable (any non-empty value).
- Confirmations (update, rollback) are skipped by `--yes`; without `--yes` and without a TTY they fail with exit 64 instead of assuming consent.

Rationale: silently hanging on a hidden prompt is the worst failure mode for cron — the legacy scripts have exactly this problem when run non-interactively without `--force`. Failing fast with a named missing input makes automation errors self-diagnosing. Honoring `NO_COLOR` and pipe detection means log files never fill with ANSI escapes.

### AD-5: Parity with the legacy bash scripts, with documented divergences

| Behavior | `update-agent.sh` (legacy) | `crenein-agent update` | Why |
|---|---|---|---|
| `--dry-run`, `--skip-frontend`, `--no-cleanup` | yes | identical semantics | runbook compatibility |
| Skip confirmation | `--force` | `--yes` (`--force` no longer implies consent) | separates "don't ask" from "do it even if unchanged"; cron jobs use `--yes` |
| Proceed when image unchanged | `--force` | `--force` | identical |
| Target version | always `:latest` (blind) | manifest latest by default, `--version X.Y.Z` explicit | the core product improvement |
| Confirmation without TTY | hangs or misreads | exit 64 unless `--yes` | AD-4 |
| Backup, `--no-deps --force-recreate agent frontend`, health checks, auto-rollback, prune, keep-5 retention | yes | identical (engine) | safety parity |
| Log file `/var/log/c-network-agent-update.log` | yes | identical format `[ts] LEVEL: msg`, written by the engine | existing monitoring greps it |
| Colored stdout report | yes | yes (human mode only) | operator familiarity |

The `--force`/`--yes` split is the only behavioral divergence and MUST be called out in `update --help`.

Note on the health check target: the backend check uses the public root `GET /health` (no `X-API-Key`) introduced by the `c-network-agent-back` change `add-agent-health-version`, probed as `https://localhost:8000/health` (insecure TLS) with fallback to `http://localhost:8000/health`. One legacy bug is intentionally not ported: `update-agent.sh` accepted any HTTP response — including a 404 — as success. The engine requires HTTP 200; for legacy images that predate the root endpoint it falls back to checking the agent container state and logs a warning (see the `update-engine` spec in `add-engine-detectors`).

## Risks / Trade-offs

- A frozen exit-code/JSON contract constrains future refactors → mitigated by `schema_version` and additive-only evolution; a breaking change requires bumping the version and a new spec change.
- `--force` no longer skipping confirmation may surprise operators of the old script → mitigated by help text, and by `--force` without `--yes` on a TTY still showing the confirmation (it only ever fails closed, never executes unexpectedly).
- Plain-mode `install` prompts must stay automation-friendly (expect scripts) → prompts are written to stderr one per line with a stable `key [default]: ` format and never reordered within a schema version.
- Integration tests need Docker-capable environments → the suite is split: contract tests (flags, exit codes, JSON shape on `--dry-run`/fake engine) run anywhere; full-stack tests are flagged for a client-like VM.

## Migration Plan

1. Land after `add-engine-detectors` is implemented and verified (hard dependency — the subcommands compile against engine entry points).
2. No client-side migration: the binary distributed by `add-cli-scaffold-distribution` simply gains subcommands on the next release.
3. Legacy bash scripts remain authoritative on client VMs until Phase 6; nothing in this change deprecates them.
4. Rollback: revert the change and ship the previous binary; no persisted state on client VMs references the new subcommands.

## Open Questions

- None. Flag surface, exit codes, and JSON shapes are fixed by the spec in this change; engine semantics are fixed by `add-engine-detectors`.
