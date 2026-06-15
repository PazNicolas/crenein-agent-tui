package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// fixedStatusNow is the deterministic timestamp for status tests.
var fixedStatusNow = time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)

// validComposeContent is a minimal docker-compose.yml that contains the agent
// image marker and service image tags, enabling install-dir detection and
// image tag parsing.
const validComposeContent = `
version: "3"
services:
  agent:
    image: crenein/c-network-agent-back:1.8.3
  frontend:
    image: crenein/c-network-agent-front:1.8.3
  mongodb:
    image: mongodb/mongodb-community-server:7.0-ubi8
  influxdb:
    image: influxdb:2.7
  redis:
    image: redis:7.2
`

// runStatusCmd runs the status command with injected deps and returns
// stdout, stderr, and the resolved exit code.
func runStatusCmd(t *testing.T, args []string, deps statusDeps) cmdResult {
	t.Helper()

	root := newRootCmd()
	// Remove the real status command and add one with injected deps.
	for _, sub := range root.Commands() {
		if sub.Use == "status" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newStatusCmdWithDeps(deps))

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true

	root.SetArgs(append([]string{"status"}, args...))
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

// allRunningDeps builds a statusDeps where all 5 services report as running.
func allRunningDeps() statusDeps {
	states := []dockerx.ContainerState{
		{Service: "agent", Name: "stack-agent-1", Status: "Up 3 days (healthy)", Running: true, ImageID: "sha256:abc"},
		{Service: "frontend", Name: "stack-frontend-1", Status: "Up 3 days", Running: true, ImageID: "sha256:def"},
		{Service: "mongodb", Name: "stack-mongodb-1", Status: "Up 3 days", Running: true, ImageID: "sha256:ghi"},
		{Service: "influxdb", Name: "stack-influxdb-1", Status: "Up 3 days", Running: true, ImageID: "sha256:jkl"},
		{Service: "redis", Name: "stack-redis-1", Status: "Up 3 days", Running: true, ImageID: "sha256:mno"},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	return statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) {
			return "1.8.3", "health"
		},
		readFile:   fs.ReadFile,
		readDir:    func(path string) ([]string, error) { return nil, nil },
		installDir: ".",
		now:        func() time.Time { return fixedStatusNow },
	}
}

// ─── Task 5.4 tests ───────────────────────────────────────────────────────────

// TestStatus_AllRunning_Exit0 verifies that all 5 services running → exit 0.
func TestStatus_AllRunning_Exit0(t *testing.T) {
	res := runStatusCmd(t, nil, allRunningDeps())
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
}

// TestStatus_AllRunning_JSON_Exit0 verifies that --json also exits 0 when all running.
func TestStatus_AllRunning_JSON_Exit0(t *testing.T) {
	res := runStatusCmd(t, []string{"--json"}, allRunningDeps())
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
}

// TestStatus_AllRunning_JSON_Schema verifies the complete JSON schema for the healthy case.
func TestStatus_AllRunning_JSON_Schema(t *testing.T) {
	res := runStatusCmd(t, []string{"--json"}, allRunningDeps())
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.exitCode)
	}

	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %q", err, res.stdout)
	}

	// schema_version
	if doc.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", doc.SchemaVersion)
	}
	// command
	if doc.Command != "status" {
		t.Errorf("command = %q, want %q", doc.Command, "status")
	}
	// timestamp deterministic
	wantTS := fixedStatusNow.UTC().Format(time.RFC3339)
	if doc.Timestamp != wantTS {
		t.Errorf("timestamp = %q, want %q", doc.Timestamp, wantTS)
	}
	// agent
	if doc.Agent.Version != "1.8.3" {
		t.Errorf("agent.version = %q, want 1.8.3", doc.Agent.Version)
	}
	if doc.Agent.VersionSource != "health" {
		t.Errorf("agent.version_source = %q, want health", doc.Agent.VersionSource)
	}
	// mongo
	if doc.Mongo.Major != "7.x" {
		t.Errorf("mongo.major = %q, want 7.x", doc.Mongo.Major)
	}
	// services count
	if len(doc.Services) != 5 {
		t.Errorf("services count = %d, want 5", len(doc.Services))
	}
	// All should be running.
	for _, svc := range doc.Services {
		if svc.State != "running" {
			t.Errorf("service %s: state = %q, want running", svc.Name, svc.State)
		}
	}
	// install_dir present
	if doc.InstallDir == "" {
		t.Error("install_dir should be non-empty")
	}
}

// TestStatus_Degraded_Redis_Exited verifies degraded (redis exited) → exit 1.
func TestStatus_Degraded_Redis_Exited(t *testing.T) {
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 3 days", Running: true},
		{Service: "frontend", Status: "Up 3 days", Running: true},
		{Service: "mongodb", Status: "Up 3 days", Running: true},
		{Service: "influxdb", Status: "Up 3 days", Running: true},
		{Service: "redis", Status: "Exited (0) 2 hours ago", Running: false},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	// Human mode.
	resHuman := runStatusCmd(t, nil, deps)
	if resHuman.exitCode != ExitOpFailure {
		t.Errorf("human: exit code = %d, want %d (ExitOpFailure)", resHuman.exitCode, ExitOpFailure)
	}

	// JSON mode.
	resJSON := runStatusCmd(t, []string{"--json"}, deps)
	if resJSON.exitCode != ExitOpFailure {
		t.Errorf("json: exit code = %d, want %d (ExitOpFailure)", resJSON.exitCode, ExitOpFailure)
	}

	// redis.state == "exited" in JSON.
	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(resJSON.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %q", err, resJSON.stdout)
	}
	var redisSvc *statusSvcEntry
	for i := range doc.Services {
		if doc.Services[i].Name == "redis" {
			redisSvc = &doc.Services[i]
		}
	}
	if redisSvc == nil {
		t.Fatal("redis service not found in JSON output")
	}
	if redisSvc.State != "exited" {
		t.Errorf("redis.state = %q, want exited", redisSvc.State)
	}
}

// TestStatus_MissingService verifies that a missing container → state "missing".
func TestStatus_MissingService(t *testing.T) {
	// Only 4 of 5 services present (influxdb missing).
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 1 day", Running: true},
		{Service: "frontend", Status: "Up 1 day", Running: true},
		{Service: "mongodb", Status: "Up 1 day", Running: true},
		{Service: "redis", Status: "Up 1 day", Running: true},
		// influxdb absent
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "unknown", "unknown" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	res := runStatusCmd(t, []string{"--json"}, deps)
	if res.exitCode != ExitOpFailure {
		t.Errorf("exit code = %d, want %d (missing service → degraded)", res.exitCode, ExitOpFailure)
	}

	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	var influxSvc *statusSvcEntry
	for i := range doc.Services {
		if doc.Services[i].Name == "influxdb" {
			influxSvc = &doc.Services[i]
		}
	}
	if influxSvc == nil {
		t.Fatal("influxdb service not found in output")
	}
	if influxSvc.State != "missing" {
		t.Errorf("influxdb.state = %q, want missing", influxSvc.State)
	}
}

// TestStatus_NoInstall_Exit3 verifies that missing installation → exit 3 + suggestion.
func TestStatus_NoInstall_Exit3(t *testing.T) {
	// readFile returns error → no compose file found.
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return nil, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "unknown", "unknown" },
		readFile:           func(name string) ([]byte, error) { return nil, errors.New("not found") },
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         "", // force detection
		now:                func() time.Time { return fixedStatusNow },
	}

	// Human mode.
	resHuman := runStatusCmd(t, nil, deps)
	if resHuman.exitCode != ExitPreflight {
		t.Errorf("human: exit code = %d, want %d (ExitPreflight/no-install)", resHuman.exitCode, ExitPreflight)
	}
	if !strings.Contains(resHuman.stderr, "install") {
		t.Errorf("stderr should suggest `crenein-agent install`; got: %q", resHuman.stderr)
	}

	// JSON mode exit code must be the same.
	resJSON := runStatusCmd(t, []string{"--json"}, deps)
	if resJSON.exitCode != ExitPreflight {
		t.Errorf("json: exit code = %d, want %d", resJSON.exitCode, ExitPreflight)
	}
}

// TestStatus_VersionSourceDegradation verifies all three version-source paths.
func TestStatus_VersionSourceDegradation(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		source     string
		wantSource string
	}{
		{"health", "1.8.3", "health", "health"},
		{"image_tag", "1.8.2", "image_tag", "image_tag"},
		{"unknown", "unknown", "unknown", "unknown"},
	}

	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 1 day", Running: true},
		{Service: "frontend", Status: "Up 1 day", Running: true},
		{Service: "mongodb", Status: "Up 1 day", Running: true},
		{Service: "influxdb", Status: "Up 1 day", Running: true},
		{Service: "redis", Status: "Up 1 day", Running: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ver, src := tc.version, tc.source
			deps := statusDeps{
				composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
					return states, nil
				},
				detectAgentVersion: func(_ context.Context) (string, string) { return ver, src },
				readFile:           fs.ReadFile,
				readDir:            func(string) ([]string, error) { return nil, nil },
				installDir:         ".",
				now:                func() time.Time { return fixedStatusNow },
			}

			res := runStatusCmd(t, []string{"--json"}, deps)
			var doc statusJSONDoc
			if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if doc.Agent.VersionSource != tc.wantSource {
				t.Errorf("agent.version_source = %q, want %q", doc.Agent.VersionSource, tc.wantSource)
			}
		})
	}
}

// TestStatus_JSON_AllRequiredFields verifies that all documented fields are present.
func TestStatus_JSON_AllRequiredFields(t *testing.T) {
	res := runStatusCmd(t, []string{"--json"}, allRunningDeps())

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	topLevel := []string{"schema_version", "command", "timestamp", "cli_version", "install_dir", "agent", "mongo", "services"}
	for _, field := range topLevel {
		if _, ok := raw[field]; !ok {
			t.Errorf("top-level field %q missing from JSON output", field)
		}
	}

	// Verify agent sub-fields.
	var agentRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["agent"], &agentRaw); err != nil {
		t.Fatalf("agent field not a JSON object: %v", err)
	}
	for _, f := range []string{"version", "version_source", "image", "health"} {
		if _, ok := agentRaw[f]; !ok {
			t.Errorf("agent.%s missing from JSON output", f)
		}
	}

	// Verify each service entry has required fields.
	var svcs []map[string]json.RawMessage
	if err := json.Unmarshal(raw["services"], &svcs); err != nil {
		t.Fatalf("services not a JSON array: %v", err)
	}
	for _, svc := range svcs {
		for _, f := range []string{"name", "image", "state", "health", "status_text", "uptime_seconds"} {
			if _, ok := svc[f]; !ok {
				name := "?"
				if n, ok2 := svc["name"]; ok2 {
					_ = json.Unmarshal(n, &name)
				}
				t.Errorf("service %s: field %q missing", name, f)
			}
		}
	}
}

// TestStatus_ExitCodeConsistentHumanAndJSON verifies that --json doesn't change exit code.
func TestStatus_ExitCodeConsistentHumanAndJSON(t *testing.T) {
	cases := []struct {
		name     string
		deps     func() statusDeps
		wantCode int
	}{
		{"all-running", allRunningDeps, ExitSuccess},
		{"degraded", func() statusDeps {
			states := []dockerx.ContainerState{
				{Service: "agent", Status: "Up 1 day", Running: true},
				{Service: "frontend", Status: "Up 1 day", Running: true},
				{Service: "mongodb", Status: "Up 1 day", Running: true},
				{Service: "influxdb", Status: "Up 1 day", Running: true},
				{Service: "redis", Status: "Exited (1) 1 hour ago", Running: false},
			}
			fs := dockerx.NewFakeFS(map[string][]byte{"./docker-compose.yml": []byte(validComposeContent)})
			return statusDeps{
				composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
					return states, nil
				},
				detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
				readFile:           fs.ReadFile,
				readDir:            func(string) ([]string, error) { return nil, nil },
				installDir:         ".",
				now:                func() time.Time { return fixedStatusNow },
			}
		}, ExitOpFailure},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := tc.deps()
			resHuman := runStatusCmd(t, nil, deps)
			resJSON := runStatusCmd(t, []string{"--json"}, deps)

			if resHuman.exitCode != tc.wantCode {
				t.Errorf("human: exit code = %d, want %d", resHuman.exitCode, tc.wantCode)
			}
			if resJSON.exitCode != tc.wantCode {
				t.Errorf("json: exit code = %d, want %d", resJSON.exitCode, tc.wantCode)
			}
			if resHuman.exitCode != resJSON.exitCode {
				t.Errorf("exit codes differ: human=%d json=%d", resHuman.exitCode, resJSON.exitCode)
			}
		})
	}
}

// TestStatus_Timestamp_Deterministic verifies injected now controls the timestamp.
func TestStatus_Timestamp_Deterministic(t *testing.T) {
	res := runStatusCmd(t, []string{"--json"}, allRunningDeps())

	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := "2026-06-14T12:00:00Z"
	if doc.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", doc.Timestamp, want)
	}
}

// ─── Unit tests for uptime parsing ────────────────────────────────────────────

func TestParseUptimeSeconds(t *testing.T) {
	cases := []struct {
		status string
		want   int64
	}{
		{"Up 3 days", 3 * 86400},
		{"Up 2 minutes", 2 * 60},
		{"Up About an hour", 3600},
		{"Up 1 hour", 3600},
		{"Up 2 hours", 2 * 3600},
		{"Up 5 weeks", 5 * 7 * 86400},
		{"Up 3 days (healthy)", 3 * 86400},
		{"Up 3 days (unhealthy)", 3 * 86400},
		{"Exited (0) 2 hours ago", 0},
		{"", 0},
		{"Up", 0},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			got := parseUptimeSeconds(tc.status)
			if got != tc.want {
				t.Errorf("parseUptimeSeconds(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

// TestParseHealthFromStatus verifies health extraction from status text.
func TestParseHealthFromStatus(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"Up 3 days (healthy)", "healthy"},
		{"Up 3 days (unhealthy)", "unhealthy"},
		{"Up 3 days", "none"},
		{"Exited (0)", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			got := parseHealthFromStatus(tc.status)
			if got != tc.want {
				t.Errorf("parseHealthFromStatus(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestImageTagFromCompose verifies image tag extraction from compose content.
func TestImageTagFromCompose(t *testing.T) {
	cases := []struct {
		service string
		want    string
	}{
		{"agent", "crenein/c-network-agent-back:1.8.3"},
		{"mongodb", "mongodb/mongodb-community-server:7.0-ubi8"},
		{"redis", "redis:7.2"},
		{"nonexistent", ""},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			got := imageTagFromCompose(validComposeContent, tc.service)
			if got != tc.want {
				t.Errorf("imageTagFromCompose(_, %q) = %q, want %q", tc.service, got, tc.want)
			}
		})
	}
}

// TestMongoInfoFromCompose verifies mongo image and major extraction.
func TestMongoInfoFromCompose(t *testing.T) {
	imageTag, major := mongoInfoFromCompose(validComposeContent)
	if imageTag != "mongodb/mongodb-community-server:7.0-ubi8" {
		t.Errorf("imageTag = %q, want mongodb/mongodb-community-server:7.0-ubi8", imageTag)
	}
	if major != "7.x" {
		t.Errorf("major = %q, want 7.x", major)
	}
}

// TestStatus_Unhealthy_Exit1 verifies that a running-but-unhealthy service → exit 1.
func TestStatus_Unhealthy_Exit1(t *testing.T) {
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 3 days (unhealthy)", Running: true},
		{Service: "frontend", Status: "Up 3 days", Running: true},
		{Service: "mongodb", Status: "Up 3 days", Running: true},
		{Service: "influxdb", Status: "Up 3 days", Running: true},
		{Service: "redis", Status: "Up 3 days", Running: true},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	res := runStatusCmd(t, []string{"--json"}, deps)
	if res.exitCode != ExitOpFailure {
		t.Errorf("exit code = %d, want %d (unhealthy → degraded)", res.exitCode, ExitOpFailure)
	}

	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var agentSvc *statusSvcEntry
	for i := range doc.Services {
		if doc.Services[i].Name == "agent" {
			agentSvc = &doc.Services[i]
		}
	}
	if agentSvc == nil {
		t.Fatal("agent service not found")
	}
	if agentSvc.Health != "unhealthy" {
		t.Errorf("agent.health = %q, want unhealthy", agentSvc.Health)
	}
	if agentSvc.State != "running" {
		t.Errorf("agent.state = %q, want running (container is still running)", agentSvc.State)
	}
}

// TestStatus_AgentHealth_Unknown verifies that agent.health is "unknown" (not "none")
// when the agent container has no healthcheck annotation.
func TestStatus_AgentHealth_Unknown(t *testing.T) {
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 1 day", Running: true},
		{Service: "frontend", Status: "Up 1 day", Running: true},
		{Service: "mongodb", Status: "Up 1 day", Running: true},
		{Service: "influxdb", Status: "Up 1 day", Running: true},
		{Service: "redis", Status: "Up 1 day", Running: true},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	res := runStatusCmd(t, []string{"--json"}, deps)
	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Agent.Health != "unknown" {
		t.Errorf("agent.health = %q, want %q", doc.Agent.Health, "unknown")
	}
}

// TestContainerStateFromStatus verifies state derivation from status text.
func TestContainerStateFromStatus(t *testing.T) {
	cases := []struct {
		status  string
		running bool
		want    string
	}{
		{"Up 3 days", true, "running"},
		{"Up 3 days (healthy)", true, "running"},
		{"Exited (0) 2 hours ago", false, "exited"},
		{"Restarting (1) 30 seconds ago", false, "restarting"},
		{"Created", false, "created"},
		{"Paused", false, "paused"},
		{"", false, "exited"},
		{"", true, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.status+"_running="+fmt.Sprintf("%v", tc.running), func(t *testing.T) {
			got := containerStateFromStatus(tc.status, tc.running)
			if got != tc.want {
				t.Errorf("containerStateFromStatus(%q, %v) = %q, want %q", tc.status, tc.running, got, tc.want)
			}
		})
	}
}

// TestStatus_RestartingState verifies that a restarting container → state "restarting".
func TestStatus_RestartingState(t *testing.T) {
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 1 day", Running: true},
		{Service: "frontend", Status: "Up 1 day", Running: true},
		{Service: "mongodb", Status: "Up 1 day", Running: true},
		{Service: "influxdb", Status: "Up 1 day", Running: true},
		{Service: "redis", Status: "Restarting (1) 5 seconds ago", Running: false},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	deps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	res := runStatusCmd(t, []string{"--json"}, deps)
	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var redisSvc *statusSvcEntry
	for i := range doc.Services {
		if doc.Services[i].Name == "redis" {
			redisSvc = &doc.Services[i]
		}
	}
	if redisSvc == nil {
		t.Fatal("redis not found")
	}
	if redisSvc.State != "restarting" {
		t.Errorf("redis.state = %q, want restarting", redisSvc.State)
	}
}

// ─── Task 4.4 — update-available in status ────────────────────────────────────

// statusManifest builds a minimal valid Manifest for status update tests.
// cliLatest and agentLatest control the "latest" version fields.
func statusManifest(cliLatest, agentLatest string) *release.Manifest {
	return &release.Manifest{
		Agent: release.AgentSection{
			Latest: agentLatest,
			Releases: map[string]release.AgentRelease{
				agentLatest: {
					Date:  "2026-06-14",
					Image: "crenein/c-network-agent-back:" + agentLatest,
					Mongo: map[string]string{"7": "mongo7", "4": "mongo4"},
					Notes: "Agent notes",
				},
			},
		},
		CLI: release.CLISection{
			Latest: cliLatest,
			Releases: map[string]release.CLIRelease{
				cliLatest: {Date: "2026-06-14", Notes: "CLI notes"},
			},
		},
	}
}

// allRunningDepsWithManifest returns allRunningDeps plus the given manifest client.
func allRunningDepsWithManifest(mc release.Client) statusDeps {
	d := allRunningDeps()
	d.manifestClient = mc
	return d
}

// TestStatus_UpdateAvailable_JSON_UpdatesObject verifies the "updates" object
// appears in --json when an update is available and has all required fields.
func TestStatus_UpdateAvailable_JSON_UpdatesObject(t *testing.T) {
	fetchedAt := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m := statusManifest("0.2.0", "1.9.0")
	m.FetchedAt = fetchedAt

	// CLI: 0.1.0 < 0.2.0 → update available.
	// Agent: 1.8.3 < 1.9.0 → update available.
	mc := &fakeManifestClient{manifest: m}
	deps := allRunningDepsWithManifest(mc)
	// Override build.version via test helper approach: use collectStatus directly
	// with a known cliVersion. However, build.version is package-level so we test
	// the integration via the full command — build.version == "" in tests → "dev".
	// Instead, supply a version by setting it and restoring.
	origVersion := build.version
	build.version = "0.1.0"
	defer func() { build.version = origVersion }()

	res := runStatusCmd(t, []string{"--json"}, deps)
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %q", err, res.stdout)
	}

	updatesRaw, ok := raw["updates"]
	if !ok {
		t.Fatal("top-level field 'updates' missing from JSON output")
	}

	var updates statusUpdatesInfo
	if err := json.Unmarshal(updatesRaw, &updates); err != nil {
		t.Fatalf("updates is not a valid JSON object: %v", err)
	}

	if updates.CLIVersion != "0.1.0" {
		t.Errorf("updates.cli_version = %q, want 0.1.0", updates.CLIVersion)
	}
	if updates.CLILatest != "0.2.0" {
		t.Errorf("updates.cli_latest = %q, want 0.2.0", updates.CLILatest)
	}
	if !updates.CLIUpdateAvailable {
		t.Error("updates.cli_update_available should be true")
	}
	if updates.AgentVersion != "1.8.3" {
		t.Errorf("updates.agent_version = %q, want 1.8.3", updates.AgentVersion)
	}
	if updates.AgentLatest != "1.9.0" {
		t.Errorf("updates.agent_latest = %q, want 1.9.0", updates.AgentLatest)
	}
	if !updates.AgentUpdateAvailable {
		t.Error("updates.agent_update_available should be true")
	}
	if updates.LastChecked == nil {
		t.Fatal("updates.last_checked should not be null")
	}
	wantLastChecked := fetchedAt.UTC().Format(time.RFC3339)
	if *updates.LastChecked != wantLastChecked {
		t.Errorf("updates.last_checked = %q, want %q", *updates.LastChecked, wantLastChecked)
	}
}

// TestStatus_UpdateUpToDate_JSON verifies update fields when CLI and agent are
// already at the latest version.
func TestStatus_UpdateUpToDate_JSON(t *testing.T) {
	fetchedAt := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m := statusManifest("0.1.0", "1.8.3")
	m.FetchedAt = fetchedAt

	mc := &fakeManifestClient{manifest: m}
	deps := allRunningDepsWithManifest(mc)

	origVersion := build.version
	build.version = "0.1.0"
	defer func() { build.version = origVersion }()

	res := runStatusCmd(t, []string{"--json"}, deps)
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d; stderr: %q", res.exitCode, res.stderr)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	var updates statusUpdatesInfo
	if err := json.Unmarshal(raw["updates"], &updates); err != nil {
		t.Fatalf("updates not a valid object: %v", err)
	}
	if updates.CLIUpdateAvailable {
		t.Error("cli_update_available should be false when already up to date")
	}
	if updates.AgentUpdateAvailable {
		t.Error("agent_update_available should be false when already up to date")
	}
}

// TestStatus_ManifestFetchFails_StatusDoesNotFail verifies that a manifest
// fetch error does NOT cause status to fail — exit code is unchanged and
// update booleans are false.
func TestStatus_ManifestFetchFails_StatusDoesNotFail(t *testing.T) {
	fetchErr := cnerr.New("release.FetchManifest: network unavailable", "check connectivity")
	mc := &fakeManifestClient{fetchErr: fetchErr}
	deps := allRunningDepsWithManifest(mc)

	origVersion := build.version
	build.version = "0.1.0"
	defer func() { build.version = origVersion }()

	// Human mode: must not fail.
	resHuman := runStatusCmd(t, nil, deps)
	if resHuman.exitCode != 0 {
		t.Errorf("human: exit code = %d, want 0 (manifest error must not change exit code)", resHuman.exitCode)
	}
	if !strings.Contains(resHuman.stderr, "warning") {
		t.Errorf("human: stderr should contain 'warning' about manifest fetch failure; got: %q", resHuman.stderr)
	}

	// JSON mode: must not fail, updates object present with false booleans.
	resJSON := runStatusCmd(t, []string{"--json"}, deps)
	if resJSON.exitCode != 0 {
		t.Errorf("json: exit code = %d, want 0", resJSON.exitCode)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resJSON.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout: %q", err, resJSON.stdout)
	}

	updatesRaw, ok := raw["updates"]
	if !ok {
		t.Fatal("updates field should be present even when manifest fetch fails")
	}
	var updates statusUpdatesInfo
	if err := json.Unmarshal(updatesRaw, &updates); err != nil {
		t.Fatalf("updates not a valid object: %v", err)
	}
	if updates.CLIUpdateAvailable {
		t.Error("cli_update_available should be false when manifest unreachable")
	}
	if updates.AgentUpdateAvailable {
		t.Error("agent_update_available should be false when manifest unreachable")
	}
	if updates.LastChecked != nil {
		t.Errorf("last_checked should be null when manifest unreachable, got %q", *updates.LastChecked)
	}
}

// TestStatus_DevBuild_NoCLIUpdateAvailable verifies that a dev build (version="")
// never reports cli_update_available=true.
func TestStatus_DevBuild_NoCLIUpdateAvailable(t *testing.T) {
	m := statusManifest("0.2.0", "1.8.3")
	m.FetchedAt = time.Now()
	mc := &fakeManifestClient{manifest: m}
	deps := allRunningDepsWithManifest(mc)

	// build.version is "" in test builds (dev build).
	origVersion := build.version
	build.version = ""
	defer func() { build.version = origVersion }()

	res := runStatusCmd(t, []string{"--json"}, deps)
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d; stderr: %q", res.exitCode, res.stderr)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var updates statusUpdatesInfo
	if err := json.Unmarshal(raw["updates"], &updates); err != nil {
		t.Fatalf("updates not valid: %v", err)
	}
	if updates.CLIUpdateAvailable {
		t.Error("cli_update_available must be false for dev builds")
	}
}

// TestStatus_UpdateAvailable_Human_Lines verifies that update-available info
// appears in human-readable output.
func TestStatus_UpdateAvailable_Human_Lines(t *testing.T) {
	m := statusManifest("0.2.0", "1.9.0")
	m.FetchedAt = time.Now()
	mc := &fakeManifestClient{manifest: m}
	deps := allRunningDepsWithManifest(mc)

	origVersion := build.version
	build.version = "0.1.0"
	defer func() { build.version = origVersion }()

	res := runStatusCmd(t, nil, deps)
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "update available") {
		t.Errorf("human output should contain 'update available'; got: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "0.2.0") {
		t.Errorf("human output should show CLI latest 0.2.0; got: %q", res.stdout)
	}
}

// TestStatus_SchemaVersion1_WithUpdates verifies schema_version stays 1 after
// adding the updates object.
func TestStatus_SchemaVersion1_WithUpdates(t *testing.T) {
	m := statusManifest("0.1.0", "1.8.3")
	m.FetchedAt = time.Now()
	mc := &fakeManifestClient{manifest: m}
	deps := allRunningDepsWithManifest(mc)

	origVersion := build.version
	build.version = "0.1.0"
	defer func() { build.version = origVersion }()

	res := runStatusCmd(t, []string{"--json"}, deps)
	var doc statusJSONDoc
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", doc.SchemaVersion)
	}
}

// TestStatus_ExitCode_NotChangedByManifest verifies that degraded-stack exit
// code is NOT affected by the manifest (offline or online).
func TestStatus_ExitCode_NotChangedByManifest(t *testing.T) {
	states := []dockerx.ContainerState{
		{Service: "agent", Status: "Up 1 day", Running: true},
		{Service: "frontend", Status: "Up 1 day", Running: true},
		{Service: "mongodb", Status: "Up 1 day", Running: true},
		{Service: "influxdb", Status: "Up 1 day", Running: true},
		{Service: "redis", Status: "Exited (0) 1 hour ago", Running: false},
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(validComposeContent),
	})
	baseDeps := statusDeps{
		composePs: func(_ context.Context, _ string, _ []string) ([]dockerx.ContainerState, error) {
			return states, nil
		},
		detectAgentVersion: func(_ context.Context) (string, string) { return "1.8.3", "health" },
		readFile:           fs.ReadFile,
		readDir:            func(string) ([]string, error) { return nil, nil },
		installDir:         ".",
		now:                func() time.Time { return fixedStatusNow },
	}

	fetchErr := cnerr.New("net error", "check connectivity")
	mc := &fakeManifestClient{fetchErr: fetchErr}
	baseDeps.manifestClient = mc

	res := runStatusCmd(t, []string{"--json"}, baseDeps)
	if res.exitCode != ExitOpFailure {
		t.Errorf("exit code = %d, want %d (degraded must remain degraded regardless of manifest)", res.exitCode, ExitOpFailure)
	}
}
