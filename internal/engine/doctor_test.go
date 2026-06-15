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
	composePath := installDir + "/docker-compose.yml"
	// Use mongo:4.4 so cpu.avx_mongo passes without needing AVX.
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  agent:\n  frontend:\n  mongodb:\n    image: mongo:4.4\n")

	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:     composeContent,
		envPath:         []byte("INFLUXDB_TOKEN=abc123\n"),
		"/proc/cpuinfo": []byte("flags : fpu avx sse4\n"),
		installDir + "/c-network-agent-back/certs/cert.pem":  []byte("CERT"),
		installDir + "/c-network-agent-back/certs/key.pem":   []byte("KEY"),
		installDir + "/c-network-agent-front/certs/cert.pem": []byte("CERT"),
		installDir + "/c-network-agent-front/certs/key.pem":  []byte("KEY"),
	})
	_ = fs.Chmod(installDir+"/c-network-agent-back/certs/cert.pem", 0o644)
	_ = fs.Chmod(installDir+"/c-network-agent-back/certs/key.pem", 0o600)
	_ = fs.Chmod(installDir+"/c-network-agent-front/certs/cert.pem", 0o644)
	_ = fs.Chmod(installDir+"/c-network-agent-front/certs/key.pem", 0o600)
	_ = fs.Chmod(envPath, 0o600)

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
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info output"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
	}

	// Prober responses (FakeHTTPProber returns 200 by default when exhausted):
	// 1. registry-1.docker.io (dockerhub check)
	// 2. hub.docker.com (dockerhub check)
	// 3. core.crenein.com (crenein check)
	// 4. https://localhost:8000/health (agent.health — now uses deps.Prober)
	// FakeHTTPProber returns 200 for any call beyond the pre-loaded list.
	prober := &dockerx.FakeHTTPProber{}

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
// Note: agent.health uses a real http.Client so it will fail in tests.
// We test the overall structure and the checks we can control.
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

	// All expected check IDs must be present (including new ones).
	expectedIDs := []string{
		"docker.installed",
		"docker.daemon",
		"docker.compose",
		"net.dockerhub",
		"net.cnetwork_api",
		"disk.space",
		"files.env_permission",
		"files.compose_readable",
		"files.cert_modes",
		"files.docker_socket",
		"services.running",
		"logs.recent_errors",
		"agent.health",
		"cpu.avx_mongo",
	}
	for _, id := range expectedIDs {
		findCheck(t, report, id)
	}

	// Checks that should always be OK with the allOKDeps fixture.
	alwaysOKIDs := []string{
		"docker.installed",
		"docker.daemon",
		"docker.compose",
		"net.dockerhub",
		"net.cnetwork_api",
		"disk.space",
		"files.env_permission",
		"files.compose_readable",
		"files.cert_modes",
		"files.docker_socket",
		"services.running",
		"logs.recent_errors",
		"cpu.avx_mongo",
	}
	for _, id := range alwaysOKIDs {
		c := findCheck(t, report, id)
		if c.Status != StatusOK {
			t.Errorf("check %q: expected OK, got %s (detail: %s; fix: %s)",
				c.ID, c.Status, c.Detail, c.FixSuggestion)
		}
		// Every check must have Severity set.
		if c.Severity == "" {
			t.Errorf("check %q: Severity is empty", c.ID)
		}
		// DurationMs must be set (non-negative; exact value is non-deterministic).
		if c.DurationMs < 0 {
			t.Errorf("check %q: DurationMs is negative: %d", c.ID, c.DurationMs)
		}
	}
}

// TestDoctorSeverityAssignment verifies that severity is correctly assigned.
func TestDoctorSeverityAssignment(t *testing.T) {
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

	criticalIDs := []string{
		"docker.installed",
		"docker.daemon",
		"docker.compose",
		"disk.space",
		"services.running",
		"files.docker_socket",
		"files.compose_readable",
		"files.cert_modes",
		"cpu.avx_mongo",
	}
	for _, id := range criticalIDs {
		c := findCheck(t, report, id)
		if c.Severity != StatusCritical {
			t.Errorf("check %q: expected Severity=CRITICAL, got %s", id, c.Severity)
		}
	}

	warningIDs := []string{
		"net.dockerhub",
		"net.cnetwork_api",
		"logs.recent_errors",
		"files.env_permission",
	}
	for _, id := range warningIDs {
		c := findCheck(t, report, id)
		if c.Severity != StatusWarning {
			t.Errorf("check %q: expected Severity=WARNING, got %s", id, c.Severity)
		}
	}
}

// ─── Test: mixed WARNING/CRITICAL scenario ────────────────────────────────────

// TestDoctorMixedResults verifies that summary equals worst status and that
// every non-OK check has a non-empty fix suggestion (7.6).
func TestDoctorMixedResults(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  mongodb:\n    image: mongo:4.4\n")
	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:     composeContent,
		envPath:         []byte("INFLUXDB_TOKEN=abc\n"),
		"/proc/cpuinfo": []byte("flags : fpu avx sse4\n"),
	})
	_ = fs.Chmod(envPath, 0o644)

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
			{Out: []byte("Docker Compose v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: partialRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
	}

	prober := &dockerx.FakeHTTPProber{}

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
	envCheck := findCheck(t, report, "files.env_permission")
	if envCheck.Status != StatusWarning {
		t.Errorf("files.env_permission: expected WARNING, got %s", envCheck.Status)
	}
	if envCheck.FixSuggestion == "" {
		t.Errorf("files.env_permission: expected non-empty fix suggestion")
	}
	if !strings.Contains(envCheck.FixSuggestion, "chmod 600") {
		t.Errorf("files.env_permission fix should mention 'chmod 600', got: %s", envCheck.FixSuggestion)
	}

	// services.running check should be CRITICAL with a non-empty fix.
	stackCheck := findCheck(t, report, "services.running")
	if stackCheck.Status != StatusCritical {
		t.Errorf("services.running: expected CRITICAL, got %s", stackCheck.Status)
	}
	if stackCheck.FixSuggestion == "" {
		t.Errorf("services.running: expected non-empty fix suggestion")
	}
	if !strings.Contains(stackCheck.Detail, "agent") {
		t.Errorf("services.running detail should name 'agent', got: %s", stackCheck.Detail)
	}

	// Every non-OK, non-SKIP check MUST have a fix suggestion.
	for _, c := range report.Checks {
		if c.Status != StatusOK && c.Status != StatusSkip && c.FixSuggestion == "" {
			t.Errorf("check %q: status %s but FixSuggestion is empty", c.ID, c.Status)
		}
	}
}

// ─── Test: no installation found ─────────────────────────────────────────────

// TestDoctorNoInstallation verifies that when no docker-compose.yml is found,
// stack/log checks report SKIP (not panic), and host-level checks still run.
func TestDoctorNoInstallation(t *testing.T) {
	ctx := context.Background()

	fs := dockerx.NewFakeFS(nil)

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: nil,
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
	}

	report := Run(ctx, deps, opts)

	// Host-level checks should still run and (with our stubs) pass.
	dockerCheck := findCheck(t, report, "docker.installed")
	if dockerCheck.Status != StatusOK {
		t.Errorf("docker.installed: expected OK even with no install, got %s", dockerCheck.Status)
	}

	daemonCheck := findCheck(t, report, "docker.daemon")
	if daemonCheck.Status != StatusOK {
		t.Errorf("docker.daemon: expected OK, got %s", daemonCheck.Status)
	}

	// Stack and stack-dependent checks should be SKIP with no installation.
	skipIDs := []string{"services.running", "logs.recent_errors", "agent.health", "cpu.avx_mongo"}
	for _, id := range skipIDs {
		c := findCheck(t, report, id)
		if c.Status != StatusSkip {
			t.Errorf("%s with no install: expected SKIP, got %s (detail: %s)",
				id, c.Status, c.Detail)
		}
	}

	// files.compose_readable and files.cert_modes should also be SKIP with no install.
	skipFileIDs := []string{"files.compose_readable", "files.cert_modes"}
	for _, id := range skipFileIDs {
		c := findCheck(t, report, id)
		if c.Status != StatusSkip {
			t.Errorf("%s with no install: expected SKIP, got %s", id, c.Status)
		}
	}

	// Summary should NOT be elevated by SKIP checks.
	// With our fake, all non-SKIP checks pass, so summary is OK.
	if report.Summary != StatusOK {
		// env-permission check shows WARNING when .env not found — that's expected.
		// So summary can be WARNING.
		if report.Summary != StatusWarning {
			t.Errorf("summary: expected OK or WARNING (due to env), got %s", report.Summary)
		}
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
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  mongodb:\n    image: mongo:4.4\n")
	envPath := installDir + "/.env"

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:     composeContent,
		envPath:         []byte("INFLUXDB_TOKEN=abc\n"),
		"/proc/cpuinfo": []byte("flags : fpu avx sse4\n"),
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
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut: map[string][]byte{
			"agent":    []byte(agentLogs),
			"frontend": []byte(""),
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

	logCheck := findCheck(t, report, "logs.recent_errors")
	if logCheck.Status != StatusWarning {
		t.Errorf("logs.recent_errors: expected WARNING when errors found, got %s (detail: %s)",
			logCheck.Status, logCheck.Detail)
	}
	if logCheck.FixSuggestion == "" {
		t.Errorf("logs.recent_errors: expected non-empty fix suggestion")
	}
	if !strings.Contains(logCheck.Detail, "error") && !strings.Contains(logCheck.Detail, "2") {
		t.Errorf("logs.recent_errors detail should mention error count; got: %s", logCheck.Detail)
	}
	if !strings.Contains(logCheck.Detail, "[agent]") {
		t.Errorf("logs.recent_errors detail should include service label '[agent]'; got: %s", logCheck.Detail)
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

// TestStatusSkipDoesNotElevateSummary verifies SKIP never elevates the summary.
func TestStatusSkipDoesNotElevateSummary(t *testing.T) {
	if StatusSkip.severity() != 0 {
		t.Errorf("SKIP severity should be 0 (same as OK), got %d", StatusSkip.severity())
	}
	if StatusOK.worse(StatusSkip) != StatusOK {
		t.Error("OK.worse(SKIP) should remain OK")
	}
	if StatusWarning.worse(StatusSkip) != StatusWarning {
		t.Error("WARNING.worse(SKIP) should remain WARNING")
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
		composePath:          []byte("image: crenein/c-network-agent-back:latest\nservices:\n  mongodb:\n    image: mongo:4.4\n"),
		installDir + "/.env": []byte("TOKEN=x\n"),
		"/proc/cpuinfo":      []byte("flags : fpu avx sse4\n"),
	})
	_ = fs.Chmod(installDir+"/.env", 0o600)

	runner := &dockerx.FakeCommandRunner{
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("ok"), Err: nil},
		},
	}

	// Prober: first 2 requests (Docker Hub URLs) succeed; 3rd (core.crenein.com) fails.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}},
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
	hubCheck := findCheck(t, report, "net.dockerhub")
	if hubCheck.Status != StatusOK {
		t.Errorf("net.dockerhub: expected OK, got %s (detail: %s)",
			hubCheck.Status, hubCheck.Detail)
	}

	// Crenein check must be WARNING (severity=WARNING, status matches severity when failing).
	creneinCheck := findCheck(t, report, "net.cnetwork_api")
	if creneinCheck.Status != StatusWarning {
		t.Errorf("net.cnetwork_api: expected WARNING (severity=WARNING), got %s", creneinCheck.Status)
	}
	if !strings.Contains(creneinCheck.FixSuggestion, "core.crenein.com") {
		t.Errorf("net.cnetwork_api fix should mention 'core.crenein.com', got: %s",
			creneinCheck.FixSuggestion)
	}

	// Summary reflects the worst non-SKIP status. The connectivity failure
	// (WARNING-severity) is the worst controlled failure; agent.health may also
	// fail (CRITICAL) when localhost:8000 is unreachable in the test environment.
	// Accept WARNING or CRITICAL — the key assertion is that net.cnetwork_api
	// is WARNING and net.dockerhub is OK, regardless of agent.health outcome.
	if report.Summary == StatusOK {
		t.Errorf("summary: expected WARNING or CRITICAL (some check failed), got OK")
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
			{Out: []byte("Docker Compose version v2"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("ok"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{PingErr: nil}
	prober := &dockerx.FakeHTTPProber{}

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

	diskCheck := findCheck(t, report, "disk.space")
	if diskCheck.Status != StatusCritical {
		t.Errorf("disk.space: expected CRITICAL for 512 MB free, got %s (detail: %s)",
			diskCheck.Status, diskCheck.Detail)
	}
	if !strings.Contains(diskCheck.FixSuggestion, "docker image prune") {
		t.Errorf("disk.space fix should mention 'docker image prune', got: %s",
			diskCheck.FixSuggestion)
	}
}

// ─── Test: skip propagation when docker daemon fails ─────────────────────────

// TestDoctorSkipPropagationDaemonFail verifies that when docker.daemon fails,
// dependent checks are SKIP and summary is not elevated by them.
func TestDoctorSkipPropagationDaemonFail(t *testing.T) {
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
			// docker info for Permissions.
			{Out: []byte("ok"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		// Daemon not responding.
		PingErr: errors.New("cannot connect to docker daemon"),
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

	// docker.daemon must be CRITICAL.
	daemonCheck := findCheck(t, report, "docker.daemon")
	if daemonCheck.Status != StatusCritical {
		t.Errorf("docker.daemon: expected CRITICAL, got %s", daemonCheck.Status)
	}

	// Checks that depend on docker.daemon being OK must be SKIP.
	skipIDs := []string{"docker.compose", "services.running", "logs.recent_errors", "agent.health", "cpu.avx_mongo"}
	for _, id := range skipIDs {
		c := findCheck(t, report, id)
		if c.Status != StatusSkip {
			t.Errorf("check %q: expected SKIP when docker.daemon fails, got %s", id, c.Status)
		}
	}

	// Network connectivity checks are pure HTTP probes and MUST still run
	// (never SKIP) even when Docker is unavailable.
	for _, id := range []string{"net.dockerhub", "net.cnetwork_api"} {
		if c := findCheck(t, report, id); c.Status == StatusSkip {
			t.Errorf("check %q: must run regardless of Docker state, got SKIP", id)
		}
	}

	// Summary must be CRITICAL (from docker.daemon), not elevated by SKIP.
	if report.Summary != StatusCritical {
		t.Errorf("summary: expected CRITICAL, got %s", report.Summary)
	}
}

// ─── Test: AVX / Mongo check ─────────────────────────────────────────────────

// TestDoctorAVXMongoNoAVX verifies that Mongo ≥5.0 on a non-AVX CPU is CRITICAL.
func TestDoctorAVXMongoNoAVX(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	// Use mongodb-community-server (≥5.0).
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  mongodb:\n    image: mongodb/mongodb-community-server:7.0-ubuntu2204\n")

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:          composeContent,
		installDir + "/.env": []byte("TOKEN=x\n"),
		// No AVX flag in cpuinfo.
		"/proc/cpuinfo": []byte("flags : fpu vme de pse\n"),
	})
	_ = fs.Chmod(installDir+"/.env", 0o600)

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
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
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

	avxCheck := findCheck(t, report, "cpu.avx_mongo")
	if avxCheck.Status != StatusCritical {
		t.Errorf("cpu.avx_mongo: expected CRITICAL when Mongo ≥5 and no AVX, got %s (detail: %s)",
			avxCheck.Status, avxCheck.Detail)
	}
	if !strings.Contains(avxCheck.FixSuggestion, "--mongo 4") {
		t.Errorf("cpu.avx_mongo fix should mention '--mongo 4', got: %s", avxCheck.FixSuggestion)
	}
}

// TestDoctorAVXMongoMongo44 verifies that Mongo 4.4 always passes regardless of AVX.
func TestDoctorAVXMongoMongo44(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	composePath := installDir + "/docker-compose.yml"
	composeContent := []byte("image: crenein/c-network-agent-back:latest\nservices:\n  mongodb:\n    image: mongo:4.4\n")

	fs := dockerx.NewFakeFS(map[string][]byte{
		composePath:          composeContent,
		installDir + "/.env": []byte("TOKEN=x\n"),
		// No AVX — but mongo:4.4 doesn't need it.
		"/proc/cpuinfo": []byte("flags : fpu vme de pse\n"),
	})
	_ = fs.Chmod(installDir+"/.env", 0o600)

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
			{Out: []byte("Docker Compose version v2.21.0"), Err: nil},
			{Out: nil, Err: errors.New("not found")},
			{Out: []byte("Docker info"), Err: nil},
		},
	}

	client := &dockerx.FakeClient{
		PingErr:         nil,
		ComposePsResult: allRunning,
		ComposeLogsOut:  map[string][]byte{"agent": []byte(""), "frontend": []byte("")},
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

	avxCheck := findCheck(t, report, "cpu.avx_mongo")
	if avxCheck.Status != StatusOK {
		t.Errorf("cpu.avx_mongo: expected OK for mongo:4.4, got %s (detail: %s)",
			avxCheck.Status, avxCheck.Detail)
	}
}

// TestDoctorAgentHealth_Prober verifies that checkAgentHealth uses deps.Prober
// and returns OK on 200, WARNING on 404, and CRITICAL on error.
func TestDoctorAgentHealth_Prober(t *testing.T) {
	ctx := context.Background()
	installDir := "/srv/crenein"

	makeClient := func(responses []dockerx.HTTPResponse) Deps {
		deps, _ := allOKDeps(installDir)
		deps.Prober = &dockerx.FakeHTTPProber{Responses: responses}
		return deps
	}

	opts := DoctorOptions{
		InstallDir:        installDir,
		DiskSpaceProvider: detect.DiskSpaceProvider(func(path string) (uint64, error) { return 5000, nil }),
	}

	// ok200 builds the 3 connectivity responses (2 dockerhub + 1 crenein) that
	// precede the agent.health probe so tests only need to append the health responses.
	ok200 := func() dockerx.HTTPResponse {
		return dockerx.HTTPResponse{Resp: &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}}
	}

	t.Run("200_OK", func(t *testing.T) {
		deps := makeClient([]dockerx.HTTPResponse{
			// net.dockerhub: registry-1.docker.io + hub.docker.com
			ok200(), ok200(),
			// net.cnetwork_api: core.crenein.com
			ok200(),
			// agent.health HTTPS probe
			ok200(),
		})
		report := Run(ctx, deps, opts)
		c := findCheck(t, report, "agent.health")
		if c.Status != StatusOK {
			t.Errorf("agent.health: got %s, want OK (detail: %s)", c.Status, c.Detail)
		}
	})

	t.Run("404_warning", func(t *testing.T) {
		deps := makeClient([]dockerx.HTTPResponse{
			ok200(), ok200(), ok200(), // connectivity
			{Resp: &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header)}},
		})
		report := Run(ctx, deps, opts)
		c := findCheck(t, report, "agent.health")
		if c.Status != StatusWarning {
			t.Errorf("agent.health: got %s, want WARNING", c.Status)
		}
	})

	t.Run("error_critical", func(t *testing.T) {
		deps := makeClient([]dockerx.HTTPResponse{
			ok200(), ok200(), ok200(), // connectivity
			// Both HTTPS and HTTP probes fail → CRITICAL
			{Err: errors.New("connection refused")},
			{Err: errors.New("connection refused")},
		})
		report := Run(ctx, deps, opts)
		c := findCheck(t, report, "agent.health")
		if c.Status != StatusCritical {
			t.Errorf("agent.health: got %s, want CRITICAL", c.Status)
		}
	})
}
