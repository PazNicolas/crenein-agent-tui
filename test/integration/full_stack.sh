#!/usr/bin/env bash
# full_stack.sh — §8.4 full-stack integration tests.
#
# [VALIDATE ON CLIENT-LIKE VM: no AVX, compose v1, poor TERM]
#
# IMPORTANT: This script is NOT executed by CI or run_contract_tests.sh.
# It documents the round-trip install→status→doctor→logs→update→rollback flow.
# Run it manually on a disposable VM (Ubuntu/Debian, root, Docker present).
#
# Usage (as root on a real VM):
#   BIN=/usr/local/bin/crenein-agent bash test/integration/full_stack.sh
#
# Or after a local build:
#   BIN=./test/integration/bin/crenein-agent bash test/integration/full_stack.sh

set -euo pipefail

BIN="${BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/bin/crenein-agent}"

# Verify jq is available
if ! command -v jq &>/dev/null; then
    echo 'ERROR: jq is required for shape assertions.' >&2
    exit 1
fi

echo '=== §8.4 Full-stack integration tests (REQUIRES DOCKER + ROOT) ==='
echo '    [VALIDATE ON CLIENT-LIKE VM: no AVX, compose v1, poor TERM]'
echo

PASS=0
FAIL=0

check() {
    local desc="$1"
    local rc="$2"
    local expected="$3"
    if [[ "$rc" -eq "$expected" ]]; then
        echo "  PASS  $desc (exit $rc)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc (exit $rc, expected $expected)"
        FAIL=$((FAIL + 1))
    fi
}

# ── 1. Install ────────────────────────────────────────────────────────────────
echo '--- 1. Install (non-interactive) ---'
INSTALL_DIR="$(mktemp -d)"

rc=0
"$BIN" install \
    --yes \
    --dir "$INSTALL_DIR" \
    --mongo auto \
    --api-url http://localhost:8000 \
    --api-token test-token \
    --admin-email admin@example.com \
    --admin-password admin123 \
    </dev/null \
    2>&1 || rc=$?
check "install --yes exits 0" "$rc" 0

echo

# ── 2. Status --json shape ─────────────────────────────────────────────────
echo '--- 2. Status --json shape ---'

STATUS_OUT="$(mktemp)"
rc=0
"$BIN" status --json </dev/null >"$STATUS_OUT" 2>/dev/null || rc=$?
# After install, status should find the installation. Exit 0 (all running) or 1 (degraded).
if [[ "$rc" -eq 0 || "$rc" -eq 1 ]]; then
    echo "  PASS  status --json exits 0 or 1 (exit $rc)"
    PASS=$((PASS + 1))
else
    echo "  FAIL  status --json exits $rc (expected 0 or 1)"
    FAIL=$((FAIL + 1))
fi

# Status JSON shape assertions
jq_check() {
    local expr="$1"
    local desc="$2"
    if jq -e "$expr" "$STATUS_OUT" >/dev/null 2>&1; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        json: $(jq '.' "$STATUS_OUT" 2>/dev/null)"
        FAIL=$((FAIL + 1))
    fi
}

jq_check '.schema_version == 1' 'status --json: schema_version == 1'
jq_check '.command == "status"' 'status --json: command == "status"'
jq_check '.timestamp | type == "string" and length > 0' 'status --json: timestamp is non-empty'
jq_check '.cli_version | type == "string" and length > 0' 'status --json: cli_version is non-empty'
jq_check '.install_dir | type == "string" and length > 0' 'status --json: install_dir is non-empty'
jq_check '.agent.version | type == "string"' 'status --json: agent.version is string'
jq_check '.agent.version_source | . == "health" or . == "image_tag" or . == "unknown"' \
    'status --json: agent.version_source in {health,image_tag,unknown}'
jq_check '.agent.image | type == "string"' 'status --json: agent.image is string'
jq_check '.agent.health | . == "healthy" or . == "unhealthy" or . == "unknown"' \
    'status --json: agent.health in {healthy,unhealthy,unknown}'
jq_check '.mongo.image | type == "string"' 'status --json: mongo.image is string'
jq_check '.mongo.major | type == "string"' 'status --json: mongo.major is string'
jq_check '.services | type == "array" and length == 5' 'status --json: services has exactly 5 entries'
jq_check '.services | all(.name | type == "string" and length > 0)' \
    'status --json: all services have name'
jq_check '.services | all(.state | . == "running" or . == "restarting" or . == "exited" or . == "created" or . == "paused" or . == "missing")' \
    'status --json: all services have valid state'
jq_check '.services | all(.health | . == "healthy" or . == "unhealthy" or . == "none")' \
    'status --json: all services have valid health'
jq_check '.services | all(.uptime_seconds | type == "number" and . >= 0)' \
    'status --json: all services have uptime_seconds >= 0'
jq_check '[ .services[].name ] == ["agent","frontend","mongodb","influxdb","redis"]' \
    'status --json: services in stable order (agent,frontend,mongodb,influxdb,redis)'

rm -f "$STATUS_OUT"
echo

# ── 3. Doctor -------------------------------------------------------------------
echo '--- 3. Doctor (healthy stack) ---'
rc=0
"$BIN" doctor </dev/null 2>&1 || rc=$?
check "doctor exits 0 on healthy stack" "$rc" 0

DOCTOR_OUT="$(mktemp)"
rc=0
"$BIN" doctor --json </dev/null >"$DOCTOR_OUT" 2>/dev/null || rc=$?
check "doctor --json exits 0 on healthy stack" "$rc" 0
if jq -e '.summary.status == "ok"' "$DOCTOR_OUT" >/dev/null 2>&1; then
    echo "  PASS  doctor --json: summary.status == ok on healthy stack"
    PASS=$((PASS + 1))
else
    echo "  FAIL  doctor --json: summary.status is not ok on healthy stack"
    FAIL=$((FAIL + 1))
fi
rm -f "$DOCTOR_OUT"
echo

# ── 4. Logs -------------------------------------------------------------------
echo '--- 4. Logs ---'
rc=0
timeout 5s "$BIN" logs agent --tail 20 </dev/null 2>&1 || rc=$?
# logs exits 0 on success
check "logs agent --tail 20 exits 0" "$rc" 0
echo

# ── 5. Update (dry-run) ────────────────────────────────────────────────────
echo '--- 5. Update --dry-run ---'
DRYRUN_OUT="$(mktemp)"
rc=0
"$BIN" update --dry-run </dev/null >"$DRYRUN_OUT" 2>/dev/null || rc=$?
check "update --dry-run exits 0" "$rc" 0
if grep -qi 'DRY RUN' "$DRYRUN_OUT"; then
    echo "  PASS  update --dry-run: stdout contains 'DRY RUN'"
    PASS=$((PASS + 1))
else
    echo "  FAIL  update --dry-run: stdout missing 'DRY RUN'"
    FAIL=$((FAIL + 1))
fi
rm -f "$DRYRUN_OUT"
echo

# ── 6. Rollback --list (no backups yet after fresh install) ─────────────────
echo '--- 6. Rollback --list (no backups) ---'
rc=0
"$BIN" rollback --list </dev/null 2>&1 || rc=$?
# Exit 3 when no backups exist
check "rollback --list (no backups) exits 3" "$rc" 3
echo

# ── 7. JSON stream discipline: no ANSI in doctor --json stdout ───────────────
echo '--- 7. No ANSI in doctor --json stdout ---'
DOCTOR2_OUT="$(mktemp)"
"$BIN" doctor --json </dev/null >"$DOCTOR2_OUT" 2>/dev/null || true
if grep -qP '\033\[' "$DOCTOR2_OUT" 2>/dev/null; then
    echo "  FAIL  doctor --json: stdout contains ANSI sequences"
    FAIL=$((FAIL + 1))
else
    echo "  PASS  doctor --json: stdout is ANSI-free"
    PASS=$((PASS + 1))
fi
rm -f "$DOCTOR2_OUT"
echo

# ── Summary ───────────────────────────────────────────────────────────────────
echo "Tests: $((PASS + FAIL))  Passed: $PASS  Failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
    exit 1
fi
echo 'All full-stack tests passed.'
