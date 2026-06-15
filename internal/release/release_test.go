package release_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func okResp(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

func statusResp(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}
}

// validManifest returns a minimal but complete Manifest ready for serialising.
func validManifest() release.Manifest {
	return release.Manifest{
		Agent: release.AgentSection{
			Latest: "1.8.3",
			Releases: map[string]release.AgentRelease{
				"1.8.3": {
					Date:  "2026-06-12",
					Image: "crenein/c-network-agent-back:1.8.3",
					Mongo: map[string]string{
						"7": "mongodb/mongodb-community-server:7.0-ubuntu2204",
						"4": "mongo:4.4",
					},
					Notes: "Fixes bugs 1&2 Telegram",
				},
				"1.8.2": {
					Date:  "2026-05-28",
					Image: "crenein/c-network-agent-back:1.8.2",
					Mongo: map[string]string{
						"7": "mongodb/mongodb-community-server:7.0-ubuntu2204",
						"4": "mongo:4.4",
					},
					Notes: "anomaly: one notification per cycle",
				},
			},
		},
		CLI: release.CLISection{
			Latest: "0.1.0",
			Releases: map[string]release.CLIRelease{
				"0.1.0": {Date: "2026-06-12", Notes: "Initial release"},
			},
		},
	}
}

// ─── CompareSemver ────────────────────────────────────────────────────────────

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.9.9", "2.0.0", -1},
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.0", 1},
		{"1.8.3", "1.8.2", 1},
		{"1.8.2", "1.8.3", -1},
		{"1.8.3", "1.8.3", 0},
	}
	for _, tc := range cases {
		got := release.CompareSemver(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// ─── Manifest validation ──────────────────────────────────────────────────────

func TestParseManifest_Valid(t *testing.T) {
	m := validManifest()
	data := mustMarshal(m)
	got, err := release.ParseManifest(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Agent.Latest != "1.8.3" {
		t.Errorf("agent.latest = %q, want 1.8.3", got.Agent.Latest)
	}
	if got.CLI.Latest != "0.1.0" {
		t.Errorf("cli.latest = %q, want 0.1.0", got.CLI.Latest)
	}
}

func TestParseManifest_InvalidJSON(t *testing.T) {
	_, err := release.ParseManifest([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestManifestValidate_LatestMissingFromReleases(t *testing.T) {
	m := validManifest()
	m.Agent.Latest = "9.9.9" // not in releases
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal("expected error when agent.latest not in releases")
	}
}

func TestManifestValidate_EmptyImage(t *testing.T) {
	m := validManifest()
	rel := m.Agent.Releases["1.8.3"]
	rel.Image = ""
	m.Agent.Releases["1.8.3"] = rel
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestManifestValidate_MongoMissingKey7(t *testing.T) {
	m := validManifest()
	rel := m.Agent.Releases["1.8.3"]
	rel.Mongo = map[string]string{"4": "mongo:4.4"}
	m.Agent.Releases["1.8.3"] = rel
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal(`expected error for missing mongo key "7"`)
	}
}

func TestManifestValidate_MongoMissingKey4(t *testing.T) {
	m := validManifest()
	rel := m.Agent.Releases["1.8.3"]
	rel.Mongo = map[string]string{"7": "mongodb/mongodb-community-server:7.0-ubuntu2204"}
	m.Agent.Releases["1.8.3"] = rel
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal(`expected error for missing mongo key "4"`)
	}
}

func TestManifestValidate_InvalidSemverKey(t *testing.T) {
	m := validManifest()
	// Add a badly-formatted key.
	m.Agent.Releases["not-semver"] = release.AgentRelease{
		Image: "crenein/c-network-agent-back:bad",
		Mongo: map[string]string{"7": "a", "4": "b"},
	}
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal("expected error for invalid semver key")
	}
}

func TestManifestValidate_CLILatestMissing(t *testing.T) {
	m := validManifest()
	m.CLI.Latest = "9.9.9"
	data := mustMarshal(m)
	_, err := release.ParseManifest(data)
	if err == nil {
		t.Fatal("expected error when cli.latest not in cli.releases")
	}
}

// ─── Resolution helpers ───────────────────────────────────────────────────────

func TestResolveAgentVersion_Default(t *testing.T) {
	m := validManifest()
	ver, err := release.ResolveAgentVersion(&m, "")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "1.8.3" {
		t.Errorf("got %q, want 1.8.3", ver)
	}
}

func TestResolveAgentVersion_Pin(t *testing.T) {
	m := validManifest()
	ver, err := release.ResolveAgentVersion(&m, "1.8.2")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "1.8.2" {
		t.Errorf("got %q, want 1.8.2", ver)
	}
}

func TestResolveAgentVersion_PinAbsent(t *testing.T) {
	m := validManifest()
	_, err := release.ResolveAgentVersion(&m, "9.9.9")
	if err == nil {
		t.Fatal("expected error for absent pin version")
	}
}

func TestResolveAgentImage(t *testing.T) {
	m := validManifest()
	img, err := release.ResolveAgentImage(&m, "1.8.3")
	if err != nil {
		t.Fatal(err)
	}
	if img != "crenein/c-network-agent-back:1.8.3" {
		t.Errorf("unexpected image: %q", img)
	}
}

func TestResolveMongoImage_AVX(t *testing.T) {
	m := validManifest()
	img, err := release.ResolveMongoImage(&m, "1.8.3", true)
	if err != nil {
		t.Fatal(err)
	}
	if img != "mongodb/mongodb-community-server:7.0-ubuntu2204" {
		t.Errorf("unexpected mongo image (AVX): %q", img)
	}
}

func TestResolveMongoImage_NoAVX(t *testing.T) {
	m := validManifest()
	img, err := release.ResolveMongoImage(&m, "1.8.3", false)
	if err != nil {
		t.Fatal(err)
	}
	if img != "mongo:4.4" {
		t.Errorf("unexpected mongo image (no-AVX): %q", img)
	}
}

func TestResolveReleaseNotes(t *testing.T) {
	m := validManifest()
	notes := release.ResolveReleaseNotes(&m, "1.8.3")
	if notes != "Fixes bugs 1&2 Telegram" {
		t.Errorf("unexpected notes: %q", notes)
	}
	// Absent version returns empty string.
	if got := release.ResolveReleaseNotes(&m, "0.0.0"); got != "" {
		t.Errorf("expected empty notes for absent version, got %q", got)
	}
}

// ─── Update-available computation ─────────────────────────────────────────────

func TestComputeUpdateInfo_BothAvailable(t *testing.T) {
	m := validManifest()
	// Simulate manifest with higher versions.
	m.CLI.Latest = "0.2.0"
	m.CLI.Releases["0.2.0"] = release.CLIRelease{Date: "2026-07-01", Notes: "v0.2.0"}
	m.Agent.Latest = "1.8.4"
	m.Agent.Releases["1.8.4"] = release.AgentRelease{
		Image: "crenein/c-network-agent-back:1.8.4",
		Mongo: map[string]string{"7": "mongodb/mongodb-community-server:7.0-ubuntu2204", "4": "mongo:4.4"},
	}

	info := release.ComputeUpdateInfo(&m, "0.1.0", "1.8.3")
	if info.CLIStatus != release.UpdateAvailable {
		t.Errorf("CLI status = %v, want UpdateAvailable", info.CLIStatus)
	}
	if info.AgentStatus != release.UpdateAvailable {
		t.Errorf("Agent status = %v, want UpdateAvailable", info.AgentStatus)
	}
}

func TestComputeUpdateInfo_UpToDate(t *testing.T) {
	m := validManifest()
	info := release.ComputeUpdateInfo(&m, "0.1.0", "1.8.3")
	if info.CLIStatus != release.UpdateUpToDate {
		t.Errorf("CLI status = %v, want UpdateUpToDate", info.CLIStatus)
	}
	if info.AgentStatus != release.UpdateUpToDate {
		t.Errorf("Agent status = %v, want UpdateUpToDate", info.AgentStatus)
	}
}

func TestComputeUpdateInfo_UnknownAgentSuppressed(t *testing.T) {
	m := validManifest()
	info := release.ComputeUpdateInfo(&m, "0.1.0", "unknown")
	if info.CLIStatus != release.UpdateUpToDate {
		t.Errorf("CLI status = %v, want UpdateUpToDate", info.CLIStatus)
	}
	if info.AgentStatus != release.UpdateUnknown {
		t.Errorf("Agent status = %v, want UpdateUnknown (suppressed), got %v", release.UpdateUnknown, info.AgentStatus)
	}
}

func TestComputeUpdateInfo_UnknownWithDigestSuppressed(t *testing.T) {
	m := validManifest()
	// "unknown (digest sha256:...)" should also be suppressed.
	info := release.ComputeUpdateInfo(&m, "0.1.0", "unknown (digest sha256:abc123)")
	if info.AgentStatus != release.UpdateUnknown {
		t.Errorf("Agent status = %v, want UpdateUnknown for digest version", info.AgentStatus)
	}
}

// ─── Cache: TTL, bypass, corruption ──────────────────────────────────────────

// fakeHTTPForManifest builds HTTP responses for: releases/latest then asset download.
func setupHTTPForManifest(t *testing.T, m release.Manifest) *dockerx.FakeHTTPProber {
	t.Helper()

	latestRelease := map[string]any{
		"tag_name": "v0.1.0",
		"assets": []map[string]any{
			{
				"name":                 "versions.json",
				"browser_download_url": "https://example.com/versions.json",
			},
		},
	}

	return &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: okResp(mustMarshal(latestRelease))},
			{Resp: okResp(mustMarshal(m))},
		},
	}
}

func TestFetchManifest_LiveFetch(t *testing.T) {
	m := validManifest()
	http := setupHTTPForManifest(t, m)
	fs := dockerx.NewFakeFS(nil)
	now := func() time.Time { return time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC) }

	client := release.NewManifestClient(http, nil, fs, "/home/test", now)
	got, err := client.FetchManifest(context.Background(), false)
	if err != nil {
		t.Fatalf("FetchManifest error: %v", err)
	}
	if got.Agent.Latest != "1.8.3" {
		t.Errorf("agent.latest = %q, want 1.8.3", got.Agent.Latest)
	}

	// Cache should have been written.
	if len(fs.Writes) == 0 {
		t.Error("expected cache write after live fetch")
	}
}

func TestFetchManifest_ServedFromCache(t *testing.T) {
	m := validManifest()
	// Pre-populate the cache (fresh).
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	cacheEntry := map[string]any{
		"fetched_at":    now.Format(time.RFC3339),
		"manifest_json": mustMarshal(m),
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/home/test/.crenein/version-cache.json": mustMarshal(cacheEntry),
	})

	// HTTP prober has no responses — any call would panic/fail.
	http := &dockerx.FakeHTTPProber{}
	clock := func() time.Time { return now.Add(1 * time.Hour) } // 1h after fetch → still fresh

	client := release.NewManifestClient(http, nil, fs, "/home/test", clock)
	got, err := client.FetchManifest(context.Background(), false)
	if err != nil {
		t.Fatalf("FetchManifest error: %v", err)
	}
	if got.Agent.Latest != "1.8.3" {
		t.Errorf("agent.latest = %q, want 1.8.3", got.Agent.Latest)
	}

	// No HTTP calls should have been made.
	if len(http.Requests) != 0 {
		t.Errorf("expected 0 HTTP calls (cache hit), got %d", len(http.Requests))
	}
}

func TestFetchManifest_CacheExpiredTriggersFetch(t *testing.T) {
	m := validManifest()
	fetchTime := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC) // 2 days ago
	cacheEntry := map[string]any{
		"fetched_at":    fetchTime.Format(time.RFC3339),
		"manifest_json": mustMarshal(m),
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/home/test/.crenein/version-cache.json": mustMarshal(cacheEntry),
	})

	http := setupHTTPForManifest(t, m)
	now := func() time.Time { return time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC) }

	client := release.NewManifestClient(http, nil, fs, "/home/test", now)
	_, err := client.FetchManifest(context.Background(), false)
	if err != nil {
		t.Fatalf("FetchManifest error: %v", err)
	}

	// Should have made 2 HTTP calls (releases/latest + asset download).
	if len(http.Requests) != 2 {
		t.Errorf("expected 2 HTTP calls on expired cache, got %d", len(http.Requests))
	}
}

func TestFetchManifest_CorruptCacheTreatedAsAbsent(t *testing.T) {
	m := validManifest()
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/home/test/.crenein/version-cache.json": []byte("not-json-garbage!!!"),
	})

	http := setupHTTPForManifest(t, m)
	now := func() time.Time { return time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC) }

	client := release.NewManifestClient(http, nil, fs, "/home/test", now)
	_, err := client.FetchManifest(context.Background(), false)
	if err != nil {
		t.Fatalf("FetchManifest error: %v", err)
	}
	// Corrupt cache → live fetch → 2 HTTP calls.
	if len(http.Requests) != 2 {
		t.Errorf("expected 2 HTTP calls on corrupt cache, got %d", len(http.Requests))
	}
}

func TestFetchManifest_BypassCache(t *testing.T) {
	m := validManifest()
	// Cache is fresh but bypass is true.
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	cacheEntry := map[string]any{
		"fetched_at":    now.Format(time.RFC3339),
		"manifest_json": mustMarshal(m),
	}
	fs := dockerx.NewFakeFS(map[string][]byte{
		"/home/test/.crenein/version-cache.json": mustMarshal(cacheEntry),
	})
	http := setupHTTPForManifest(t, m)
	clock := func() time.Time { return now.Add(30 * time.Minute) }

	client := release.NewManifestClient(http, nil, fs, "/home/test", clock)
	_, err := client.FetchManifest(context.Background(), true /* bypassCache */)
	if err != nil {
		t.Fatalf("FetchManifest error: %v", err)
	}
	// Must have gone to the network despite fresh cache.
	if len(http.Requests) != 2 {
		t.Errorf("expected 2 HTTP calls with bypassCache=true, got %d", len(http.Requests))
	}
}

// ─── /health detection and Docker fallback ────────────────────────────────────

func TestDetectAgentVersion_HealthHTTPS(t *testing.T) {
	healthBody := mustMarshal(map[string]string{
		"status":  "success",
		"message": "Connection Successful",
		"version": "1.8.3",
	})
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: okResp(healthBody)},
		},
	}
	client := release.NewManifestClient(http, nil, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	if ver != "1.8.3" {
		t.Errorf("got %q, want 1.8.3", ver)
	}
}

func TestDetectAgentVersion_HTTPSFails_FallsBackToHTTP(t *testing.T) {
	// First call (https) fails with connection error; second (http) succeeds.
	healthBody := mustMarshal(map[string]string{"version": "1.8.2"})
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Err: fmt.Errorf("connection refused")},
			{Resp: okResp(healthBody)},
		},
	}
	client := release.NewManifestClient(http, nil, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	if ver != "1.8.2" {
		t.Errorf("got %q, want 1.8.2", ver)
	}
}

func TestDetectAgentVersion_404FallsBackToDocker(t *testing.T) {
	// Both /health probes return 404 (legacy backend).
	docker := &dockerx.FakeClient{
		ContainerListOut: []dockerx.ContainerState{
			{Name: "agent-1", ImageID: "sha256:abc"},
		},
		ImageInspectOut: dockerx.ImageInfo{
			ID:       "sha256:abc123",
			RepoTags: []string{"crenein/c-network-agent-back:1.8.2"},
		},
	}
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: statusResp(http.StatusNotFound)},
			{Resp: statusResp(http.StatusNotFound)},
		},
	}
	client := release.NewManifestClient(http, docker, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	if ver != "1.8.2" {
		t.Errorf("got %q, want 1.8.2", ver)
	}
}

func TestDetectAgentVersion_DockerLatestTag(t *testing.T) {
	docker := &dockerx.FakeClient{
		ContainerListOut: []dockerx.ContainerState{
			{Name: "agent-1", ImageID: "sha256:deadbeef"},
		},
		ImageInspectOut: dockerx.ImageInfo{
			ID:       "sha256:deadbeef123",
			RepoTags: []string{"crenein/c-network-agent-back:latest"},
		},
	}
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: statusResp(http.StatusNotFound)},
			{Resp: statusResp(http.StatusNotFound)},
		},
	}
	client := release.NewManifestClient(http, docker, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	// The :latest tag carries no semantic version, so agent.version must be
	// exactly "unknown" (no embedded digest) per the spec.
	if ver != "unknown" {
		t.Errorf("got %q, want exactly %q", ver, "unknown")
	}
}

func TestDetectAgentVersion_AllFail(t *testing.T) {
	// Both /health probes fail; no containers found.
	docker := &dockerx.FakeClient{
		ContainerListErr: fmt.Errorf("docker not reachable"),
	}
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Err: fmt.Errorf("refused")},
			{Err: fmt.Errorf("refused")},
		},
	}
	client := release.NewManifestClient(http, docker, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	if ver != "unknown" {
		t.Errorf("got %q, want unknown", ver)
	}
}

func TestDetectAgentVersion_NoVersionField(t *testing.T) {
	// /health returns 200 but no "version" field (partial legacy backend).
	healthBody := mustMarshal(map[string]string{
		"status":  "success",
		"message": "Connection Successful",
	})
	docker := &dockerx.FakeClient{
		ContainerListOut: []dockerx.ContainerState{
			{Name: "agent-1", ImageID: "sha256:abc"},
		},
		ImageInspectOut: dockerx.ImageInfo{
			ID:       "sha256:abc",
			RepoTags: []string{"crenein/c-network-agent-back:1.8.1"},
		},
	}
	http := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			{Resp: okResp(healthBody)}, // https: 200 but no version field → fallback
			{Resp: okResp(healthBody)}, // http: same
		},
	}
	client := release.NewManifestClient(http, docker, dockerx.NewFakeFS(nil), "/home/test", time.Now)
	ver := client.DetectAgentVersion(context.Background())
	if ver != "1.8.1" {
		t.Errorf("got %q, want 1.8.1 (from Docker fallback)", ver)
	}
}

// ─── Seed data ────────────────────────────────────────────────────────────────

func TestAgentSeed_ContainsExpectedVersions(t *testing.T) {
	seed := release.AgentSeed()
	expected := []string{"1.8.3", "1.8.2", "1.8.1", "1.8.0", "1.6.1"}
	for _, ver := range expected {
		if _, ok := seed[ver]; !ok {
			t.Errorf("seed missing version %q", ver)
		}
	}
}

func TestAgentSeed_AllEntriesValid(t *testing.T) {
	seed := release.AgentSeed()
	for ver, rel := range seed {
		if rel.Image == "" {
			t.Errorf("seed[%q].image is empty", ver)
		}
		if _, ok := rel.Mongo["7"]; !ok {
			t.Errorf(`seed[%q].mongo missing key "7"`, ver)
		}
		if _, ok := rel.Mongo["4"]; !ok {
			t.Errorf(`seed[%q].mongo missing key "4"`, ver)
		}
	}
}
