# crenein-agent

CLI para instalar, actualizar y supervisar el agente **C-Network** de CRENEIN en hosts de cliente. Es un único binario estático (sin dependencias de sistema) que reemplaza los scripts bash de instalación y actualización.

## Instalación

En un host Linux (amd64 o arm64), como root:

```bash
curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash
```

El instalador detecta la arquitectura, descarga la última release publicada, **verifica su SHA256** contra `checksums.txt` e instala el binario en `/usr/local/bin/crenein-agent` (modo `0755`). Volver a ejecutarlo actualiza en el lugar (idempotente).

Para instalar una versión específica:

```bash
sudo bash install.sh v0.1.0
```

### Códigos de salida del instalador

| Código | Significado |
| ------ | ----------- |
| `0` | éxito |
| `1` | sistema operativo o arquitectura no soportados |
| `2` | falta una dependencia (`curl`, `tar`, `sha256sum`, `mktemp`) |
| `3` | fallo al resolver la versión o descargar |
| `4` | el checksum no coincide (no se toca el binario existente) |
| `5` | no se ejecutó como root |

## Tabla global de códigos de salida

Todos los subcomandos comparten esta tabla. Las excepciones por comando se documentan en la sección de cada uno.

| Código | Significado |
| ------ | ----------- |
| `0` | éxito |
| `1` | fallo de operación |
| `3` | fallo de pre-flight (raíz, distro, disco, conectividad) |
| `4` | operación abortada por el usuario |
| `64` | error de uso (flag desconocido, valor inválido, input requerido sin TTY) |

Códigos extendidos:

| Código | Comando | Significado |
| ------ | ------- | ----------- |
| `2` | `doctor` | al menos un check crítico falló |
| `5` | `update` | update falló, rollback automático exitoso |
| `6` | `update` | update falló y rollback también falló (intervención manual) |
| `10` | `self-update --check` | actualización disponible |

## Subcomandos

### `version`

Imprime la versión del binario.

```bash
crenein-agent version
crenein-agent --version   # idéntico
```

**Flags:** ninguno adicional.  
**Exit codes:** `0`.

---

### `install`

Instala el stack de agente C-Network en el host. Requiere root. Soporta Ubuntu y Debian.

```bash
# Modo interactivo (TTY)
crenein-agent install

# Modo no interactivo (cron / SSH sin TTY)
crenein-agent install --yes \
  --api-url https://core.crenein.com \
  --api-token tok123 \
  --admin-email ops@example.com \
  --admin-password supersecret
```

**Flags:**

| Flag | Env | Default | Descripción |
| ---- | --- | ------- | ----------- |
| `--yes` | — | false | Omite prompts y usa valores por defecto |
| `--dir` | `CRENEIN_INSTALL_DIR` | CWD | Directorio de instalación |
| `--mongo {auto\|7\|4}` | `CRENEIN_MONGO_MAJOR` | `auto` | Versión mayor de MongoDB |
| `--api-url` | `CRENEIN_API_URL` | `http://localhost:8000` | URL de la API C-Network |
| `--api-token` | `CRENEIN_API_TOKEN` | `your-api-token-here` | Token de la API |
| `--admin-email` | `CRENEIN_ADMIN_EMAIL` | `admin@example.com` | Email del administrador |
| `--admin-password` | `CRENEIN_ADMIN_PASSWORD` | `admin123` | Contraseña del administrador |

**Precedencia de resolución:** flag > env > prompt TTY > default.  
**Con `--yes`:** no hay prompts; sin TTY y sin `--yes` → exit 64.

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | instalación exitosa |
| `1` | fallo en un paso del engine |
| `3` | pre-flight fallido (no root, distro no soportada, disco insuficiente, sin conectividad, CPU sin AVX con `--mongo 7`) |
| `4` | usuario declinó la confirmación final |
| `64` | error de uso |

---

### `update`

Actualiza el stack (agent + frontend) usando el manifest de versiones publicado.

```bash
# Modo interactivo
crenein-agent update

# Modo no interactivo / cron
crenein-agent update --yes --quiet

# Ver el plan sin ejecutar nada
crenein-agent update --dry-run

# Fijar versión específica
crenein-agent update --yes --version 1.8.4
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `--yes` | Omite la confirmación interactiva |
| `--version X.Y.Z` | Versión objetivo (default: latest del manifest) |
| `--dry-run` | Muestra el plan sin realizar cambios (no requiere confirmación) |
| `--skip-frontend` | Actualiza solo el servicio `agent` |
| `--no-cleanup` | No ejecuta `docker image prune -f` al finalizar |
| `--force` | Recrea containers aunque el image ID no haya cambiado |

> **Nota:** `--force` controla la política de recreación; `--yes` controla el consentimiento. Son ortogonales.

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | actualización exitosa o ya al día |
| `1` | fallo antes de mutar el sistema (manifest inalcanzable, pull fallido) |
| `3` | pre-flight fallido |
| `4` | usuario declinó la confirmación |
| `5` | update falló tras mutar, rollback automático exitoso |
| `6` | update falló y rollback también falló |
| `64` | error de uso (sin TTY y sin `--yes`, `--version` con formato inválido) |

---

### `doctor`

Ejecuta checks de diagnóstico read-only sobre el host y el stack.

```bash
# Salida humana
crenein-agent doctor

# Salida JSON (para automatización)
crenein-agent doctor --json

# En cron (nunca cuelga, exit code clasifica el resultado)
crenein-agent doctor --json --quiet
case $? in
  0) echo "stack healthy" ;;
  1) echo "warnings found" ;;
  2) echo "critical issues" ;;
esac
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `--json` | Emite un único documento JSON en stdout |

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | todos los checks pasan |
| `1` | al menos un check con nivel `warning` falló (sin críticos) |
| `2` | al menos un check crítico falló |
| `64` | error de uso |

Doctor **nunca aborta** antes de emitir el JSON — aunque Docker no esté instalado, los checks se marcan como `fail` o `skip` y el JSON se emite igualmente.

#### Shape `doctor --json` (schema_version 1)

```json
{
  "schema_version": 1,
  "command": "doctor",
  "timestamp": "2026-06-12T15:04:05Z",
  "cli_version": "0.3.0",
  "summary": {
    "status": "ok",
    "total": 11,
    "passed": 9,
    "warnings": 1,
    "critical": 1,
    "skipped": 0
  },
  "checks": [
    {
      "id": "docker.daemon",
      "name": "Docker daemon running",
      "severity": "critical",
      "status": "pass",
      "message": "Docker 26.1 is running",
      "fix": null,
      "duration_ms": 41
    }
  ]
}
```

**IDs de checks estables:** `docker.installed`, `docker.daemon`, `docker.compose`, `net.dockerhub`, `net.cnetwork_api`, `disk.space`, `files.permissions`, `services.running`, `agent.health`, `logs.recent_errors`, `cpu.avx_mongo`.

**Enums:**
- `summary.status`: `"ok"` | `"warning"` | `"critical"`
- `checks[].severity`: `"critical"` | `"warning"`
- `checks[].status`: `"pass"` | `"warn"` | `"fail"` | `"skip"`
- `checks[].fix`: string o `null`

---

### `status`

Muestra el estado de instalación y servicios del stack.

```bash
# Salida humana
crenein-agent status

# Salida JSON
crenein-agent status --json

# En script: degraded check
if ! crenein-agent status --json --quiet > /tmp/status.json; then
  jq '.services[] | select(.state != "running")' /tmp/status.json
fi
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `--json` | Emite un único documento JSON en stdout |

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | todos los servicios corriendo y sanos |
| `1` | al menos un servicio no está corriendo o está `unhealthy` |
| `3` | no se encontró instalación (sugiere `crenein-agent install`) |
| `64` | error de uso |

#### Shape `status --json` (schema_version 1)

```json
{
  "schema_version": 1,
  "command": "status",
  "timestamp": "2026-06-12T15:04:05Z",
  "cli_version": "0.3.0",
  "install_dir": "/root",
  "agent": {
    "version": "1.8.3",
    "version_source": "health",
    "image": "crenein/c-network-agent-back:1.8.3",
    "health": "healthy"
  },
  "mongo": {
    "image": "mongodb/mongodb-community-server:7.0-ubuntu2204",
    "major": "7.x"
  },
  "services": [
    {
      "name": "agent",
      "image": "crenein/c-network-agent-back:1.8.3",
      "state": "running",
      "health": "healthy",
      "status_text": "Up 3 days",
      "uptime_seconds": 262800
    }
  ]
}
```

Los `services` siempre tienen exactamente 5 elementos en orden estable: `agent`, `frontend`, `mongodb`, `influxdb`, `redis`. Un servicio ausente (container eliminado) aparece con `"state": "missing"`.

**Enums:**
- `agent.version_source`: `"health"` | `"image_tag"` | `"unknown"`
- `agent.health`: `"healthy"` | `"unhealthy"` | `"unknown"`
- `services[].state`: `"running"` | `"restarting"` | `"exited"` | `"created"` | `"paused"` | `"missing"`
- `services[].health`: `"healthy"` | `"unhealthy"` | `"none"`

---

### `logs`

Muestra o sigue los logs del stack vía compose.

```bash
# Últimas 100 líneas de todos los servicios
crenein-agent logs

# Últimas 50 líneas del servicio agent
crenein-agent logs agent --tail 50

# Seguir en tiempo real
crenein-agent logs -f agent

# Volcar a archivo (sin ANSI cuando stdout no es TTY)
crenein-agent logs agent --tail 200 > /tmp/agent.log
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `-f`, `--follow` | Streaming continuo (termina con Ctrl-C / SIGINT → exit 0) |
| `--tail N` | Líneas de backlog (default: 100) |

**Servicios válidos:** `agent`, `frontend`, `mongodb`, `influxdb`, `redis`.

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | completado o terminado por SIGINT en follow mode |
| `1` | error de Docker/compose |
| `3` | no se encontró instalación |
| `64` | nombre de servicio desconocido o error de uso |

---

### `rollback`

Restaura el stack al snapshot de backup más reciente (o uno específico).

```bash
# Listar snapshots disponibles
crenein-agent rollback --list

# Rollback interactivo (pide confirmación)
crenein-agent rollback

# Rollback no interactivo
crenein-agent rollback --yes

# Rollback a snapshot específico
crenein-agent rollback --yes --backup 20260612_103000
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `--yes` | Omite la confirmación interactiva |
| `--backup TIMESTAMP` | Snapshot específico (default: el más reciente) |
| `--list` | Lista snapshots disponibles y sale |

**Exit codes:**

| Código | Causa |
| ------ | ----- |
| `0` | rollback completo y health check pasó |
| `1` | rollback falló o health check post-rollback falló |
| `3` | no hay backups disponibles o no hay instalación |
| `4` | usuario declinó la confirmación |
| `64` | timestamp `--backup` inexistente, sin TTY y sin `--yes` |

---

### `self-update`

Actualiza el binario `crenein-agent` a la última versión publicada en GitHub Releases.

```bash
# Verificar disponibilidad (sin modificar nada)
crenein-agent self-update --check
# exit 0 = al día, exit 10 = actualización disponible, exit 1 = error

# Actualizar (interactivo)
crenein-agent self-update

# Actualizar (no interactivo)
crenein-agent self-update --yes

# Instalar versión específica
crenein-agent self-update --yes --version 0.2.0
```

**Flags:**

| Flag | Descripción |
| ---- | ----------- |
| `--check` | Solo verifica; exit 0=al día, 10=disponible, 1=error |
| `--yes` | Omite la confirmación |
| `--version X.Y.Z` | Instala una versión específica (permite downgrade) |
| `--force-check` | Fuerza fetch del manifest (ignora caché de 24h) |

---

## Flags globales

Disponibles en todos los subcomandos:

| Flag | Descripción |
| ---- | ----------- |
| `--quiet` | Suprime líneas de progreso en stderr (errores siempre se muestran) |
| `--no-color` | Deshabilita ANSI color (también `NO_COLOR=1` en el entorno) |

## Ejemplos de automatización

### Cron: update diario silencioso

```bash
# /etc/cron.d/crenein-agent-update
0 3 * * * root /usr/local/bin/crenein-agent update --yes --quiet >> /var/log/crenein-agent-update.log 2>&1
```

### Cron: doctor con branching

```bash
#!/bin/bash
/usr/local/bin/crenein-agent doctor --json --quiet > /tmp/dr.json
case $? in
  0) echo "healthy" ;;
  1) echo "WARNING: $(jq -r '[.checks[] | select(.status=="warn")] | length' /tmp/dr.json) checks warn" ;;
  2) echo "CRITICAL: $(jq -r '[.checks[] | select(.status=="fail")] | map(.name) | join(", ")' /tmp/dr.json)" ;;
esac
```

### jq: extraer servicios caídos

```bash
crenein-agent status --json --quiet \
  | jq '.services[] | select(.state != "running") | {name, state, health}'
```

### Verificar versión instalada

```bash
VER=$(crenein-agent status --json --quiet | jq -r '.agent.version')
echo "Agent version: $VER"
```

## Desarrollo

Requiere Go 1.24+.

```bash
go build -o crenein-agent .   # compilar
go test ./...                 # tests unitarios
bash test/integration/run_contract_tests.sh  # tests de integración (sin Docker)
```

Las releases se generan automáticamente al empujar un tag `v*`:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

GitHub Actions ejecuta [goreleaser](https://goreleaser.com) y publica binarios estáticos para `linux/amd64` y `linux/arm64` con sus checksums.

### Tests de integración full-stack (requiere Docker)

Para ejercitar el flujo completo install→status→doctor→logs→update→rollback en una VM descartable (Ubuntu/Debian, root, Docker instalado):

```bash
sudo bash test/integration/full_stack.sh
```

Con un binario pre-instalado:

```bash
BIN=/usr/local/bin/crenein-agent sudo bash test/integration/full_stack.sh
```

> Validar en VM de cliente: sin AVX, compose v1, TERM limitado.

## Licencia

Ver [LICENSE](LICENSE).
