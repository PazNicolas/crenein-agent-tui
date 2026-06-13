package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// findCheck returns the first Check with the given ID or fails the test.
func findCheck(t *testing.T, report DoctorReport, id string) Check {
	t.Helper()
	for _, c := range report.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q not found in report; got IDs: %v", id, checkIDs(report))
	return Check{}
}

func checkIDs(r DoctorReport) []string {
	ids := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		ids[i] = c.ID
	}
	return ids
}

// allOKDeps returns a Deps configured so every check passes.
func allOKDeps(installDir string) (Deps, *dockerx.FakeFS) {
	// Compose file that references the agent image.
	composePath := installDir + "/docker-compose.yml"
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  agent:\n  frontend:\n")

	// .env with mode 600.
	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath: composeContent,
		envPath:     []byte("INFLUXDB_TOKEN=abc123\n"),
		installDir + "/c-network-agent-back/certs/cert.pem":  []byte("CERT"),
		installDir + "/c-network-agent-back/certs/key.pem":   []byte("KEY"),
		installDir + "/c-network-agent-front/certs/cert.pem": []byte("CERT"),
		installDir + "/c-network-agent-front/certs/key.pem":  []byte("KEY"),
	})
	// Set correct modes for certs.
	_ = fs.Chmod(installDir+"/c-network-agent-back/certs/cert.pem", 0o644)
	_ = fs.Chmod(installDir+"/c-network-agent-back/certs/key.pem", 0o600)
	_ = fs.Chmod(installDir+"/c-network-agent-front/certs/cert.pem", 0o644)
	_ = fs.Chmod(installDir+"/c-network-agent-front/certs/key.pem", 0o600)
	// .env mode 600.
	_ = fs.Chmod(envPath, 0o600)

	// All services running.
	allRunning := []dockerx.ContainerState{
		{Service: "agent", Running: true},
		{Service: "frontend", Running: true},
		{Service: "mongodb", Running: true},
		{Service: "influxdb", Running: true},
		{Service: "redis", Running: true},
	}

	// Runner call order from Run():
	//   1. detect.Compose → runner.Run("docker", "compose", "version")    → OK
	//   2. detect.Compose → runner.Run("docker-compose", "--version")       → not found
	//   3. detect.Permissions → runner.Run("docker", "info")               → OK (socket accessible)
	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil}, // compose version (v2 check)
			{Out: nil, Err: errors.New("not found")},                  // docker-compose v1 check
			{Out: []byte("Docker info output"), Err: nil},             // docker info for Permissions
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
	}

	prober := &dockerx.FakeHTTPProber{
		// Default: always 200 OK for connectivity checks.
	}

	return Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}, fs
}

// ─── Test: all-OK scenario ────────────────────────────────────────────────────

// TestDoctorAllOK verifies report shape when every check passes (7.6).
func TestDoctorAllOK(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"
	deps, _ := allOKDeps(installDir)

	diskProvider := detect.DiskSpaceProvider(func(path string) (uint64, error) {
		return 5000, nil
	})
	opts := DoctorOptions{
		InstallDir:        installDir,
		DiskSpaceProvider: diskProvider,
	}

	report := Run(ctx, deps, opts)

	// Every check must be OK.
	for _, c := range report.Checks {
		if c.Status != StatusOK {
			t.Errorf("check %q: expected OK, got %s (detail: %s; fix: %s)",
				c.ID, c.Status, c.Detail, c.FixSuggestion)
		}
	}

	// Summary must be OK.
	if report.Summary != StatusOK {
		t.Errorf("summary: expected OK, got %s", report.Summary)
	}

	// All expected check IDs must be present.
	expectedIDs := []string{
		"docker-installed",
		"docker-daemon",
		"compose-available",
		"connectivity-docker-hub",
		"connectivity-crenein",
		"disk-space",
		"env-permission",
		"compose-readable",
		"cert-key-modes",
		"docker-socket",
		"stack-services",
		"service-logs",
	}
	for _, id := range expectedIDs {
		findCheck(t, report, id)
	}
}

// ─── Test: mixed WARNING/CRITICAL scenario ────────────────────────────────────

// TestDoctorMixedResults verifies that summary equals worst status and that
// every non-OK check has a non-empty fix suggestion (7.6).
func TestDoctorMixedResults(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	composeContent := []byte("image: crenein/c-network-agent-back:latest\n")
	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath: composeContent,
		envPath:     []byte("INFLUXDB_TOKEN=abc\n"),
	})
	// .env with mode 644 (world-readable) → WARNING.
	_ = fs.Chmod(envPath, 0o644)

	// Agent service is down → CRITICAL.
	partialRunning := []dockerx.ContainerState{
		{Service: "agent", Running: false},
		{Service: "frontend", Running: true},
		{Service: "mongodb", Running: true},
		{Service: "influxdb", Running: true},
		{Service: "redis", Running: true},
	}

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose v2.21.0"), Err: nil}, // compose version (v2)
			{Out: nil, Err: errors.New("not found")},          // docker-compose v1
			{Out: []byte("Docker info"), Err: nil},            // docker info for Permissions
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: partialRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
	}

	prober := &dockerx.FakeHTTPProber{} // default 200 OK

	diskProvider := detect.DiskSpaceProvider(func(path string) (uint64, error) {
		return 5000, nil
	})

	deps := Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := DoctorOptions{
		InstallDir:        installDir,
		DiskSpaceProvider: diskProvider,
	}

	report := Run(ctx, deps, opts)

	// Summary must be CRITICAL (worst of WARNING + CRITICAL).
	if report.Summary != StatusCritical {
		t.Errorf("summary: expected CRITICAL, got %s", report.Summary)
	}

	// .env check should be WARNING with a non-empty fix.
	envCheck := findCheck(t, report, "env-permission")
	if envCheck.Status != StatusWarning {
		t.Errorf("env-permission: expected WARNING, got %s", envCheck.Status)
	}
	if envCheck.FixSuggestion == "" {
		t.Errorf("env-permission: expected non-empty fix suggestion")
	}
	if !strings.Contains(envCheck.FixSuggestion, "chmod 600") {
		t.Errorf("env-permission fix should mention 'chmod 600', got: %s", envCheck.FixSuggestion)
	}

	// stack-services check should be CRITICAL with a non-empty fix.
	stackCheck := findCheck(t, report, "stack-services")
	if stackCheck.Status != StatusCritical {
		t.Errorf("stack-services: expected CRITICAL, got %s", stackCheck.Status)
	}
	if stackCheck.FixSuggestion == "" {
		t.Errorf("stack-services: expected non-empty fix suggestion")
	}
	if !strings.Contains(stackCheck.Detail, "agent") {
		t.Errorf("stack-services detail should name 'agent', got: %s", stackCheck.Detail)
	}

	// Every non-OK check MUST have a fix suggestion.
	for _, c := range report.Checks {
		if c.Status != StatusOK && c.FixSuggestion == "" {
			t.Errorf("check %q: status %s but FixSuggestion is empty", c.ID, c.Status)
		}
	}
}

// ─── Test: no installation found ─────────────────────────────────────────────

// TestDoctorNoInstallation verifies that when no docker-compose.yml is found,
// stack/log checks report WARNING (not panic), and host-level checks still run.
// (7.6, spec: "no installation found scenario")
func TestDoctorNoInstallation(t *testing.T) {
	ctx := context.Background()

	// Empty FS: no compose file, no .env, no /home entries.
	fs := dockerx.NewFakeFS(nil)

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil}, // compose version (v2)
			{Out: nil, Err: errors.New("not found")},                  // docker-compose v1
			{Out: []byte("Docker info"), Err: nil},                    // docker info for Permissions
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: nil, // No containers.
	}

	prober := &dockerx.FakeHTTPProber{}

	diskProvider := detect.DiskSpaceProvider(func(_ string) (uint64, error) {
		return 5000, nil
	})

	deps := Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := DoctorOptions{
		DiskSpaceProvider: diskProvider,
		// No InstallDir — will search and find nothing.
	}

	report := Run(ctx, deps, opts)

	// Host-level checks should still run and (with our stubs) pass.
	dockerCheck := findCheck(t, report, "docker-installed")
	if dockerCheck.Status != StatusOK {
		t.Errorf("docker-installed: expected OK even with no install, got %s", dockerCheck.Status)
	}

	daemonCheck := findCheck(t, report, "docker-daemon")
	if daemonCheck.Status != StatusOK {
		t.Errorf("docker-daemon: expected OK, got %s", daemonCheck.Status)
	}

	// Stack and log checks should be WARNING (not a crash).
	stackCheck := findCheck(t, report, "stack-services")
	if stackCheck.Status != StatusWarning {
		t.Errorf("stack-services with no install: expected WARNING, got %s (detail: %s)",
			stackCheck.Status, stackCheck.Detail)
	}
	if !strings.Contains(stackCheck.Detail, "no installation") {
		t.Errorf("stack-services detail should mention 'no installation', got: %s", stackCheck.Detail)
	}

	logCheck := findCheck(t, report, "service-logs")
	if logCheck.Status != StatusWarning {
		t.Errorf("service-logs with no install: expected WARNING, got %s", logCheck.Status)
	}

	// env-permission should be WARNING (no .env found).
	envCheck := findCheck(t, report, "env-permission")
	if envCheck.Status != StatusWarning {
		t.Errorf("env-permission with no install: expected WARNING, got %s", envCheck.Status)
	}

	// All checks must have run (no panic/abort).
	if len(report.Checks) == 0 {
		t.Fatal("no checks in report — doctor must have aborted")
	}
}

// ─── Test: log error detection ────────────────────────────────────────────────

// TestDoctorLogErrorDetection verifies that error patterns in service logs
// produce a WARNING with count and sample (7.6).
func TestDoctorLogErrorDetection(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	composeContent := []byte("image: crenein/c-network-agent-back:latest\n")
	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath: composeContent,
		envPath:     []byte("INFLUXDB_TOKEN=abc\n"),
	})
	_ = fs.Chmod(envPath, 0o600)

	agentLogs := `2024-01-01T10:00:00 INFO starting agent
2024-01-01T10:00:01 ERROR database connection failed: timeout
2024-01-01T10:00:02 INFO retrying...
2024-01-01T10:00:03 FATAL cannot reach influxdb
2024-01-01T10:00:04 INFO agent running`

	allRunning := []dockerx.ContainerState{
		{Service: "agent", Running: true},
		{Service: "frontend", Running: true},
		{Service: "mongodb", Running: true},
		{Service: "influxdb", Running: true},
		{Service: "redis", Running: true},
	}

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil}, // compose version (v2)
			{Out: nil, Err: errors.New("not found")},                  // docker-compose v1
			{Out: []byte("Docker info"), Err: nil},                    // docker info for Permissions
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut: map[string][]byte{
			"agent":    []byte(agentLogs),
			"frontend": []byte(""), // no errors
		},
	}

	prober := &dockerx.FakeHTTPProber{}

	diskProvider := detect.DiskSpaceProvider(func(_ string) (uint64, error) {
		return 5000, nil
	})

	deps := Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := DoctorOptions{
		InstallDir:        installDir,
		DiskSpaceProvider: diskProvider,
	}

	report := Run(ctx, deps, opts)

	logCheck := findCheck(t, report, "service-logs")
	if logCheck.Status != StatusWarning {
		t.Errorf("service-logs: expected WARNING when errors found, got %s (detail: %s)",
			logCheck.Status, logCheck.Detail)
	}
	if logCheck.FixSuggestion == "" {
		t.Errorf("service-logs: expected non-empty fix suggestion")
	}
	if !strings.Contains(logCheck.Detail, "error") && !strings.Contains(logCheck.Detail, "2") {
		t.Errorf("service-logs detail should mention error count; got: %s", logCheck.Detail)
	}
	// Detail should include a sample of the matched lines.
	if !strings.Contains(logCheck.Detail, "[agent]") {
		t.Errorf("service-logs detail should include service label '[agent]'; got: %s", logCheck.Detail)
	}
}

// ─── Test: Status severity ordering ──────────────────────────────────────────

func TestStatusSeverity(t *testing.T) {
	if StatusOK.severity() >= StatusWarning.severity() {
		t.Error("OK should be less severe than WARNING")
	}
	if StatusWarning.severity() >= StatusCritical.severity() {
		t.Error("WARNING should be less severe than CRITICAL")
	}
	if StatusCritical.worse(StatusOK) != StatusCritical {
		t.Error("CRITICAL.worse(OK) should return CRITICAL")
	}
	if StatusOK.worse(StatusWarning) != StatusWarning {
		t.Error("OK.worse(WARNING) should return WARNING")
	}
	if StatusWarning.worse(StatusCritical) != StatusCritical {
		t.Error("WARNING.worse(CRITICAL) should return CRITICAL")
	}
}

// ─── Test: connectivity failure ───────────────────────────────────────────────

// TestDoctorConnectivityFailure verifies that a connectivity failure for one
// endpoint does not affect others (spec: independent checks).
func TestDoctorConnectivityFailure(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:          []byte("image: crenein/c-network-agent-back:latest\n"),
		installDir + "/.env": []byte("TOKEN=x\n"),
	})
	_ = fs.Chmod(installDir+"/.env", 0o600)

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2"), Err: nil}, // compose version (v2)
			{Out: nil, Err: errors.New("not found")},             // docker-compose v1
			{Out: []byte("ok"), Err: nil},                        // docker info for Permissions
		},
	}

	// Prober: first 2 requests (Docker Hub URLs) succeed; 3rd (core.crenein.com) fails.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// registry-1.docker.io → OK
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}},
			// hub.docker.com → OK
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}},
			// core.crenein.com → error
			{Resp: nil, Err: errors.New("connection refused")},
		},
	}

	client := &dockerx.FakeClient{
		PingErr: nil,
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
		ComposeLogsOut: map[string][]byte{"agent": {}, "frontend": {}},
	}

	diskProvider := detect.DiskSpaceProvider(func(_ string) (uint64, error) {
		return 5000, nil
	})

	deps := Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := DoctorOptions{
		InstallDir:        installDir,
		DiskSpaceProvider: diskProvider,
	}

	report := Run(ctx, deps, opts)

	// Docker Hub check must be OK (independent of crenein).
	hubCheck := findCheck(t, report, "connectivity-docker-hub")
	if hubCheck.Status != StatusOK {
		t.Errorf("connectivity-docker-hub: expected OK, got %s (detail: %s)",
			hubCheck.Status, hubCheck.Detail)
	}

	// Crenein check must be CRITICAL.
	creneinCheck := findCheck(t, report, "connectivity-crenein")
	if creneinCheck.Status != StatusCritical {
		t.Errorf("connectivity-crenein: expected CRITICAL, got %s", creneinCheck.Status)
	}
	if !strings.Contains(creneinCheck.FixSuggestion, "core.crenein.com") {
		t.Errorf("connectivity-crenein fix should mention 'core.crenein.com', got: %s",
			creneinCheck.FixSuggestion)
	}

	// Summary must reflect the failure.
	if report.Summary != StatusCritical {
		t.Errorf("summary: expected CRITICAL, got %s", report.Summary)
	}
}

// ─── Test: disk space failure ─────────────────────────────────────────────────

func TestDoctorLowDiskSpace(t *testing.T) {
	ctx := context.Background()

	fs := dockerx.NewFakeFS(nil)

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2"), Err: nil}, // compose version (v2)
			{Out: nil, Err: errors.New("not found")},             // docker-compose v1
			{Out: []byte("ok"), Err: nil},                        // docker info for Permissions
		},
	}

	client := &dockerx.FakeClient{PingErr: nil}
	prober := &dockerx.FakeHTTPProber{}

	// Only 512 MB free — below the 2048 MB minimum.
	diskProvider := detect.DiskSpaceProvider(func(_ string) (uint64, error) {
		return 512, nil
	})

	deps := Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := DoctorOptions{
		DiskSpaceProvider: diskProvider,
	}

	report := Run(ctx, deps, opts)

	diskCheck := findCheck(t, report, "disk-space")
	if diskCheck.Status != StatusCritical {
		t.Errorf("disk-space: expected CRITICAL for 512 MB free, got %s (detail: %s)",
			diskCheck.Status, diskCheck.Detail)
	}
	if !strings.Contains(diskCheck.FixSuggestion, "docker image prune") {
		t.Errorf("disk-space fix should mention 'docker image prune', got: %s",
			diskCheck.FixSuggestion)
	}
}
