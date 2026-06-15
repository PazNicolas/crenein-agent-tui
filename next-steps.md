# Next steps — crenein-agent

_Punto de retomada. Última actualización: 2026-06-15._

## Estado de los changes (OpenSpec)

| Change | Progreso | Estado |
|--------|----------|--------|
| `add-cli-scaffold-distribution` | 14/17 | código completo; faltan validaciones en VM (5.3–5.5) |
| `add-engine-detectors` | 49/50 | código completo; 7.8 = validación VM |
| `add-headless-commands` | 39/41 | ✅ completo + validado (judgment-day); faltan 8.4/9.4 `[VALIDATE ON VM]` |
| `add-selfupdate-version-manifest` | 25/31 | casi cerrado; faltan 5.3/5.4 (dependen del TUI) y 1.5+6.2–6.4 (release/VM) |
| `add-tui-dashboard` | 35/38 | Lotes 1–5 ✅ verificados; restan solo Lote 6 (8.6/8.7 cierre) y 7.4 `[VALIDATE ON VM]` manual |

Todo lo anterior está commiteado y pusheado a `main` (último: `52e3a35`), con `build`/`vet`/`gofmt`/`test` + contract tests (`test/integration/`) verdes en CI.

El CLI headless completo (install/update/doctor/status/logs/rollback/self-update) está funcional y validado adversarialmente.

## Plan de lotes — `add-tui-dashboard` (Fase 4)

Modo interactivo: cada lote se delega → se verifican gates + se revisa el código antes de seguir. teatest de cada vista va en su propio lote.

| Lote | Cubre | Contenido |
|------|-------|-----------|
| **1 — Fundación** ✅ | §1 (1.1–1.5) | HECHO y verificado (gates verdes, binario estático, sin worktree divergente). Creados: `internal/tui/{styles/styles.go,root.go,views.go,adapter.go}` + tests; `cmd/root.go` con `shouldRunTUI`+`RunE`. Notas para lotes futuros: (a) `OperationFinishedMsg.Err` siempre nil → el error final de install/update se toma del **retorno** de `engine.*`, no del canal; (b) stack de navegación puede acumular duplicados (esc igual funciona). |
| **2 — Status view** ✅ | §2 + 8.1 | HECHO y verificado. Refactor AD-3 resuelto: lógica de status extraída a **`internal/status`** (`Doc`, `Deps`, `Collect`, `FetchUpdatesInfo`, `NewDepsReal`, parsers, `ResolveInstallDir`); JSON de `crenein-agent status` byte-idéntico. Status view en `internal/tui/status_view.go` consume `status.Collect`. AD-6 implementado en **dos fases** (Collect sin manifest → `FetchUpdatesInfo` encadenado en background). Constructores: `NewModel(version,profile)` (real) + `NewModelWithStatusDeps(...)` (tests). `root.Init()` ahora delega a la vista activa. 4 goldens 80x24 (installed/not-installed/update-available/unavailable) + tests async/indicadores/breakpoint. |
| **3 — Install wizard** ✅ | §3 + 8.2 | HECHO y verificado. `internal/tui/install_view.go` (`*installView`, state machine guard→checks→config→preview→execution→summary). Step 1 corre `internal/detect` live (pending→running→✅/⚠️/❌, bloquea forward en fatal). Guard vía `status.ResolveInstallDir`. Form: editables (admin email/pass, API URL/token) → `InstallOptions`; informativos masked (mongo/redis pass, ports, /data, mongo user `cnetwork_admin`). Execution: `NewChanReporter(64)`+`ListenEngine` para stream de 12 steps + Cmd que corre `installFn` (default `engine.Install`, inyectable) y devuelve `installFinishedMsg{res,err}` (error del RETORNO, no del canal). Spinner `tea.Tick(120ms)` solo en execution. `AccessSummary` del engine = paridad install-agent.sh. Root: `opRunningMsg{running}` bloquea navegación. 7 tests (full-run/guard/fatal-check/step-fails/async/validation/render). **Deuda para Lote 4**: extraer `baseWizard` compartido (ChanReporter+ListenEngine+installFinishedMsg+spinner) en vez de copiar; inyectar detectores del step 1 vía struct de deps (hoy el step 1 llama al OS real, tests lo bypassean con `sysChecksResultMsg`). |
| **4 — Update + Doctor** ✅ | §4 + §5 + 8.3 + 8.4 | HECHO y verificado. `internal/tui/wizard_base.go` (baseWizard compartido: ChanReporter+ListenEngine+spinner+opRunningMsg; lo usa Update; install NO se refactorizó —sus tests acceden a campos internos— documentado). `internal/tui/update_view.go`: preview current→available+notes (manifest, reusa `cmd/update.go`), estados up-to-date/manifest-unavailable, confirm (agent+frontend sí; mongodb/influxdb/redis/`/data` NO), live `backup→pull→recreate→health-*`, result con rollback (`RolledBack`/`RollbackFailed`). `internal/tui/doctor_view.go`: **OJO `engine.Run` es ATÓMICO** (no emite eventos) → spinner "running diagnostics" colectivo → checklist con glyphs + summary; selección up/down/j/k + detalle (Detail+FixSuggestion); re-run `r`; read-only (sin opRunningMsg). Seams `updateFn`/`doctorFn` inyectables. Goldens 80x24. Limpié código muerto del sub-agente (wizardFinishedMsg, fakeUpdateWithRollback, SA4000 x&&x). |
| **5 — Logs + Degradación** ✅ | §6 + §7 + 8.5 | HECHO y verificado (incl. `go test -race` limpio). `internal/tui/logs_view.go`: puente `lineChanWriter` (io.Writer→`chan string` buf 256, non-blocking, parte por \n) + listen-loop `logLineMsg`; goroutine corre `logsStreamFn` (default ComposeLogsStream, inyectable) y cierra canal→`logsEndedMsg{err}` (errCh buffered evita race líneas-vs-fin); `CancelFunc` guardada para cancelar/reiniciar al cambiar filtro. Filtro `f`/`tab` (all→agent→frontend→mongodb→influxdb→redis); pause/resume `space` (PAUSED/FOLLOWING); ring buffer cap 2000 (`logsRingCap`), tail inicial 100. Degradación: 7.1 ya estaba (shouldRunTUI); 7.2 mensaje have/need + test recovery nuevo; 7.3 test mono nuevo (5 vistas sin ANSI/emoji). 11 tests. Wiring `newLogsViewReal`. Sin código muerto. |
| **6 — Cierre** | 8.6 + 8.7 | teatest de navegación global / quit-durante-operación / too-small / no-color goldens + gates finales + revisión de spec. |

### Decisiones de diseño relevantes (design.md)
- **AD-2:** single root model + view stack para navegación.
- **AD-3:** el TUI consume el engine con los **mismos calls** que headless; el progreso llega como `tea.Msg` (de ahí el event adapter del Lote 1).
- **AD-4:** la degradación de TERM se decide **antes** de arrancar bubbletea (gate en `cmd`).
- **AD-5:** layout responsive desde `WindowSizeMsg`, un solo breakpoint (100 cols).
- **AD-6:** indicadores de update-available degradan a "unknown" si el manifest no está.

### Riesgos / notas
- **Extracción de `engine.Status` (Lote 2):** no existe hoy; la composición vive en `cmd/status.go`. El TUI no debe importar `cmd` → extraer a un paquete compartido sin romper `cmd/status.go` ni sus tests.
- Reuso directo (sin refactor): doctor → `engine.Run`, update → `engine.Update`, install → `engine.Install`, logs → `dockerx.ComposeLogsStream`. Solo status necesita la extracción.
- Verificar que el binario siga **estático** tras agregar las deps de charm (1.1).

## Pendiente manual (en VM cliente — Nicolás)
Validación `[VALIDATE ON VM]` que abarca varios changes: instalación E2E, idempotencia, exit codes, no-AVX, compose v1, terminales reales (SSH/screen/web console). Tareas: `add-cli-scaffold` 5.3–5.5, `add-engine-detectors` 7.8, `add-headless-commands` 8.4/9.4, `add-tui-dashboard` 7.4, `add-selfupdate` 1.5+6.2–6.4. El script `test/integration/full_stack.sh` sirve de guía para el round-trip.

## Para retomar
Arrancar por el **Lote 1 (Fundación)** del TUI. Confirmar modo de ejecución (interactivo) y backend de artefactos (OpenSpec).
