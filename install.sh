#!/usr/bin/env bash
#
# crenein-agent installer.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/PazNicolas/crenein-agent-tui/main/install.sh | sudo bash
#   sudo bash install.sh            # install the latest release
#   sudo bash install.sh v0.1.0     # install a specific version
#
# Installs the latest (or requested) crenein-agent release to
# /usr/local/bin/crenein-agent, verifying its SHA256 against the release's
# checksums.txt. Re-running upgrades in place (idempotent).
#
# Exit codes (spec-level contract — do not repurpose):
#   0  success
#   1  unsupported OS / architecture
#   2  missing required dependency (curl, tar, sha256sum, mktemp)
#   3  version resolution or download failure
#   4  checksum mismatch (existing binary left untouched)
#   5  not running as root
set -euo pipefail

REPO="PazNicolas/crenein-agent-tui"
BINARY="crenein-agent"
INSTALL_PATH="/usr/local/bin/${BINARY}"

err() { printf 'error: %s\n' "$*" >&2; }
info() { printf '%s\n' "$*"; }

# --- 5: root check -----------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  err "must run as root (it writes to ${INSTALL_PATH}); re-run with sudo:"
  err "  curl -sSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo bash"
  exit 5
fi

# --- 1: OS / architecture detection ------------------------------------------
os="$(uname -s)"
if [ "${os}" != "Linux" ]; then
  err "unsupported operating system '${os}': only Linux is supported"
  exit 1
fi

raw_arch="$(uname -m)"
case "${raw_arch}" in
  x86_64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *)
    err "unsupported architecture '${raw_arch}': only x86_64 (amd64) and aarch64/arm64 are supported"
    exit 1
    ;;
esac

# --- 2: dependency check -----------------------------------------------------
for tool in curl tar sha256sum mktemp; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    err "required tool '${tool}' is not installed"
    exit 2
  fi
done

# --- 3: resolve the target version -------------------------------------------
# Either an explicit "vX.Y.Z" argument, or the latest release resolved by
# following the /releases/latest redirect (avoids the rate-limited REST API).
resolve_latest() {
  local location
  # -s silent, -I head only, -L do NOT follow (we read the redirect target).
  location="$(curl -sI "https://github.com/${REPO}/releases/latest" \
    | tr -d '\r' \
    | awk 'tolower($1) == "location:" { print $2 }' \
    | tail -n1)"
  # location looks like https://github.com/<repo>/releases/tag/v0.1.0
  printf '%s\n' "${location##*/tag/}"
}

VERSION="${1:-}"
if [ -z "${VERSION}" ]; then
  VERSION="$(resolve_latest || true)"
fi

case "${VERSION}" in
  v[0-9]*) : ;; # ok: looks like a version tag
  *)
    err "could not resolve a release version (got '${VERSION:-<empty>}')"
    err "pass one explicitly, e.g.: sudo bash install.sh v0.1.0"
    exit 3
    ;;
esac

# Strip the leading 'v' for the archive name (assets use the bare X.Y.Z form).
VERSION_NUM="${VERSION#v}"
ARCHIVE="${BINARY}_${VERSION_NUM}_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

# --- temp workdir, always cleaned up -----------------------------------------
WORKDIR="$(mktemp -d)"
# shellcheck disable=SC2317,SC2329  # cleanup runs indirectly via the EXIT trap below
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

info "Installing ${BINARY} ${VERSION} (${ARCH})..."

# --- 3: download archive + checksums -----------------------------------------
if ! curl -fsSL -o "${WORKDIR}/${ARCHIVE}" "${BASE_URL}/${ARCHIVE}"; then
  err "failed to download ${ARCHIVE} from ${BASE_URL}"
  exit 3
fi
if ! curl -fsSL -o "${WORKDIR}/checksums.txt" "${BASE_URL}/checksums.txt"; then
  err "failed to download checksums.txt from ${BASE_URL}"
  exit 3
fi

# --- 4: verify SHA256 before touching anything -------------------------------
# --ignore-missing so we only check the one archive we downloaded.
if ! (cd "${WORKDIR}" && sha256sum --check --ignore-missing checksums.txt >/dev/null 2>&1); then
  err "checksum verification failed for ${ARCHIVE}"
  err "the existing installation (if any) was left untouched"
  exit 4
fi

# --- extract and install atomically ------------------------------------------
tar -xzf "${WORKDIR}/${ARCHIVE}" -C "${WORKDIR}"
if [ ! -f "${WORKDIR}/${BINARY}" ]; then
  err "archive did not contain expected binary '${BINARY}'"
  exit 3
fi

# install(1) writes to a temp name then renames within the same filesystem, so
# a concurrent run never observes a partial binary at the install path.
install -m 755 "${WORKDIR}/${BINARY}" "${INSTALL_PATH}"

info "Installed to ${INSTALL_PATH}"
"${INSTALL_PATH}" --version
exit 0
