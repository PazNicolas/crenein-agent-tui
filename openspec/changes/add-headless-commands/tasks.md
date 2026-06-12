## 1. CLI Scaffold And Global Conventions

- [ ] 1.1 Add `spf13/cobra`, `golang.org/x/term`, and a color library to `go.mod`; create `cmd/root.go` with the root command, `SilenceUsage`/`SilenceErrors`, and persistent flags `--quiet` and `--no-color`.
- [ ] 1.2 Implement the exit code mapper: typed CLI errors (`usage`, `preflight`, `aborted`, `rolled-back`, `rollback-failed`) translated to the global table (0/1/3/4/5/6/64) in a single place in `main.go`/`cmd/root.go`.
- [ ] 1.3 Implement TTY detection helpers (stdin/stdout/stderr via `term.IsTerminal`) and the decoration policy (color/spinner off when non-TTY, `--json`, `--no-color`, `--quiet`, or `NO_COLOR` set).
- [ ] 1.4 Implement the dual presenter over the engine `Reporter` interface: human presenter (colored steps to stderr) and machine presenter (silent, JSON-at-end); enforce data-to-stdout / everything-else-to-stderr.
- [ ] 1.5 Implement the input resolver with precedence flag > env > TTY prompt > default, including the `<label> [<default>]: ` prompt format on stderr and the exit-64 "missing inputs" error listing flag + `CRENEIN_*` env names.
- [ ] 1.6 Unit tests for 1.2-1.5 (no Docker, no TTY required: use injected readers/writers and fake terminals).

## 2. Install Subcommand

- [ ] 2.1 Create `cmd/install.go` with flags `--yes`, `--dir`, `--mongo {auto|7|4}`, `--api-url`, `--api-token`, `--admin-email`, `--admin-password` and env bindings `CRENEIN_INSTALL_DIR`, `CRENEIN_MONGO_MAJOR`, `CRENEIN_API_URL`, `CRENEIN_API_TOKEN`, `CRENEIN_ADMIN_EMAIL`, `CRENEIN_ADMIN_PASSWORD`.
- [ ] 2.2 Wire the interactive plain flow: sequential prompts in stable order, resolved-values summary with masked secrets, final `Proceed? [y/N]` confirmation (exit 4 on decline).
- [ ] 2.3 Wire the non-interactive flow (`--yes`): defaults for unset values, no prompts, exit 64 when promptable input is needed without TTY and without `--yes`.
- [ ] 2.4 Map engine pre-flight results (root, distro, disk, connectivity, AVX-vs-`--mongo 7` refusal) to exit 3 with fix suggestions on stderr; success prints the access summary to stdout.
- [ ] 2.5 Unit tests with a fake engine: flag/env precedence, prompt order, decline path, missing-input errors, AVX auto selection display.

## 3. Update Subcommand

- [ ] 3.1 Create `cmd/update.go` with flags `--yes`, `--version X.Y.Z`, `--dry-run`, `--skip-frontend`, `--no-cleanup`, `--force`; document the `--force` vs `--yes` divergence from `update-agent.sh` in the help text.
- [ ] 3.2 Resolve the target version: manifest latest by default, `--version` validated against the manifest (suggest near versions on miss), `--force` bypassing validation / falling back to `:latest` when the manifest is unreachable.
- [ ] 3.3 Implement the confirmation step (`1.8.3 -> 1.8.4` plus release notes; skip with `--yes`; exit 64 without TTY and without `--yes`; exit 4 on decline) and `--dry-run` plan printing (side-effect free, exit 0, no confirmation).
- [ ] 3.4 Map engine outcomes to exit codes: 0 success / already-up-to-date, 1 pre-mutation failure, 3 pre-flight, 5 rolled back, 6 rollback failed (with manual recovery commands on stderr).
- [ ] 3.5 Unit tests with a fake engine covering every exit code and the `--skip-frontend` / `--no-cleanup` / `--force` pass-through to the engine request.

## 4. Doctor Subcommand

- [ ] 4.1 Create `cmd/doctor.go` with `--json`; human renderer: one line per check with status marker, message, and fix suggestion; final totals summary line.
- [ ] 4.2 Implement the JSON document (schema_version 1): `command`, `timestamp`, `cli_version`, `summary{status,total,passed,warnings,critical,skipped}`, `checks[]{id,name,severity,status,message,fix,duration_ms}` with stable check ids.
- [ ] 4.3 Implement exit code mapping 0/1/2 from the worst finding (usage errors stay 64); dependent checks render as `skip` when a prerequisite check fails.
- [ ] 4.4 Unit tests with fake check results: all-pass, warnings-only, critical, skip propagation, JSON field completeness, exit codes with and without `--json`.

## 5. Status Subcommand

- [ ] 5.1 Create `cmd/status.go` with `--json`; human renderer: install dir, agent version (+source), mongo flavor, aligned service table (name, image, state, health, uptime).
- [ ] 5.2 Implement version resolution order `/health` `version` field -> image tag -> `unknown`, and the JSON document (schema_version 1) with `agent{}`, `mongo{}`, and the fixed five-element `services[]` (missing containers as `state: "missing"`).
- [ ] 5.3 Implement exit codes: 0 all running, 1 degraded, 3 no installation (suggest `crenein-agent install` on stderr).
- [ ] 5.4 Unit tests with a fake engine/dockerx: healthy, degraded, missing service, no install, version-source degradation.

## 6. Logs Subcommand

- [ ] 6.1 Create `cmd/logs.go` with positional `[service]`, `-f/--follow`, `--tail N` (default 100); validate service against {agent, frontend, mongodb, influxdb, redis} (exit 64 with the valid list on unknown).
- [ ] 6.2 Delegate to the detected compose binary via `internal/dockerx`, passing `--no-color` when stdout is not a TTY or color is disabled; stream to stdout.
- [ ] 6.3 Handle SIGINT/SIGTERM in follow mode: terminate the child compose process and exit 0.
- [ ] 6.4 Unit tests for argument validation and compose invocation arguments (fake dockerx); signal-handling test.

## 7. Rollback Subcommand

- [ ] 7.1 Create `cmd/rollback.go` with `--yes`, `--backup <TIMESTAMP>`, `--list`; `--list` prints snapshots (most recent first) read-only.
- [ ] 7.2 Wire snapshot selection (latest by default, `--backup` validated with exit 64 + available timestamps on miss) and the pre-action confirmation showing timestamp and images to restore (exit 4 on decline, exit 64 without TTY and without `--yes`).
- [ ] 7.3 Map engine outcomes: 0 restored + health OK, 1 rollback failed or post-rollback health failed, 3 no backups / no installation.
- [ ] 7.4 Unit tests with a fake engine: list, latest selection, explicit selection, decline, no-backups, health-failure paths.

## 8. Integration Tests (bash + jq)

- [ ] 8.1 Create `test/integration/` harness: build the binary, run scripted invocations with stdin from `/dev/null`, capture stdout/stderr separately, assert exit codes.
- [ ] 8.2 Contract tests without Docker: usage errors (64), `update --dry-run`, missing-input failures for `install`/`update`/`rollback` without TTY, `--quiet` behavior, no-hang guarantee (timeout wrapper).
- [ ] 8.3 JSON shape tests with `jq -e`: `doctor --json` and `status --json` schema_version, enum membership, and all documented fields/types.
- [ ] 8.4 Full-stack tests (marked, Docker required): install + status + doctor + logs + update + rollback round-trip on a disposable VM. [VALIDATE ON CLIENT-LIKE VM: no AVX, compose v1, poor TERM]
- [ ] 8.5 Wire contract tests (8.2-8.3) into CI; document how to run the full-stack suite manually.

## 9. Validation And Documentation

- [ ] 9.1 Run `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`.
- [ ] 9.2 Verify every spec scenario in `specs/headless-cli/spec.md` against the implementation, especially the exit code table and JSON field contracts.
- [ ] 9.3 Update the repo README with the subcommand reference: flags, exit codes per command, JSON shapes, and automation examples (`cron`, `jq`).
- [ ] 9.4 Manually exercise the plain interactive install prompts and the update confirmation on a real terminal. [VALIDATE ON CLIENT-LIKE VM: no AVX, compose v1, poor TERM]
