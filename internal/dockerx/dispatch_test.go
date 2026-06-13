package dockerx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// dockerAvailable reports whether the docker binary is in PATH. Tests that
// require a real daemon are skipped when it is absent.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// TestComposeVariant_String verifies the String() representation of each variant.
func TestComposeVariant_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		variant dockerx.ComposeVariant
		want    string
	}{
		{dockerx.ComposeV2, "docker compose"},
		{dockerx.ComposeV1, "docker-compose"},
		{dockerx.ComposeNone, "none"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.variant.String(); got != tc.want {
				t.Errorf("ComposeVariant.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFakeClient_RecordsInvocations verifies that FakeClient captures every
// call and its arguments in order.
func TestFakeClient_RecordsInvocations(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	ctx := context.Background()

	_ = fc.Ping(ctx)
	_ = fc.ComposeUp(ctx, "docker-compose.yml", dockerx.ComposeUpOptions{
		Detach:        true,
		NoDeps:        true,
		ForceRecreate: true,
		Services:      []string{"agent", "frontend"},
	})
	_, _ = fc.ComposePs(ctx, "docker-compose.yml", []string{"agent"})
	_ = fc.ComposePull(ctx, "docker-compose.yml", []string{"agent"})
	_, _ = fc.ComposeExec(ctx, "docker-compose.yml", dockerx.ExecOptions{
		Service: "influxdb",
		Cmd:     []string{"influx", "bucket", "list"},
	})
	_, _ = fc.ImageInspect(ctx, "crenein/c-network-agent-back:1.8.4")
	_ = fc.ImageTag(ctx, "sha256:abc", "crenein/c-network-agent-back:latest")
	_ = fc.ImagePrune(ctx)
	_, _ = fc.ContainerList(ctx, "agent")

	wantMethods := []string{
		"Ping",
		"ComposeUp",
		"ComposePs",
		"ComposePull",
		"ComposeExec",
		"ImageInspect",
		"ImageTag",
		"ImagePrune",
		"ContainerList",
	}
	if len(fc.Calls) != len(wantMethods) {
		t.Fatalf("expected %d calls, got %d: %v", len(wantMethods), len(fc.Calls), fc.Calls)
	}
	for i, want := range wantMethods {
		if fc.Calls[i].Method != want {
			t.Errorf("call[%d].Method = %q, want %q", i, fc.Calls[i].Method, want)
		}
	}
}

// TestFakeClient_ComposeUp_V2_ArgsContainNoDepsForceRecreate verifies the
// FakeClient captures --no-deps and --force-recreate in ComposeUp call args.
func TestFakeClient_ComposeUp_V2_ArgsContainNoDepsForceRecreate(t *testing.T) {
	t.Parallel()
	fc := &dockerx.FakeClient{}
	ctx := context.Background()

	opts := dockerx.ComposeUpOptions{
		NoDeps:        true,
		ForceRecreate: true,
		Detach:        true,
		Services:      []string{"agent", "frontend"},
	}
	_ = fc.ComposeUp(ctx, "docker-compose.yml", opts)

	if len(fc.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fc.Calls))
	}
	call := fc.Calls[0]
	if call.Method != "ComposeUp" {
		t.Errorf("method = %q, want ComposeUp", call.Method)
	}
	// The fake records opts as a formatted string; verify key flags present.
	found := false
	for _, arg := range call.Args {
		if containsStr(arg, "NoDeps=true") && containsStr(arg, "ForceRecreate=true") {
			found = true
		}
	}
	if !found {
		t.Errorf("ComposeUp call args do not record NoDeps+ForceRecreate: %v", call.Args)
	}
}

// TestFakeCommandRunner_RecordsInvocations verifies FakeCommandRunner FIFO
// response consumption and invocation recording.
func TestFakeCommandRunner_RecordsInvocations(t *testing.T) {
	t.Parallel()
	runner := &dockerx.FakeCommandRunner{
		Responses: []dockerx.CmdResponse{
			{Out: []byte("Docker Compose version v2.24.0")},
			{Out: []byte("ok")},
		},
	}
	ctx := context.Background()

	out1, err1 := runner.Run(ctx, "docker", "compose", "version")
	out2, err2 := runner.Run(ctx, "apt-get", "update")
	out3, err3 := runner.Run(ctx, "systemctl", "start", "docker")

	if err1 != nil || string(out1) != "Docker Compose version v2.24.0" {
		t.Errorf("first call: out=%q err=%v", out1, err1)
	}
	if err2 != nil || string(out2) != "ok" {
		t.Errorf("second call: out=%q err=%v", out2, err2)
	}
	// Exhausted responses → zero value (empty out, nil error).
	if err3 != nil {
		t.Errorf("third call (exhausted): err=%v", err3)
	}
	if string(out3) != "" {
		t.Errorf("third call (exhausted): expected empty output, got %q", out3)
	}

	if len(runner.Invocations) != 3 {
		t.Fatalf("expected 3 invocations, got %d", len(runner.Invocations))
	}
	if runner.Invocations[0].Name != "docker" {
		t.Errorf("invocation[0].Name = %q, want docker", runner.Invocations[0].Name)
	}
	if runner.Invocations[1].Name != "apt-get" {
		t.Errorf("invocation[1].Name = %q, want apt-get", runner.Invocations[1].Name)
	}
}

// TestFakeFS_ReadWriteRoundtrip verifies in-memory filesystem behaviour.
func TestFakeFS_ReadWriteRoundtrip(t *testing.T) {
	t.Parallel()
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/proc/cpuinfo": []byte("flags: avx sse4"),
	})

	data, err := fs.ReadFile("/proc/cpuinfo")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "flags: avx sse4" {
		t.Errorf("ReadFile content = %q", data)
	}

	err = fs.WriteFile("/etc/vsftpd.conf", []byte("listen_ipv6=YES"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	data2, err := fs.ReadFile("/etc/vsftpd.conf")
	if err != nil {
		t.Fatalf("ReadFile after write: %v", err)
	}
	if string(data2) != "listen_ipv6=YES" {
		t.Errorf("content after write = %q", data2)
	}

	if fs.Modes["/etc/vsftpd.conf"] != 0o600 {
		t.Errorf("mode = %o, want %o", fs.Modes["/etc/vsftpd.conf"], 0o600)
	}

	if len(fs.Writes) != 1 || fs.Writes[0].Name != "/etc/vsftpd.conf" {
		t.Errorf("Writes = %v", fs.Writes)
	}
}

// TestFakeHTTPProber_RecordsRequests verifies the fake prober captures calls
// and consumes responses FIFO.
func TestFakeHTTPProber_RecordsRequests(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}},
			{Resp: &http.Response{StatusCode: 401, Body: http.NoBody, Header: make(http.Header)}},
		},
	}

	req1, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v2/", nil)

	resp1, err1 := prober.Do(req1)
	resp2, err2 := prober.Do(req2)

	if err1 != nil || resp1.StatusCode != 200 {
		t.Errorf("first: status=%d err=%v", resp1.StatusCode, err1)
	}
	if err2 != nil || resp2.StatusCode != 401 {
		t.Errorf("second: status=%d err=%v", resp2.StatusCode, err2)
	}

	if len(prober.Requests) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(prober.Requests))
	}
}

// containsStr is a simple string containment helper.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
