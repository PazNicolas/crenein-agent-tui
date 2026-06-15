#!/usr/bin/env bash
# harness.sh — shared helpers for crenein-agent integration tests.
#
# Source this file in every test script:
#   source "$(dirname "$0")/harness.sh"
#
# It expects the binary to have been built to BIN (default: the bin/ subdir
# adjacent to this file). The build step is done once by run_contract_tests.sh
# before sourcing any test scripts.

set -euo pipefail

# ── Path to the binary ──────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${BIN:-${SCRIPT_DIR}/bin/crenein-agent}"

# ── Temp-file tracking for cleanup ─────────────────────────────────────────
_TMPFILES=()

harness_tmpfile() {
    local f
    f="$(mktemp)"
    _TMPFILES+=("$f")
    printf '%s' "$f"
}

harness_cleanup() {
    for f in "${_TMPFILES[@]+"${_TMPFILES[@]}"}"; do
        rm -f "$f"
    done
}
trap harness_cleanup EXIT

# ── Test counters ────────────────────────────────────────────────────────────
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0
FAILED_NAMES=()

# ── run_cmd ──────────────────────────────────────────────────────────────────
# run_cmd <stdout_file> <stderr_file> [args...]
#
# Invokes $BIN with stdin from /dev/null, a 10-second timeout, all args
# forwarded, and stdout/stderr written to the supplied temp files.
# The function returns the exit code of the binary (NOT of timeout itself;
# timeout exits 124 on expiry — that is intentional, tests will fail).
run_cmd() {
    local stdout_file="$1"
    local stderr_file="$2"
    shift 2

    local rc=0
    timeout 10s "$BIN" "$@" \
        </dev/null \
        >"$stdout_file" \
        2>"$stderr_file" \
        || rc=$?
    return $rc
}

# ── assert helpers ────────────────────────────────────────────────────────────

# assert_exit <expected> <actual> <description>
assert_exit() {
    local expected="$1"
    local actual="$2"
    local desc="$3"

    TESTS_RUN=$((TESTS_RUN + 1))
    if [[ "$actual" -eq "$expected" ]]; then
        TESTS_PASSED=$((TESTS_PASSED + 1))
        printf '  PASS  exit=%d  %s\n' "$actual" "$desc"
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
        FAILED_NAMES+=("$desc")
        printf '  FAIL  exit=%d (expected %d)  %s\n' "$actual" "$expected" "$desc"
    fi
}

# assert_stdout_empty <stdout_file> <description>
assert_stdout_empty() {
    local stdout_file="$1"
    local desc="$2"

    TESTS_RUN=$((TESTS_RUN + 1))
    if [[ ! -s "$stdout_file" ]]; then
        TESTS_PASSED=$((TESTS_PASSED + 1))
        printf '  PASS  stdout-empty  %s\n' "$desc"
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
        FAILED_NAMES+=("stdout-empty: $desc")
        printf '  FAIL  stdout-empty  %s\n' "$desc"
        printf '        stdout was: %s\n' "$(cat "$stdout_file")"
    fi
}

# assert_stderr_contains <pattern> <stderr_file> <description>
assert_stderr_contains() {
    local pattern="$1"
    local stderr_file="$2"
    local desc="$3"

    TESTS_RUN=$((TESTS_RUN + 1))
    if grep -qiF "$pattern" "$stderr_file" 2>/dev/null; then
        TESTS_PASSED=$((TESTS_PASSED + 1))
        printf '  PASS  stderr-contains(%s)  %s\n' "$pattern" "$desc"
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
        FAILED_NAMES+=("stderr-contains($pattern): $desc")
        printf '  FAIL  stderr-contains(%s)  %s\n' "$pattern" "$desc"
        printf '        stderr was: %s\n' "$(cat "$stderr_file")"
    fi
}

# assert_jq <jq_expr> <json_file> <description>
# Runs jq -e against the file and passes when jq exits 0.
assert_jq() {
    local expr="$1"
    local json_file="$2"
    local desc="$3"

    TESTS_RUN=$((TESTS_RUN + 1))
    local actual
    if jq -e "$expr" "$json_file" >/dev/null 2>&1; then
        TESTS_PASSED=$((TESTS_PASSED + 1))
        printf '  PASS  jq(%s)  %s\n' "$expr" "$desc"
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
        FAILED_NAMES+=("jq($expr): $desc")
        printf '  FAIL  jq(%s)  %s\n' "$expr" "$desc"
        printf '        json was: %s\n' "$(cat "$json_file")"
    fi
}

# assert_valid_json <file> <description>
# Passes when the file parses as exactly one JSON document (entire file).
assert_valid_json() {
    local file="$1"
    local desc="$2"

    TESTS_RUN=$((TESTS_RUN + 1))
    if jq -e '.' "$file" >/dev/null 2>&1; then
        TESTS_PASSED=$((TESTS_PASSED + 1))
        printf '  PASS  valid-json  %s\n' "$desc"
    else
        TESTS_FAILED=$((TESTS_FAILED + 1))
        FAILED_NAMES+=("valid-json: $desc")
        printf '  FAIL  valid-json  %s\n' "$desc"
        printf '        file contents: %s\n' "$(cat "$file")"
    fi
}

# ── harness_summary ───────────────────────────────────────────────────────────
# Print totals. Exit 1 when any test failed.
harness_summary() {
    echo
    printf 'Tests run: %d  Passed: %d  Failed: %d\n' \
        "$TESTS_RUN" "$TESTS_PASSED" "$TESTS_FAILED"
    if [[ "$TESTS_FAILED" -gt 0 ]]; then
        echo 'FAILED tests:'
        for name in "${FAILED_NAMES[@]}"; do
            printf '  - %s\n' "$name"
        done
        return 1
    fi
    echo 'All tests passed.'
    return 0
}
