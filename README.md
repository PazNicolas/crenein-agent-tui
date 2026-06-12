# crenein-agent

CLI/TUI para instalar, actualizar y supervisar el agente **C-Network** de CRENEIN
en los hosts de cliente. Es un único binario estático (sin dependencias de
sistema) que reemplaza a los scripts bash de instalación y actualización.

> **Estado:** Fase 1 — *scaffold + distribución*. Por ahora solo el comando
> `version` es funcional. El motor de instalación, los comandos headless, el
> dashboard TUI y el auto-update llegan en fases posteriores.

## Instalación

En un host Linux (amd64 o arm64), como root:

```bash
curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash
```

El instalador detecta la arquitectura, descarga la última release publicada,
**verifica su SHA256** contra `checksums.txt` e instala el binario en
`/usr/local/bin/crenein-agent` (modo `0755`). Volver a ejecutarlo actualiza en
el lugar (idempotente).

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

## Uso

```bash
crenein-agent --version   # o: crenein-agent version
```

## Desarrollo

Requiere Go 1.24+.

```bash
go build -o crenein-agent .   # compilar
go test ./...                 # tests
```

Las releases se generan automáticamente al empujar un tag `v*`:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

GitHub Actions ejecuta [goreleaser](https://goreleaser.com) y publica binarios
estáticos para `linux/amd64` y `linux/arm64` con sus checksums.

## Licencia

Ver [LICENSE](LICENSE).
