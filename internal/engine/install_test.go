package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// isHex64 reports whether s is a 64-char lowercase hex string — the shape of a
// freshly generated InfluxDB token. We assert the generated token's randomness
// and format rather than embedding the legacy hardcoded secret in the repo
// (AD-5): a fixed shared token would be identical across installs, which the
// uniqueness check below catches.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// makeBaseFS returns a FakeFS pre-populated with the minimum OS files needed
// by detect.Distro, detect.AVX, and detect.Permissions. Callers may add more.
func makeBaseFS(avx bool) *dockerx.FakeFS {
	cpuFlags := "flags\t: fpu vme de pse"
	if avx {
		cpuFlags = "flags\t: fpu vme de pse avx sse4_2"
	}
	return dockerx.NewFakeFS(map[string][]byte{
		"/etc/os-release": []byte("ID=ubuntu\nVERSION_ID=22.04\nPRETTY_NAME=\"Ubuntu 22.04\"\n"),
		"/proc/cpuinfo":   []byte(cpuFlags + "\n"),
	})
}

// makeRootRunner returns a FakeCommandRunner that queues the given responses
// in order. Tests set IsRootFunc on InstallOptions, so no "docker info"
// response needs to be pre-queued for the permissions check.
func makeRootRunner(responses ...dockerx.CmdResponse) *dockerx.FakeCommandRunner {
	r := &dockerx.FakeCommandRunner{
		Responses: responses,
		LookPathFunc: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
	}
	return r
}

// isRootTrue is an IsRootFunc that always returns true for use in tests.
func isRootTrue() bool { return true }

// rootDiskProvider returns a DiskSpaceProvider that reports enough disk space.
func rootDiskProvider() detect.DiskSpaceProvider {
	return func(string) (uint64, error) { return 10240, nil }
}

// allReachableProber returns a FakeHTTPProber that returns 200 for all requests.
func allReachableProber() *dockerx.FakeHTTPProber {
	return &dockerx.FakeHTTPProber{}
}

// influxHealthProber returns a prober that:
//   - Answers "status":"pass" for the health endpoint.
//   - Returns orgID for /api/v2/orgs.
//   - Returns success for admin register.
//   - Accepts bucket create.
func influxHealthProber(orgID string) *dockerx.FakeHTTPProber {
	return &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// 3× connectivity pre-flight (docker.io, hub.docker.com, crenein)
			okResp(), okResp(), okResp(),
			// vsftpd download
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("listen_ipv6=YES\n")), Header: make(http.Header)}},
			// tftpd download
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`TFTP_USERNAME="tftp"` + "\n")), Header: make(http.Header)}},
			// influx health
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
			// admin register
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"1"}`)), Header: make(http.Header)}},
			// orgs REST call
			orgResp(orgID),
			// bucket fping REST create
			bucketResp("fping"),
			// bucket devices REST create
			bucketResp("devices"),
		},
	}
}

func okResp() dockerx.HTTPResponse {
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: 200,
			Body:       http.NoBody,
			Header:     make(http.Header),
		},
	}
}

type orgsBody struct {
	Orgs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"orgs"`
}

func orgResp(orgID string) dockerx.HTTPResponse {
	body := orgsBody{}
	body.Orgs = []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}{{ID: orgID, Name: "crenein"}}
	b, _ := json.Marshal(body)
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(string(b))),
			Header:     make(http.Header),
		},
	}
}

func bucketResp(name string) dockerx.HTTPResponse {
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"name":%q}`, name))),
			Header:     make(http.Header),
		},
	}
}

// runningContainers returns a slice of ContainerState for all required services.
func runningContainers() []dockerx.ContainerState {
	svcList := []string{"mongodb", "influxdb", "redis", "agent", "frontend"}
	states := make([]dockerx.ContainerState, len(svcList))
	for i, svc := range svcList {
		states[i] = dockerx.ContainerState{
			Name:    "srv-" + svc + "-1",
			Service: svc,
			Status:  "Up 5 seconds",
			Running: true,
		}
	}
	return states
}

// ─── Test: AVX selects mongodb/mongodb-community-server:7.0 ──────────────────

func TestInstall_AVX_SelectsMongoImage(t *testing.T) {
	cases := []struct {
		name     string
		avx      bool
		wantImg  string
		wantWarn bool
	}{
		{
			name:    "AVX present selects Mongo7",
			avx:     true,
			wantImg: "mongodb/mongodb-community-server:7.0-ubuntu2204",
		},
		{
			name:     "no AVX selects Mongo4.4",
			avx:      false,
			wantImg:  "mongo:4.4",
			wantWarn: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := makeBaseFS(tc.avx)
			runner := makeRootRunner(
				// apt-get update
				dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
				// apt-get install packages
				dockerx.CmdResponse{Out: nil},
				// docker info (Docker detection: already installed)
				dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
				// id backups (exists)
				dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
				// openssl backend cert
				dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
				// openssl frontend cert
				dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
			)
			runner.LookPathFunc = func(name string) (string, error) {
				return "/usr/bin/" + name, nil
			}
			fakeClient := &dockerx.FakeClient{
				ComposePsResult: runningContainers(),
			}
			prober := influxHealthProber("abc123def456ab12")
			opts := InstallOptions{
				InstallDir:        t.TempDir(),
				DiskSpaceProvider: rootDiskProvider(),
				IsRootFunc:        isRootTrue,
				RetryInterval:     time.Millisecond,
			}
			deps := Deps{
				Client:   fakeClient,
				Runner:   runner,
				FS:       fs,
				Prober:   prober,
				Reporter: DiscardReporter{},
			}
			res, err := Install(context.Background(), deps, opts)
			if err != nil {
				t.Fatalf("Install failed: %v", err)
			}

			// Check compose file contains the expected mongo image.
			dir := opts.installDir()
			composeBytes, err := fs.ReadFile(dir + "/docker-compose.yml")
			if err != nil {
				t.Fatalf("docker-compose.yml not written: %v", err)
			}
			if !strings.Contains(string(composeBytes), tc.wantImg) {
				t.Errorf("compose does not contain %q;\ngot:\n%s", tc.wantImg, composeBytes)
			}

			// Mongo-4.4 path should emit a warning.
			if tc.wantWarn {
				found := false
				for _, w := range res.Warnings {
					if strings.Contains(w, "AVX") {
						found = true
					}
				}
				if !found {
					t.Errorf("expected AVX warning, got: %v", res.Warnings)
				}
			}
		})
	}
}

// ─── Test: MongoImageOverride takes precedence ────────────────────────────────

func TestInstall_MongoImageOverride(t *testing.T) {
	overrideImg := "mongo:6.0"
	fs := makeBaseFS(true) // AVX present, but override must win
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, // apt-get
		dockerx.CmdResponse{Out: nil},                            // apt-get install
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")}, // docker info
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},    // id backups
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")
	opts := InstallOptions{
		InstallDir:         t.TempDir(),
		MongoImageOverride: overrideImg,
		DiskSpaceProvider:  rootDiskProvider(),
		IsRootFunc:         isRootTrue,
		RetryInterval:      time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	dir := opts.installDir()
	composeBytes, _ := fs.ReadFile(dir + "/docker-compose.yml")
	if !strings.Contains(string(composeBytes), overrideImg) {
		t.Errorf("compose does not contain override %q", overrideImg)
	}
}

// ─── Test: .env idempotence — not overwritten if exists ──────────────────────

func TestInstall_EnvFile_NotOverwrittenIfExists(t *testing.T) {
	dir := t.TempDir()
	existingToken := "existingtoken1234567890abcdef01234567890abcdef0123456789abcdef01"
	existingEnv := fmt.Sprintf("INFLUXDB_TOKEN=%s\nMONGODB_INITDB_ROOT_PASSWORD=existing\nREDIS_PASSWORD=existing\n", existingToken)

	fs := makeBaseFS(true)
	// Pre-populate .env.
	_ = fs.WriteFile(dir+"/.env", []byte(existingEnv), 0o600)

	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")
	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// .env should still contain the existing token.
	envBytes, err := fs.ReadFile(dir + "/.env")
	if err != nil {
		t.Fatalf("cannot read .env: %v", err)
	}
	if !strings.Contains(string(envBytes), existingToken) {
		t.Errorf(".env was regenerated; existing token not found.\nGot:\n%s", envBytes)
	}
}

// ─── Test: .env permissions are 600 ──────────────────────────────────────────

func TestInstall_EnvFile_Mode600(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")
	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	mode := fs.Modes[dir+"/.env"]
	if mode != 0o600 {
		t.Errorf(".env mode = %04o, want 0600", mode)
	}
}

// ─── Test: generated token is random and differs from legacy ─────────────────

func TestInstall_InfluxToken_IsNotLegacy(t *testing.T) {
	// Verify the randomInfluxToken function produces unique, non-legacy values.
	token1, err := randomInfluxToken()
	if err != nil {
		t.Fatalf("randomInfluxToken: %v", err)
	}
	token2, err := randomInfluxToken()
	if err != nil {
		t.Fatalf("randomInfluxToken: %v", err)
	}

	if token1 == token2 {
		t.Errorf("two generated tokens are identical (randomness failure)")
	}
	if !isHex64(token1) {
		t.Errorf("token %q is not a 64-char hex string", token1)
	}
}

// ─── Test: .env contains all required variables ───────────────────────────────

func TestInstall_EnvFile_ContainsRequiredVars(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")
	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	envBytes, _ := fs.ReadFile(dir + "/.env")
	env := string(envBytes)

	requiredVars := []string{
		"INFLUXDB_TOKEN=",
		"DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=",
		"MONGODB_INITDB_ROOT_USERNAME=cnetwork_admin",
		"MONGODB_INITDB_ROOT_PASSWORD=",
		"REDIS_PASSWORD=",
		"CNETWORK_API_URL=http://localhost:8000",
		"CNETWORK_API_TOKEN=your-api-token-here",
	}
	for _, v := range requiredVars {
		if !strings.Contains(env, v) {
			t.Errorf(".env missing %q\nFull .env:\n%s", v, env)
		}
	}

	// Passwords must be 32 alphanumeric chars.
	mongoPwd := extractEnvVar(env, "MONGODB_INITDB_ROOT_PASSWORD")
	if len(mongoPwd) != 32 {
		t.Errorf("MongoDB password length = %d, want 32", len(mongoPwd))
	}
	for _, c := range mongoPwd {
		if !isAlphaNum(c) {
			t.Errorf("MongoDB password contains non-alphanumeric char %q", c)
		}
	}

	redisPwd := extractEnvVar(env, "REDIS_PASSWORD")
	if len(redisPwd) != 32 {
		t.Errorf("Redis password length = %d, want 32", len(redisPwd))
	}

	// The InfluxDB token in .env must be a freshly generated random hex token
	// (AD-5), never a fixed/short placeholder.
	influxToken := extractEnvVar(env, "INFLUXDB_TOKEN")
	if !isHex64(influxToken) {
		t.Errorf("INFLUXDB_TOKEN = %q, want a 64-char hex random token", influxToken)
	}
}

func isAlphaNum(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ─── Test: pre-flight blocks non-root ────────────────────────────────────────

func TestInstall_Preflight_BlocksNonRoot(t *testing.T) {
	fs := makeBaseFS(true)
	runner := makeRootRunner() // no responses needed — we abort before any runner call
	opts := InstallOptions{
		InstallDir:        t.TempDir(),
		DiskSpaceProvider: rootDiskProvider(),
		// IsRootFunc returns false — simulates a non-root caller.
		IsRootFunc: func() bool { return false },
	}
	deps := Deps{
		Client:   &dockerx.FakeClient{},
		Runner:   runner,
		FS:       fs,
		Prober:   allReachableProber(),
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err == nil {
		t.Fatal("expected error for non-root, got nil")
	}
	if !strings.Contains(err.Error(), "root") && !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error should mention root/sudo, got: %v", err)
	}
	// No file writes should have occurred.
	if len(fs.Writes) > 0 {
		t.Errorf("pre-flight failure should produce no writes; got %d write(s)", len(fs.Writes))
	}
}

// ─── Test: pre-flight blocks unsupported distro ───────────────────────────────

func TestInstall_Preflight_BlocksUnsupportedDistro(t *testing.T) {
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/etc/os-release": []byte("ID=fedora\nVERSION_ID=38\nPRETTY_NAME=\"Fedora 38\"\n"),
		"/proc/cpuinfo":   []byte("flags\t: fpu\n"),
	})
	runner := makeRootRunner()
	opts := InstallOptions{
		InstallDir:        t.TempDir(),
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   &dockerx.FakeClient{},
		Runner:   runner,
		FS:       fs,
		Prober:   allReachableProber(),
		Reporter: DiscardReporter{},
	}
	_, err := Install(context.Background(), deps, opts)
	if err == nil {
		t.Fatal("expected error for unsupported distro, got nil")
	}
	if len(fs.Writes) > 0 {
		t.Errorf("pre-flight failure should produce no writes; got %d write(s)", len(fs.Writes))
	}
}

// ─── Test: vsftpd/tftpd fallback to embedded defaults on download failure ────

func TestInstall_FTPConfig_FallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}

	// Prober: connectivity OK (3), then vsftpd download fails, tftpd download fails.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// connectivity
			okResp(), okResp(), okResp(),
			// vsftpd download — HTTP 500
			{Resp: &http.Response{StatusCode: 500, Body: http.NoBody, Header: make(http.Header)}},
			// tftpd download — network error
			{Err: fmt.Errorf("connection refused")},
			// influx health
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
			// admin register
			okResp(),
			// orgs
			orgResp("abc123def456ab12"),
			// bucket fping
			bucketResp("fping"),
			// bucket devices
			bucketResp("devices"),
		},
	}

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Both fallback warnings should appear.
	foundVsftpd, foundTftpd := false, false
	for _, w := range res.Warnings {
		if strings.Contains(w, "vsftpd") {
			foundVsftpd = true
		}
		if strings.Contains(w, "tftpd-hpa") {
			foundTftpd = true
		}
	}
	if !foundVsftpd {
		t.Error("expected vsftpd fallback warning")
	}
	if !foundTftpd {
		t.Error("expected tftpd-hpa fallback warning")
	}

	// /etc/vsftpd.conf should exist (embedded defaults written).
	if _, err := fs.ReadFile("/etc/vsftpd.conf"); err != nil {
		t.Errorf("/etc/vsftpd.conf not written: %v", err)
	}
	if _, err := fs.ReadFile("/etc/default/tftpd-hpa"); err != nil {
		t.Errorf("/etc/default/tftpd-hpa not written: %v", err)
	}
}

// ─── Test: InfluxDB bucket chain — method 4 (REST) ───────────────────────────

func TestInstall_InfluxBuckets_ViaRESTMethod(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// connectivity
			okResp(), okResp(), okResp(),
			// vsftpd + tftpd OK
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("listen_ipv6=YES\n")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`TFTP_USERNAME="tftp"` + "\n")), Header: make(http.Header)}},
			// influx health
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
			// admin register
			okResp(),
			// orgs endpoint — returns org ID
			orgResp("cafebabe12345678"),
			// bucket fping REST create
			bucketResp("fping"),
			// bucket devices REST create
			bucketResp("devices"),
		},
	}

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// No bucket-chain-failure warnings.
	for _, w := range res.Warnings {
		if strings.Contains(w, "bucket creation failed") {
			t.Errorf("unexpected bucket failure warning: %s", w)
		}
	}
}

// ─── Test: InfluxDB bucket chain — REST fails, CLI fallback ──────────────────

func TestInstall_InfluxBuckets_CLIFallback(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	// ComposeExec: org list returns valid ID, then bucket creates succeed.
	fakeClient := &dockerx.FakeClient{
		ComposePsResult: runningContainers(),
		// ComposeExec org list → valid output.
		ComposeExecOut: []byte("ID\t\t\t\t\tName\ncafebabe12345678\t\t\tcrenein\n"),
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// connectivity
			okResp(), okResp(), okResp(),
			// vsftpd + tftpd
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
			// influx health
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
			// admin register OK
			okResp(),
			// orgs REST — all 10 fail (return empty body)
			emptyResp(), emptyResp(), emptyResp(), emptyResp(), emptyResp(),
			emptyResp(), emptyResp(), emptyResp(), emptyResp(), emptyResp(),
			// bucket REST creates after CLI resolves org ID
			bucketResp("fping"),
			bucketResp("devices"),
		},
	}

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	// Should still succeed (no failure warnings for buckets specifically).
	for _, w := range res.Warnings {
		if strings.Contains(w, "bucket creation failed by all methods") {
			t.Errorf("unexpected all-methods-failed warning: %s", w)
		}
	}
}

func emptyResp() dockerx.HTTPResponse {
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("{}")),
			Header:     make(http.Header),
		},
	}
}

// ─── Test: entire bucket chain fails → warning + manual instructions ──────────

func TestInstall_InfluxBuckets_AllMethodsFail(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	// ComposeExec always fails.
	fakeClient := &dockerx.FakeClient{
		ComposePsResult: runningContainers(),
		ComposeExecErr:  fmt.Errorf("container exec failed"),
	}

	// Build a prober that returns all org REST failures and all bucket failures.
	responses := []dockerx.HTTPResponse{
		okResp(), okResp(), okResp(), // connectivity
		{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
		{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
		{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
		okResp(), // admin register
	}
	// 10 org REST failures.
	for i := 0; i < 10; i++ {
		responses = append(responses, emptyResp())
	}
	// 5 bucket fping failures + 5 bucket devices failures (method 4, but no orgID means method 4 skipped).
	// Actually with no orgID and CLI also failing, method 3 (CLI bucket create) is attempted.
	// ComposeExecErr is set so those fail too.
	// Method 4 needs orgID which is "" → skipped.
	// Result: warn + manual instructions.

	prober := &dockerx.FakeHTTPProber{Responses: responses}

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install must not abort on bucket failure, got: %v", err)
	}

	// Expect a warning with manual instructions.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "bucket creation failed") || strings.Contains(w, "Create manually") || strings.Contains(w, "influx bucket create") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bucket-failure warning with manual instructions; warnings: %v", res.Warnings)
	}
}

// ─── Test: admin registration retries + exhaustion ───────────────────────────

func TestInstall_AdminUser_ExhaustionContinues(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}

	// Admin register: 3 consecutive failures.
	adminFail := dockerx.HTTPResponse{
		Resp: &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("error")), Header: make(http.Header)},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okResp(), okResp(), okResp(), // connectivity
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"pass"}`)), Header: make(http.Header)}},
			adminFail, adminFail, adminFail, // 3 admin failures
			orgResp("abc123def456ab12"),
			bucketResp("fping"),
			bucketResp("devices"),
		},
	}

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	// Override retry sleep to zero in tests by using a short context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := Install(ctx, deps, opts)
	if err != nil {
		t.Fatalf("Install must not abort on admin failure, got: %v", err)
	}

	// Expect warning with manual registration command.
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "admin") || strings.Contains(w, "register") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected admin-failure warning; warnings: %v", res.Warnings)
	}
}

// ─── Test: access summary present on success ─────────────────────────────────

func TestInstall_AccessSummary_Present(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")

	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	wantLabels := []string{
		"Backend API (HTTPS)",
		"Frontend (HTTPS)",
		"Admin credentials",
		"InfluxDB",
		"Persistent data",
		"Backend certificates",
		".env location",
	}
	for _, label := range wantLabels {
		found := false
		for _, entry := range res.AccessSummary {
			if entry.Label == label {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AccessSummary missing entry %q", label)
		}
	}
}

// ─── Test: idempotence — re-run detects existing installation ─────────────────

func TestInstall_Idempotence_ReInstall(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)

	// Pre-populate a "previous" installation.
	existingToken := "prevtoken1234567890123456789012345678901234567890123456789012"
	_ = fs.WriteFile(dir+"/.env", []byte("INFLUXDB_TOKEN="+existingToken+"\nMONGODB_INITDB_ROOT_PASSWORD=prevpwd\nREDIS_PASSWORD=prevred\n"), 0o600)
	_ = fs.WriteFile(dir+"/docker-compose.yml", []byte("# crenein/c-network-agent-back:latest\n"), 0o644)
	// Non-empty /data/mongodb.
	_ = fs.WriteFile("/data/mongodb/WiredTiger", []byte("fake"), 0o644)

	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	fakeClient := &dockerx.FakeClient{ComposePsResult: runningContainers()}
	prober := influxHealthProber("abc123def456ab12")
	opts := InstallOptions{
		InstallDir:        dir,
		DiskSpaceProvider: rootDiskProvider(),
		IsRootFunc:        isRootTrue,
		RetryInterval:     time.Millisecond,
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	res, err := Install(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	if !res.ReinstallMode {
		t.Error("expected ReinstallMode=true")
	}

	// .env must be preserved.
	envBytes, _ := fs.ReadFile(dir + "/.env")
	if !strings.Contains(string(envBytes), existingToken) {
		t.Errorf(".env was overwritten; existing token not found")
	}

	// Reused components listed.
	if len(res.ReusedComponents) == 0 {
		t.Error("expected ReusedComponents to be non-empty")
	}
}

// ─── Test: service verification fails → structured error with fix ─────────────

func TestInstall_ServiceVerification_Failure(t *testing.T) {
	dir := t.TempDir()
	fs := makeBaseFS(true)
	runner := makeRootRunner(
		dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil}, dockerx.CmdResponse{Out: nil},
		dockerx.CmdResponse{Out: []byte("Server Version: 24.0")},
		dockerx.CmdResponse{Out: []byte("uid=1001(backups)")},
	)
	runner.LookPathFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	// ComposePs returns containers where "agent" is NOT running.
	partialContainers := runningContainers()
	for i, c := range partialContainers {
		if c.Service == "agent" {
			partialContainers[i].Running = false
			partialContainers[i].Status = "Exited (1)"
		}
	}

	fakeClient := &dockerx.FakeClient{
		ComposePsResult: partialContainers,
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okResp(), okResp(), okResp(), // connectivity
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}},
		},
	}
	opts := InstallOptions{
		InstallDir:           dir,
		DiskSpaceProvider:    rootDiskProvider(),
		IsRootFunc:           isRootTrue,
		RetryInterval:        time.Millisecond,
		ServiceVerifyTimeout: 200 * time.Millisecond, // short window — agent never becomes running
	}
	deps := Deps{
		Client:   fakeClient,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	_, err := Install(context.Background(), deps, opts)
	if err == nil {
		t.Fatal("expected error when service fails to start, got nil")
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("error should name the failing service; got: %v", err)
	}
	// Fix suggestion should mention docker compose logs.
	if !strings.Contains(err.Error(), "logs") {
		t.Errorf("error should suggest checking logs; got: %v", err)
	}
}

// ─── Test: parseOrgID from JSON ───────────────────────────────────────────────

func TestParseOrgID(t *testing.T) {
	body := []byte(`{"orgs":[{"id":"abc123","name":"crenein"}]}`)
	id := parseOrgID(body)
	if id != "abc123" {
		t.Errorf("parseOrgID = %q, want %q", id, "abc123")
	}

	// Empty body.
	if parseOrgID([]byte("{}")) != "" {
		t.Error("expected empty string for empty orgs")
	}
}

// ─── Test: parseOrgIDFromCLI ──────────────────────────────────────────────────

func TestParseOrgIDFromCLI(t *testing.T) {
	output := "ID\t\t\t\t\tName\ncafebabe12345678\t\t\tcrenein\n"
	id := parseOrgIDFromCLI(output)
	if id != "cafebabe12345678" {
		t.Errorf("parseOrgIDFromCLI = %q, want %q", id, "cafebabe12345678")
	}

	// No valid ID.
	if parseOrgIDFromCLI("no hex here\n") != "" {
		t.Error("expected empty string for no hex line")
	}
}

// ─── Test: randomAlphaNum length and charset ──────────────────────────────────

func TestRandomAlphaNum(t *testing.T) {
	s, err := randomAlphaNum(32)
	if err != nil {
		t.Fatalf("randomAlphaNum: %v", err)
	}
	if len(s) != 32 {
		t.Errorf("length = %d, want 32", len(s))
	}
	for _, c := range s {
		if !isAlphaNum(c) {
			t.Errorf("non-alphanumeric char %q in password", c)
		}
	}

	s2, _ := randomAlphaNum(32)
	if s == s2 {
		t.Error("two randomAlphaNum calls produced identical results (unlikely if random)")
	}
}
