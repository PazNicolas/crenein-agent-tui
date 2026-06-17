# Next steps — crenein-agent

_Punto de retomada. Última actualización: 2026-06-17._

## Estado general

- Git en `main`. Sin worktrees divergentes.
- Gates verdes hoy: `go build ./...`, `gofmt -l .`, `go vet ./...`, `go test ./...`,
  `shellcheck install.sh` — todos sin observaciones. CI verde.
- CLI headless completo (install/update/doctor/status/logs/rollback/self-update) funcional
  y validado adversarialmente.
- TUI: Lotes 1–6 implementados y verificados (Lote 6 cerrado el 2026-06-17).
- **No queda código por escribir.** Todo lo pendiente es validación en VM cliente + release + archive.

## Changes (OpenSpec)

| Change | Progreso | Qué falta |
|--------|----------|-----------|
| `add-cli-scaffold-distribution` | 15/18 | Validación en VM (5.3–5.5). |
| `add-engine-detectors` | 70/71 | Validación en VM (7.8). |
| `add-headless-commands` | 39/41 | Validación en VM (8.4 / 9.4). |
| `add-selfupdate-version-manifest` | 27/31 | Release (1.5, 6.2, 6.4) + VM (6.3). |
| `add-tui-dashboard` | 37/38 | Validación en VM (7.4). |

## Trabajo que queda

### A) Implementación — COMPLETA ✅ (cerrada 2026-06-17)

- **Lote 6 del TUI (8.6 + 8.7).** Goldens NO_COLOR mono de las 5 vistas a nivel root model
  (`internal/tui/root_golden_test.go` + `testdata/TestRootModelGoldenMono_*.golden`); gates
  finales (`gofmt`/`vet`/`build`/`test`) verdes. `add-tui-dashboard` → 37/38.
- **selfupdate 5.3 / 5.4.** Línea "Last checked" en el Status view
  (`internal/tui/status_view.go: lastCheckedText()`), que surface `UpdatesInfo.LastChecked`
  (antes calculado y descartado). Verificado que el render no dispara llamadas a GitHub sin
  caché (`View()` puro; `FetchUpdatesInfo`→`FetchManifest(ctx, false)`, TTL 24h en disco).
- **cli-scaffold 3.4.** `shellcheck install.sh` con 0 findings (único hallazgo SC2317 sobre
  `cleanup`: falso positivo por invocación vía `trap EXIT`, silenciado con
  `# shellcheck disable=SC2317,SC2329`) + `bash -n install.sh`.

### B) Validación manual en VM cliente (Nicolás)

Tareas `[VALIDATE ON VM]` — instalación E2E, idempotencia, exit codes, no-AVX, compose v1,
terminales reales (SSH/screen/web console), self-update con checksum tampering, arm64.

- `add-cli-scaffold` 5.3–5.5 · `add-engine-detectors` 7.8 · `add-headless-commands` 8.4 / 9.4
- `add-tui-dashboard` 7.4 · `add-selfupdate` 6.3

Guía del round-trip: `test/integration/full_stack.sh`.

### C) Release + Cierre

- `add-selfupdate`: 1.5 / 6.2 / 6.4 requieren cortar un release/tag (con `versions.json`
  adjunto) y documentar coordinación con `add-agent-health-version` + procedimiento de rollback.
- Una vez B) validado en VM y release hecho → `archive` de los changes.

## Para retomar

Ya no hay implementación pendiente. Arrancar por la validación en VM (B) usando
`test/integration/full_stack.sh`, luego el release de selfupdate, luego `archive` de los changes.

### Notas de diseño vigentes (design.md add-tui-dashboard)
- **AD-2** single root model + view stack para navegación.
- **AD-3** el TUI consume el engine con los mismos calls que headless; progreso vía `tea.Msg`.
- **AD-4** la degradación de TERM se decide antes de arrancar bubbletea (gate en `cmd`).
- **AD-5** layout responsive desde `WindowSizeMsg`, un solo breakpoint (100 cols).
- **AD-6** indicadores de update-available degradan a "unknown" si el manifest no está.

### Deuda opcional anotada
- `install_view.go` no usa el `baseWizard` (sus tests acceden a campos internos; documentado).
- `logsView` no cancela su stream al navegar fuera (por diseño — el ring buffer acota memoria).
