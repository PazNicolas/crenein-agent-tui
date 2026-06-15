package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// makeDoctorReport builds a DoctorReport from check specs for test injection.
// The summary is computed as the worst non-SKIP status found.
func makeDoctorReport(checks []engine.Check) engine.DoctorReport {
	summary := engine.StatusOK
	for _, c := range checks {
		switch {
		case c.Status == engine.StatusCritical:
			summary = engine.StatusCritical
		case c.Status == engine.StatusWarning && summary != engine.StatusCritical:
			summary = engine.StatusWarning
		}
	}
	return engine.DoctorReport{Checks: checks, Summary: summary}
}

// runDoctorCmd runs the doctor command with the given args and injected doctorFn,
// returning stdout, stderr, and the resolved exit code.
func runDoctorCmd(t *testing.T, args []string, ddeps doctorDeps, report engine.DoctorReport) cmdResult {
	t.Helper()

	// Override the global doctorFn.
	original := doctorFn
	doctorFn = func(_ context.Context, _ engine.Deps, _ engine.DoctorOptions) engine.DoctorReport {
		return report
	}
	defer func() { doctorFn = original }()

	root := newRootCmd()
	for _, sub := range root.Commands() {
		if sub.Use == "doctor" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newDoctorCmdWithDeps(ddeps))

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true

	root.SetArgs(append([]string{"doctor"}, args...))
	err := root.Execute()

	code := 0
	if err != nil {
		var ecErr *exitCodeError
		if errors.As(err, &ecErr) {
			code = ecErr.code
		} else {
			code = 1
		}
	}
	return cmdResult{
		stdout:   outBuf.String(),
		stderr:   errBuf.String(),
		exitCode: code,
	}
}

// fixedNow is a deterministic timestamp for testing.
var fixedNow = time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)

// ddepsFixed returns a doctorDeps with a fixed timestamp.
func ddepsFixed() doctorDeps {
	return doctorDeps{now: func() time.Time { return fixedNow }}
}

// ─── fixtures ────────────────────────────────────────────────────────────────

func allPassChecks() []engine.Check {
	return []engine.Check{
		{
			ID: "docker.installed", Name: "Docker installed",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "docker binary found", DurationMs: 1,
		},
		{
			ID: "docker.daemon", Name: "Docker daemon running",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "daemon responded to ping", DurationMs: 2,
		},
		{
			ID: "disk.space", Name: "Disk space (>2 GB free)",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "5000 MB free", DurationMs: 1,
		},
	}
}

func warningOnlyChecks() []engine.Check {
	return []engine.Check{
		{
			ID: "docker.installed", Name: "Docker installed",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "docker binary found",
		},
		{
			ID: "net.dockerhub", Name: "Docker Hub connectivity",
			Status: engine.StatusWarning, Severity: engine.StatusWarning,
			Detail: "cannot reach hub.docker.com", FixSuggestion: "check firewall",
		},
	}
}

func criticalChecks() []engine.Check {
	return []engine.Check{
		{
			ID: "docker.installed", Name: "Docker installed",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "docker binary found",
		},
		{
			ID: "docker.daemon", Name: "Docker daemon running",
			Status: engine.StatusCritical, Severity: engine.StatusCritical,
			Detail: "daemon not responding", FixSuggestion: "systemctl start docker",
		},
	}
}

func skipChecks() []engine.Check {
	return []engine.Check{
		{
			ID: "docker.installed", Name: "Docker installed",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "docker binary found",
		},
		{
			ID: "docker.compose", Name: "Compose available",
			Status: engine.StatusSkip, Severity: engine.StatusCritical,
			Detail: "skipped: prerequisite check failed",
		},
		{
			ID: "services.running", Name: "Agent stack service states",
			Status: engine.StatusSkip, Severity: engine.StatusCritical,
			Detail: "skipped: prerequisite check failed",
		},
	}
}

// ─── Task 4.1 — human renderer ───────────────────────────────────────────────

func TestDoctor_AllPass_HumanExit0(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	// Human output goes to stdout.
	if res.stdout == "" {
		t.Error("stdout should contain doctor output")
	}
	if res.stderr != "" {
		t.Errorf("stderr should be empty in human mode, got: %q", res.stderr)
	}
}

func TestDoctor_HumanPassMarker(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	// Passed checks should show ✓.
	if !containsAny(res.stdout, "✓") {
		t.Errorf("stdout should contain ✓ marker for passed checks; got: %q", res.stdout)
	}
}

func TestDoctor_HumanWarningMarker(t *testing.T) {
	report := makeDoctorReport(warningOnlyChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if !containsAny(res.stdout, "!") {
		t.Errorf("stdout should contain ! marker for warning checks; got: %q", res.stdout)
	}
}

func TestDoctor_HumanCriticalMarker(t *testing.T) {
	report := makeDoctorReport(criticalChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if !containsAny(res.stdout, "✗") {
		t.Errorf("stdout should contain ✗ marker for critical checks; got: %q", res.stdout)
	}
}

func TestDoctor_HumanSkipMarker(t *testing.T) {
	report := makeDoctorReport(skipChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if !containsAny(res.stdout, "-") {
		t.Errorf("stdout should contain - marker for skipped checks; got: %q", res.stdout)
	}
}

func TestDoctor_HumanShowsSummaryLine(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if !containsAny(res.stdout, "Total:") {
		t.Errorf("stdout should contain summary line; got: %q", res.stdout)
	}
}

// ─── Task 4.2 — JSON shape ───────────────────────────────────────────────────

func TestDoctor_JSON_AllPass_Schema(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.exitCode)
	}

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %q", err, res.stdout)
	}

	if doc.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", doc.SchemaVersion)
	}
	if doc.Command != "doctor" {
		t.Errorf("command = %q, want %q", doc.Command, "doctor")
	}
	if doc.Timestamp != fixedNow.UTC().Format(time.RFC3339) {
		t.Errorf("timestamp = %q, want %q", doc.Timestamp, fixedNow.UTC().Format(time.RFC3339))
	}
	if doc.Summary.Status != "ok" {
		t.Errorf("summary.status = %q, want %q", doc.Summary.Status, "ok")
	}
	if doc.Summary.Total != len(allPassChecks()) {
		t.Errorf("summary.total = %d, want %d", doc.Summary.Total, len(allPassChecks()))
	}
	if doc.Summary.Passed != len(allPassChecks()) {
		t.Errorf("summary.passed = %d, want %d", doc.Summary.Passed, len(allPassChecks()))
	}
	if doc.Summary.Warnings != 0 {
		t.Errorf("summary.warnings = %d, want 0", doc.Summary.Warnings)
	}
	if doc.Summary.Critical != 0 {
		t.Errorf("summary.critical = %d, want 0", doc.Summary.Critical)
	}
	if doc.Summary.Skipped != 0 {
		t.Errorf("summary.skipped = %d, want 0", doc.Summary.Skipped)
	}
}

func TestDoctor_JSON_CheckFields(t *testing.T) {
	checks := []engine.Check{
		{
			ID: "docker.installed", Name: "Docker installed",
			Status: engine.StatusOK, Severity: engine.StatusCritical,
			Detail: "docker binary found", DurationMs: 42,
		},
	}
	report := makeDoctorReport(checks)
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(doc.Checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(doc.Checks))
	}

	c := doc.Checks[0]
	// Verify all 8 required fields per check.
	if c.ID != "docker.installed" {
		t.Errorf("check.id = %q, want %q", c.ID, "docker.installed")
	}
	if c.Name != "Docker installed" {
		t.Errorf("check.name = %q, want %q", c.Name, "Docker installed")
	}
	if c.Severity != "critical" {
		t.Errorf("check.severity = %q, want %q", c.Severity, "critical")
	}
	if c.Status != "pass" {
		t.Errorf("check.status = %q, want %q", c.Status, "pass")
	}
	if c.Message != "docker binary found" {
		t.Errorf("check.message = %q, want %q", c.Message, "docker binary found")
	}
	if c.Fix != nil {
		t.Errorf("check.fix should be null for passing check, got: %v", *c.Fix)
	}
	if c.DurationMs != 42 {
		t.Errorf("check.duration_ms = %d, want 42", c.DurationMs)
	}
}

func TestDoctor_JSON_FixNotNullWhenPresent(t *testing.T) {
	checks := []engine.Check{
		{
			ID: "net.dockerhub", Name: "Docker Hub connectivity",
			Status: engine.StatusWarning, Severity: engine.StatusWarning,
			Detail:        "cannot reach hub.docker.com",
			FixSuggestion: "check firewall",
		},
	}
	report := makeDoctorReport(checks)
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(doc.Checks) == 0 {
		t.Fatal("expected at least 1 check")
	}
	c := doc.Checks[0]
	if c.Fix == nil {
		t.Error("check.fix should be non-null when FixSuggestion is set")
	} else if *c.Fix != "check firewall" {
		t.Errorf("check.fix = %q, want %q", *c.Fix, "check firewall")
	}
}

func TestDoctor_JSON_StatusMappings(t *testing.T) {
	checks := []engine.Check{
		{ID: "a", Status: engine.StatusOK, Severity: engine.StatusCritical, Name: "A", Detail: "ok"},
		{ID: "b", Status: engine.StatusWarning, Severity: engine.StatusWarning, Name: "B", Detail: "warn", FixSuggestion: "fix"},
		{ID: "c", Status: engine.StatusCritical, Severity: engine.StatusCritical, Name: "C", Detail: "crit", FixSuggestion: "fix"},
		{ID: "d", Status: engine.StatusSkip, Severity: engine.StatusCritical, Name: "D", Detail: "skip"},
	}
	report := engine.DoctorReport{Checks: checks, Summary: engine.StatusCritical}
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	want := map[string]string{"a": "pass", "b": "warn", "c": "fail", "d": "skip"}
	for _, c := range doc.Checks {
		if w, ok := want[c.ID]; ok {
			if c.Status != w {
				t.Errorf("check %q: status = %q, want %q", c.ID, c.Status, w)
			}
		}
	}
}

func TestDoctor_JSON_SummaryStatusWarning(t *testing.T) {
	report := makeDoctorReport(warningOnlyChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Summary.Status != "warning" {
		t.Errorf("summary.status = %q, want %q", doc.Summary.Status, "warning")
	}
	if doc.Summary.Warnings != 1 {
		t.Errorf("summary.warnings = %d, want 1", doc.Summary.Warnings)
	}
}

func TestDoctor_JSON_SummaryStatusCritical(t *testing.T) {
	report := makeDoctorReport(criticalChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Summary.Status != "critical" {
		t.Errorf("summary.status = %q, want %q", doc.Summary.Status, "critical")
	}
}

func TestDoctor_JSON_SkipPropagation(t *testing.T) {
	report := makeDoctorReport(skipChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Summary should be ok (SKIP doesn't elevate).
	if doc.Summary.Status != "ok" {
		t.Errorf("summary.status = %q, want %q (SKIP must not elevate)", doc.Summary.Status, "ok")
	}
	// Skipped count should be 2.
	if doc.Summary.Skipped != 2 {
		t.Errorf("summary.skipped = %d, want 2", doc.Summary.Skipped)
	}

	// Verify skip status in checks.
	for _, c := range doc.Checks {
		if c.ID == "docker.compose" || c.ID == "services.running" {
			if c.Status != "skip" {
				t.Errorf("check %q: status = %q, want %q", c.ID, c.Status, "skip")
			}
		}
	}
}

func TestDoctor_JSON_OnlyToStdout(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	// stderr must be empty in JSON mode (no stray output).
	if res.stderr != "" {
		t.Errorf("stderr must be empty in --json mode, got: %q", res.stderr)
	}
	// stdout must be valid JSON.
	var m map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &m); err != nil {
		t.Errorf("stdout is not valid JSON: %v\nstdout: %q", err, res.stdout)
	}
}

// ─── Task 4.3 — exit codes ───────────────────────────────────────────────────

func TestDoctor_ExitCode0_AllPass(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.exitCode)
	}
}

func TestDoctor_ExitCode0_SkipOnly(t *testing.T) {
	// SKIP checks should not elevate exit code above 0 (treated like pass).
	checks := []engine.Check{
		{ID: "docker.compose", Status: engine.StatusSkip, Severity: engine.StatusCritical, Name: "Compose", Detail: "skip"},
	}
	report := engine.DoctorReport{Checks: checks, Summary: engine.StatusOK}
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0 for skip-only report", res.exitCode)
	}
}

func TestDoctor_ExitCode1_WarningsOnly(t *testing.T) {
	report := makeDoctorReport(warningOnlyChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1 for warnings-only report", res.exitCode)
	}
}

func TestDoctor_ExitCode2_Critical(t *testing.T) {
	report := makeDoctorReport(criticalChecks())
	res := runDoctorCmd(t, nil, ddepsFixed(), report)
	if res.exitCode != ExitDoctorCritical {
		t.Errorf("exit code = %d, want %d for critical report", res.exitCode, ExitDoctorCritical)
	}
}

// ─── Task 4.4 — exit codes consistent with and without --json ────────────────

func TestDoctor_ExitCodesConsistentWithJSON(t *testing.T) {
	cases := []struct {
		name     string
		checks   []engine.Check
		wantCode int
	}{
		{"all-pass", allPassChecks(), 0},
		{"warnings-only", warningOnlyChecks(), 1},
		{"critical", criticalChecks(), ExitDoctorCritical},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := makeDoctorReport(tc.checks)

			resHuman := runDoctorCmd(t, nil, ddepsFixed(), report)
			resJSON := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

			if resHuman.exitCode != tc.wantCode {
				t.Errorf("human: exit code = %d, want %d", resHuman.exitCode, tc.wantCode)
			}
			if resJSON.exitCode != tc.wantCode {
				t.Errorf("json: exit code = %d, want %d", resJSON.exitCode, tc.wantCode)
			}
			if resHuman.exitCode != resJSON.exitCode {
				t.Errorf("human exit code (%d) != json exit code (%d)", resHuman.exitCode, resJSON.exitCode)
			}
		})
	}
}

// TestDoctor_Timestamp_Deterministic verifies that the injected `now` function
// controls the timestamp in the JSON output.
func TestDoctor_Timestamp_Deterministic(t *testing.T) {
	report := makeDoctorReport(allPassChecks())
	res := runDoctorCmd(t, []string{"--json"}, ddepsFixed(), report)

	var doc doctorJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := "2026-06-14T12:00:00Z"
	if doc.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", doc.Timestamp, want)
	}
}

// ─── utility ─────────────────────────────────────────────────────────────────

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
