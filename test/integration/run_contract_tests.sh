#!/usr/bin/env bash
# run_contract_tests.sh — §8.1 entry point: build once, run all contract tests.
#
# Usage:
#   bash test/integration/run_contract_tests.sh
#
# The script:
#   1. Builds the binary to test/integration/bin/crenein-agent (if needed or stale).
#   2. Runs §8.2 contract_tests.sh (exit codes, TTY behavior, no-Docker tests).
#   3. Runs §8.3 json_shape_tests.sh (jq -e assertions on doctor --json).
#   4. Prints an aggregated PASS/FAIL summary and exits with the combined result.
#
# Requires: bash 4+, jq, go (for the build step).
# Does NOT require Docker.
#
# ── Running the full-stack suite manually ─────────────────────────────────────
# The §8.4 full-stack suite (full_stack.sh) requires Docker and should be run
# manually on a disposable Ubuntu/Debian VM (root, Docker installed):
#
#   sudo bash test/integration/full_stack.sh
#
# Or with a pre-built binary:
#   BIN=/usr/local/bin/crenein-agent sudo bash test/integration/full_stack.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BIN_DIR="${SCRIPT_DIR}/bin"
BIN="${BIN_DIR}/crenein-agent"

# ── 1. Build the binary ───────────────────────────────────────────────────────
mkdir -p "$BIN_DIR"

echo '=== Building crenein-agent ==='
cd "$REPO_ROOT"
go build -o "$BIN" .
echo "  Built: ${BIN}"
echo

export BIN

# ── 2. Contract tests (§8.2) ──────────────────────────────────────────────────
CONTRACT_RC=0
bash "${SCRIPT_DIR}/contract_tests.sh" || CONTRACT_RC=$?

# ── 3. JSON shape tests (§8.3) ────────────────────────────────────────────────
JSON_RC=0
bash "${SCRIPT_DIR}/json_shape_tests.sh" || JSON_RC=$?

# ── 4. Aggregated result ──────────────────────────────────────────────────────
echo
echo '=== Integration test suite summary ==='
if [[ "$CONTRACT_RC" -eq 0 && "$JSON_RC" -eq 0 ]]; then
    echo 'PASS: All contract tests passed.'
    exit 0
else
    [[ "$CONTRACT_RC" -ne 0 ]] && echo "FAIL: §8.2 contract_tests.sh exited $CONTRACT_RC"
    [[ "$JSON_RC" -ne 0 ]] && echo "FAIL: §8.3 json_shape_tests.sh exited $JSON_RC"
    exit 1
fi
