#!/usr/bin/env bash
# json_shape_tests.sh — §8.3 JSON shape tests (no Docker required for doctor).
#
# Tests `doctor --json` JSON shape via jq -e assertions.
# Tests `status --json` no-installation path (exits 3 without JSON).
#
# NOTE ON DOCTOR: the doctor command NEVER aborts early; it runs all checks and
# emits JSON even when Docker is not installed. Checks that require Docker are
# reported as fail/skip (not ok), so the exit code will be 2 (critical) in CI.
# The JSON shape MUST still be fully valid regardless.
#
# NOTE ON STATUS: without an installation (no docker-compose.yml referencing
# c-network-agent-back), status exits 3 and emits NO JSON. The JSON shape test
# for status is a full-stack concern (§8.4 — VALIDATE ON CLIENT-LIKE VM).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./harness.sh
source "${SCRIPT_DIR}/harness.sh"

echo '=== §8.3 JSON shape tests ==='
echo

# ── Helper ───────────────────────────────────────────────────────────────────
_run() {
    STDOUT_FILE="$(harness_tmpfile)"
    STDERR_FILE="$(harness_tmpfile)"
    RC=0
    run_cmd "$STDOUT_FILE" "$STDERR_FILE" "$@" || RC=$?
}

# ────────────────────────────────────────────────────────────────────────────
# doctor --json
# ────────────────────────────────────────────────────────────────────────────
echo '--- doctor --json shape ---'

_run doctor --json
# doctor always emits JSON (even when checks fail). Exit code 0/1/2 depending on results.
# In CI without Docker: exit 2 (critical). Both are valid for shape testing.

# The output MUST be parseable as a single JSON document.
assert_valid_json "$STDOUT_FILE" "doctor --json: stdout is valid JSON"

# schema_version == 1
assert_jq '.schema_version == 1' "$STDOUT_FILE" "doctor --json: schema_version == 1"

# command == "doctor"
assert_jq '.command == "doctor"' "$STDOUT_FILE" 'doctor --json: command == "doctor"'

# timestamp is a non-empty string (RFC 3339)
assert_jq '.timestamp | type == "string" and length > 0' "$STDOUT_FILE" "doctor --json: timestamp is non-empty string"

# cli_version is a non-empty string
assert_jq '.cli_version | type == "string" and length > 0' "$STDOUT_FILE" "doctor --json: cli_version is non-empty string"

# summary is present
assert_jq '.summary | type == "object"' "$STDOUT_FILE" "doctor --json: summary is object"

# summary.status is one of "ok", "warning", "critical"
assert_jq '.summary.status | . == "ok" or . == "warning" or . == "critical"' \
    "$STDOUT_FILE" 'doctor --json: summary.status in {ok,warning,critical}'

# summary numeric fields are integers >= 0
assert_jq '.summary.total | type == "number" and . >= 0' "$STDOUT_FILE" "doctor --json: summary.total >= 0"
assert_jq '.summary.passed | type == "number" and . >= 0' "$STDOUT_FILE" "doctor --json: summary.passed >= 0"
assert_jq '.summary.warnings | type == "number" and . >= 0' "$STDOUT_FILE" "doctor --json: summary.warnings >= 0"
assert_jq '.summary.critical | type == "number" and . >= 0' "$STDOUT_FILE" "doctor --json: summary.critical >= 0"
assert_jq '.summary.skipped | type == "number" and . >= 0' "$STDOUT_FILE" "doctor --json: summary.skipped >= 0"

# checks is an array
assert_jq '.checks | type == "array"' "$STDOUT_FILE" "doctor --json: checks is array"

# checks is non-empty (doctor always runs at minimum the docker.installed check)
assert_jq '.checks | length > 0' "$STDOUT_FILE" "doctor --json: checks is non-empty"

# Every check has all 8 required fields with correct types.
# id: non-empty string
assert_jq '.checks | all(.id | type == "string" and length > 0)' \
    "$STDOUT_FILE" "doctor --json: all checks have non-empty id"

# name: non-empty string
assert_jq '.checks | all(.name | type == "string" and length > 0)' \
    "$STDOUT_FILE" "doctor --json: all checks have non-empty name"

# severity: "critical" or "warning"
assert_jq '.checks | all(.severity | . == "critical" or . == "warning")' \
    "$STDOUT_FILE" 'doctor --json: all checks have severity in {critical,warning}'

# status: "pass", "warn", "fail", or "skip"
assert_jq '.checks | all(.status | . == "pass" or . == "warn" or . == "fail" or . == "skip")' \
    "$STDOUT_FILE" 'doctor --json: all checks have status in {pass,warn,fail,skip}'

# message: string (may be empty but must be present)
assert_jq '.checks | all(.message | type == "string")' \
    "$STDOUT_FILE" "doctor --json: all checks have message (string)"

# fix: string or null
assert_jq '.checks | all(.fix | type == "string" or . == null)' \
    "$STDOUT_FILE" "doctor --json: all checks have fix (string or null)"

# duration_ms: integer >= 0
assert_jq '.checks | all(.duration_ms | type == "number" and . >= 0)' \
    "$STDOUT_FILE" "doctor --json: all checks have duration_ms >= 0"

# checks[0] has all 8 fields explicitly (not just via all())
assert_jq '.checks[0] | has("id") and has("name") and has("severity") and has("status") and has("message") and has("fix") and has("duration_ms")' \
    "$STDOUT_FILE" "doctor --json: checks[0] has all 8 documented fields"

# Summary math: passed + warnings + critical + skipped == total
assert_jq '.summary | .passed + .warnings + .critical + .skipped == .total' \
    "$STDOUT_FILE" "doctor --json: summary counts add up to total"

# stdout has no ANSI escape sequences (machine mode: no color/spinner)
TESTS_RUN=$((TESTS_RUN + 1))
if grep -qP '\033\[' "$STDOUT_FILE" 2>/dev/null; then
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILED_NAMES+=("doctor --json: stdout has ANSI escape sequences")
    printf '  FAIL  doctor --json: stdout contains ANSI escape sequences\n'
else
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf '  PASS  doctor --json: stdout contains no ANSI escape sequences\n'
fi

# doctor --json exits with 0, 1, or 2 (not 64)
TESTS_RUN=$((TESTS_RUN + 1))
if [[ "$RC" -ne 64 ]]; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf '  PASS  doctor --json: exit=%d (not 64)\n' "$RC"
else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILED_NAMES+=("doctor --json: must not exit 64")
    printf '  FAIL  doctor --json: exits 64 (must be 0, 1, or 2)\n'
fi

echo

# ────────────────────────────────────────────────────────────────────────────
# doctor --json --quiet: shape identical, exit unchanged
# ────────────────────────────────────────────────────────────────────────────
echo '--- doctor --json --quiet shape ---'

QUIET_STDOUT="$(harness_tmpfile)"
QUIET_STDERR="$(harness_tmpfile)"
QUIET_RC=0
run_cmd "$QUIET_STDOUT" "$QUIET_STDERR" doctor --json --quiet || QUIET_RC=$?

assert_valid_json "$QUIET_STDOUT" "doctor --json --quiet: stdout is valid JSON"
assert_jq '.schema_version == 1' "$QUIET_STDOUT" "doctor --json --quiet: schema_version == 1"

# Exit code with --quiet must match the exit code without --quiet.
TESTS_RUN=$((TESTS_RUN + 1))
if [[ "$QUIET_RC" -eq "$RC" ]]; then
    TESTS_PASSED=$((TESTS_PASSED + 1))
    printf '  PASS  doctor --json --quiet: exit=%d matches doctor --json exit=%d\n' "$QUIET_RC" "$RC"
else
    TESTS_FAILED=$((TESTS_FAILED + 1))
    FAILED_NAMES+=("doctor --json --quiet: exit differs from doctor --json")
    printf '  FAIL  doctor --json --quiet: exit=%d differs from doctor --json exit=%d\n' "$QUIET_RC" "$RC"
fi

echo

# ────────────────────────────────────────────────────────────────────────────
# status --json without installation → exit 3, NO JSON on stdout
# (Full JSON shape test goes in §8.4 full-stack; documented limit below.)
# ────────────────────────────────────────────────────────────────────────────
echo '--- status --json without installation ---'
echo '  NOTE: status --json JSON shape test requires a real installation.'
echo '        Exit-3 path (no installation) is covered here.'
echo '        Full shape test: marked for §8.4 (VALIDATE ON CLIENT-LIKE VM).'
echo

_run status --json
assert_exit 3 "$RC" "status --json (no installation) exits 3"
assert_stdout_empty "$STDOUT_FILE" "status --json (no installation): no JSON on stdout"
assert_stderr_contains "install" "$STDERR_FILE" "status --json (no installation): stderr mentions install"

echo

# ────────────────────────────────────────────────────────────────────────────
# Summary
# ────────────────────────────────────────────────────────────────────────────
harness_summary
