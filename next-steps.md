# Next steps — crenein-agent

_Punto de retomada. Última actualización: 2026-06-15._

## Estado de los changes (OpenSpec)

| Change | Progreso | Estado |
|--------|----------|--------|
| `add-cli-scaffold-distribution` | 14/17 | código completo; faltan validaciones en VM (5.3–5.5) |
| `add-engine-detectors` | 49/50 | código completo; 7.8 = validación VM |
| `add-headless-commands` | 39/41 | ✅ completo + validado (judgment-day); faltan 8.4/9.4 `[VALIDATE ON VM]` |
| `add-selfupdate-version-manifest` | 25/31 | casi cerrado; faltan 5.3/5.4 (dependen del TUI) y 1.5+6.2–6.4 (release/VM) |
| `add-tui-dashboard` | 0/38 | **siguiente** — ver plan abajo |

Todo lo anterior está commiteado y pusheado a `main` (último: `52e3a35`), con `build`/`vet`/`gofmt`/`test` + contract tests (`test/integration/`) verdes en CI.

El CLI headless completo (install/update/doctor/status/logs/rollback/self-update) está funcional y validado adversarialmente.

## Plan de lotes — `add-tui-dashboard` (Fase 4)

Modo interactivo: cada lote se delega → se verifican gates + se revisa el código antes de seguir. teatest de cada vista va en su propio lote.

| Lote | Cubre | Contenido |
|------|-------|-----------|
| **1 — Fundación** | §1 (1.1–1.5) | deps (bubbletea/lipgloss/bubbles/teatest), `internal/tui/styles` (paleta, glyphs + fallback ASCII, NO_COLOR), root model (view stack, keymap global `s/i/u/d/l/esc/q`, header/footer, `WindowSizeMsg`), engine event adapter (`Reporter`→canal→`tea.Cmd`→msgs + fake engine), `cmd` wiring (sin args + TTY → dashboard). **Base de todo.** |
| **2 — Status view** | §2 + 8.1 | service table, panel versiones, update-indicators (release en background), refresh tick + `r`, not-installed, layout responsive ≥100/<100 cols, golden 80x24. ⚠️ **Decisión:** extraer la composición de status (hoy en `cmd/status.go`) a `internal/engine` o paquete compartido, para cumplir AD-3 sin que el TUI importe `cmd`. |
| **3 — Install wizard** | §3 + 8.2 | 5 pasos: system checks → existing-install guard → config form → preview → execution (event-driven) → access summary. El más grande; reusa `engine.Install`. |
| **4 — Update + Doctor** | §4 + §5 + 8.3 + 8.4 | Update wizard (preview→confirm→backup/pull/recreate/health→result con rollback) + Doctor view (checklist live, detalle, re-run). Event-driven (`engine.Update`/`engine.Run`). |
| **5 — Logs + Degradación** | §6 + §7 + 8.5 | Logs view (follow en viewport, filtro de servicio, pause/resume, buffer cap) + degradación (gate non-TTY/`TERM=dumb`, too-small 80x24, NO_COLOR/ASCII). Reusa `dockerx.ComposeLogsStream`. |
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
