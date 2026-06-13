package detect_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func ctx() context.Context { return context.Background() }

// responseBody builds an http.Response with the given status and body.
func responseBody(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// ─── AVX ─────────────────────────────────────────────────────────────────────

func TestAVX(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
		wantErr bool
	}{
		{
			name:    "avx present",
			content: "processor\t: 0\nflags\t\t: fpu vme avx sse4_2\n",
			want:    true,
		},
		{
			name:    "avx absent",
			content: "processor\t: 0\nflags\t\t: fpu vme sse sse2\n",
			want:    false,
		},
		{
			name:    "multiple processors only last has avx",
			content: "processor\t: 0\nflags\t\t: fpu\nprocessor\t: 1\nflags\t\t: fpu avx\n",
			want:    true,
		},
		{
			name:    "empty cpuinfo",
			content: "",
			want:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := dockerx.NewFakeFS(map[string][]byte{
				"/proc/cpuinfo": []byte(tc.content),
			})
			got, err := detect.AVX(ctx(), fs)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("AVX() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("cpuinfo unreadable returns structured error", func(t *testing.T) {
		t.Parallel()
		fs := dockerx.NewFakeFS(nil) // no /proc/cpuinfo
		_, err := detect.AVX(ctx(), fs)
		if err == nil {
			t.Fatal("expected error for missing cpuinfo")
		}
		var ce *cnerr.Error
		if !errors.As(err, &ce) {
			t.Fatalf("expected *cnerr.Error, got %T: %v", err, err)
		}
		if ce.FixSuggestion == "" {
			t.Error("expected non-empty FixSuggestion")
		}
	})
}

// ─── MongoImage ──────────────────────────────────────────────────────────────

func TestMongoImage(t *testing.T) {
	t.Parallel()
	if got := detect.MongoImage(true); got != "mongodb/mongodb-community-server:7.0-ubuntu2204" {
		t.Errorf("MongoImage(true) = %q", got)
	}
	if got := detect.MongoImage(false); got != "mongo:4.4" {
		t.Errorf("MongoImage(false) = %q", got)
	}
}

// ─── Distro ──────────────────────────────────────────────────────────────────

func TestDistro(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		content   string
		wantID    string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "ubuntu",
			content: `ID=ubuntu` + "\n" + `VERSION_ID="22.04"` + "\n" + `PRETTY_NAME="Ubuntu 22.04.3 LTS"`,
			wantID:  "ubuntu",
		},
		{
			name:    "debian",
			content: `ID=debian` + "\n" + `VERSION_ID="12"`,
			wantID:  "debian",
		},
		{
			name:      "fedora unsupported",
			content:   `ID=fedora`,
			wantErr:   true,
			errSubstr: "fedora",
		},
		{
			name:      "centos unsupported",
			content:   `ID=centos`,
			wantErr:   true,
			errSubstr: "unsupported",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := dockerx.NewFakeFS(map[string][]byte{
				"/etc/os-release": []byte(tc.content),
			})
			got, err := detect.Distro(ctx(), fs)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(strings.ToLower(err.Error()), tc.errSubstr) {
					t.Errorf("error %q does not contain %q", err, tc.errSubstr)
				}
				var ce *cnerr.Error
				if !errors.As(err, &ce) {
					t.Fatalf("expected *cnerr.Error, got %T", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tc.wantID {
				t.Errorf("Distro().ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}

	t.Run("missing os-release returns structured error", func(t *testing.T) {
		t.Parallel()
		fs := dockerx.NewFakeFS(nil)
		_, err := detect.Distro(ctx(), fs)
		if err == nil {
			t.Fatal("expected error")
		}
		var ce *cnerr.Error
		if !errors.As(err, &ce) {
			t.Fatalf("expected *cnerr.Error, got %T", err)
		}
		if ce.FixSuggestion == "" {
			t.Error("expected non-empty FixSuggestion")
		}
	})
}

// ─── Docker ──────────────────────────────────────────────────────────────────

func TestDocker(t *testing.T) {
	t.Parallel()

	t.Run("installed and running", func(t *testing.T) {
		t.Parallel()
		runner := &dockerx.FakeCommandRunner{
			LookPathFunc: func(name string) (string, error) { return "/usr/bin/docker", nil },
		}
		client := &dockerx.FakeClient{} // PingErr == nil → daemon running
		info := detect.Docker(ctx(), runner, client)
		if !info.Installed {
			t.Error("expected Installed=true")
		}
		if !info.DaemonRunning {
			t.Error("expected DaemonRunning=true")
		}
	})

	t.Run("not installed", func(t *testing.T) {
		t.Parallel()
		runner := &dockerx.FakeCommandRunner{
			LookPathFunc: func(name string) (string, error) {
				return "", errors.New("not found")
			},
		}
		client := &dockerx.FakeClient{}
		info := detect.Docker(ctx(), runner, client)
		if info.Installed {
			t.Error("expected Installed=false")
		}
		if info.DaemonRunning {
			t.Error("expected DaemonRunning=false")
		}
	})

	t.Run("daemon not running", func(t *testing.T) {
		t.Parallel()
		runner := &dockerx.FakeCommandRunner{
			LookPathFunc: func(name string) (string, error) { return "/usr/bin/docker", nil },
		}
		client := &dockerx.FakeClient{PingErr: errors.New("daemon not running")}
		info := detect.Docker(ctx(), runner, client)
		if !info.Installed {
			t.Error("expected Installed=true")
		}
		if info.DaemonRunning {
			t.Error("expected DaemonRunning=false")
		}
	})

	t.Run("socket permission denied", func(t *testing.T) {
		t.Parallel()
		runner := &dockerx.FakeCommandRunner{
			LookPathFunc: func(name string) (string, error) { return "/usr/bin/docker", nil },
		}
		client := &dockerx.FakeClient{PingErr: errors.New("permission denied: /var/run/docker.sock")}
		info := detect.Docker(ctx(), runner, client)
		if !info.Installed {
			t.Error("expected Installed=true")
		}
		if info.DaemonRunning {
			t.Error("expected DaemonRunning=false when permission denied")
		}
		if info.SocketPermission {
			t.Error("expected SocketPermission=false")
		}
	})
}

// ─── Compose ─────────────────────────────────────────────────────────────────

func TestCompose(t *testing.T) {
	t.Parallel()

	type runResult struct {
		out []byte
		err error
	}

	// Helper: runner that returns given results for (docker compose version)
	// and (docker-compose --version) in that order.
	makeRunner := func(v2Result, v1Result runResult) *dockerx.FakeCommandRunner {
		return &dockerx.FakeCommandRunner{
			Responses: []dockerx.CmdResponse{
				{Out: v2Result.out, Err: v2Result.err},
				{Out: v1Result.out, Err: v1Result.err},
			},
		}
	}

	tests := []struct {
		name        string
		runner      *dockerx.FakeCommandRunner
		wantVariant dockerx.ComposeVariant
		wantErr     bool
	}{
		{
			name: "v2 only",
			runner: makeRunner(
				runResult{out: []byte("Docker Compose version v2.24.0")},
				runResult{err: errors.New("not found")},
			),
			wantVariant: dockerx.ComposeV2,
		},
		{
			name: "v1 only",
			runner: makeRunner(
				runResult{err: errors.New("not found")},
				runResult{out: []byte("docker-compose version 1.29.2")},
			),
			wantVariant: dockerx.ComposeV1,
		},
		{
			name: "both present, v2 preferred",
			runner: makeRunner(
				runResult{out: []byte("Docker Compose version v2.24.0")},
				runResult{out: []byte("docker-compose version 1.29.2")},
			),
			wantVariant: dockerx.ComposeV2,
		},
		{
			name: "neither available",
			runner: makeRunner(
				runResult{err: errors.New("not found")},
				runResult{err: errors.New("not found")},
			),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info, err := detect.Compose(ctx(), tc.runner)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var ce *cnerr.Error
				if !errors.As(err, &ce) {
					t.Fatalf("expected *cnerr.Error, got %T", err)
				}
				if !strings.Contains(ce.FixSuggestion, "docker-compose-plugin") {
					t.Errorf("fix suggestion should mention docker-compose-plugin, got: %q", ce.FixSuggestion)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Variant != tc.wantVariant {
				t.Errorf("Compose().Variant = %v, want %v", info.Variant, tc.wantVariant)
			}
		})
	}
}

// ─── Connectivity ────────────────────────────────────────────────────────────

func TestConnectivity(t *testing.T) {
	t.Parallel()

	t.Run("all reachable", func(t *testing.T) {
		t.Parallel()
		prober := &dockerx.FakeHTTPProber{
			Responses: []dockerx.HTTPResponse{
				{Resp: responseBody(200, "")},
				{Resp: responseBody(200, "")},
				{Resp: responseBody(200, "")},
			},
		}
		results, err := detect.Connectivity(ctx(), prober, detect.DefaultConnectivityURLs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, r := range results {
			if !r.Reachable {
				t.Errorf("expected %s to be reachable", r.URL)
			}
		}
	})

	t.Run("one endpoint unreachable", func(t *testing.T) {
		t.Parallel()
		prober := &dockerx.FakeHTTPProber{
			Responses: []dockerx.HTTPResponse{
				{Resp: responseBody(200, "")},
				{Resp: responseBody(200, "")},
				{Err: errors.New("connection timed out")},
			},
		}
		results, err := detect.Connectivity(ctx(), prober, detect.DefaultConnectivityURLs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}
		if !results[0].Reachable || !results[1].Reachable {
			t.Error("expected first two endpoints to be reachable")
		}
		if results[2].Reachable {
			t.Error("expected third endpoint to be unreachable")
		}
		if results[2].Err == nil {
			t.Error("expected non-nil Err for unreachable endpoint")
		}
	})

	t.Run("context cancelled stops probing", func(t *testing.T) {
		t.Parallel()
		cctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately
		prober := &dockerx.FakeHTTPProber{}
		_, err := detect.Connectivity(cctx, prober, detect.DefaultConnectivityURLs)
		if err == nil {
			t.Fatal("expected context error")
		}
	})
}

// ─── DiskSpace ───────────────────────────────────────────────────────────────

func TestDiskSpace(t *testing.T) {
	t.Parallel()

	t.Run("sufficient space", func(t *testing.T) {
		t.Parallel()
		provider := func(_ string) (uint64, error) { return 5000, nil }
		free, err := detect.DiskSpaceWithProvider(ctx(), "/data", provider)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if free != 5000 {
			t.Errorf("DiskSpace = %d, want 5000", free)
		}
	})

	t.Run("insufficient space returns structured error", func(t *testing.T) {
		t.Parallel()
		provider := func(_ string) (uint64, error) { return 1024, nil }
		_, err := detect.DiskSpaceWithProvider(ctx(), "/data", provider)
		if err == nil {
			t.Fatal("expected error")
		}
		var ce *cnerr.Error
		if !errors.As(err, &ce) {
			t.Fatalf("expected *cnerr.Error, got %T", err)
		}
		if !strings.Contains(ce.FixSuggestion, "docker image prune") {
			t.Errorf("fix suggestion should mention docker image prune, got: %q", ce.FixSuggestion)
		}
	})

	t.Run("provider error returns structured error", func(t *testing.T) {
		t.Parallel()
		provider := func(_ string) (uint64, error) { return 0, errors.New("stat failed") }
		_, err := detect.DiskSpaceWithProvider(ctx(), "/data", provider)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
