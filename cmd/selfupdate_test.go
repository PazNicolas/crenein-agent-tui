package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/selfupdate"
)

// ─── Fakes ───────────────────────────────────────────────────────────────────

// fakeManifestClient is a test double for release.Client.
type fakeManifestClient struct {
	manifest     *release.Manifest
	fetchErr     *cnerr.Error
	agentVersion string
}

func (f *fakeManifestClient) FetchManifest(_ context.Context, _ bool) (*release.Manifest, *cnerr.Error) {
	return f.manifest, f.fetchErr
}

func (f *fakeManifestClient) DetectAgentVersion(_ context.Context) string {
	if f.agentVersion == "" {
		return "unknown"
	}
	return f.agentVersion
}

// fakePerformer is a test double for selfupdate.Performer.
type fakePerformer struct {
	result *selfupdate.Result
	err    error
}

func (f *fakePerformer) Update(_ context.Context, _, _ string, _ bool) (*selfupdate.Result, error) {
	return f.result, f.err
}

// testManifest builds a minimal valid Manifest for tests.
func testManifest(cliLatest, cliNotes string) *release.Manifest {
	return &release.Manifest{
		Agent: release.AgentSection{
			Latest: "1.8.3",
			Releases: map[string]release.AgentRelease{
				"1.8.3": {
					Date:  "2026-06-12",
					Image: "crenein/c-network-agent-back:1.8.3",
					Mongo: map[string]string{"7": "mongo7", "4": "mongo4"},
					Notes: "Agent release",
				},
			},
		},
		CLI: release.CLISection{
			Latest: cliLatest,
			Releases: map[string]release.CLIRelease{
				cliLatest: {Date: "2026-06-14", Notes: cliNotes},
			},
		},
	}
}

// ─── runCmd is a test helper that executes a cobra command and captures output ───

type cmdResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runSelfUpdateCmd runs the self-update command with the given args and deps,
// returning stdout, stderr, and the exit code encoded in the returned error.
func runSelfUpdateCmd(t *testing.T, args []string, deps selfUpdateDeps, version string) cmdResult {
	t.Helper()

	// Override build info for the test.
	oldBuild := build
	build = buildInfo{version: version}
	defer func() { build = oldBuild }()

	root := newRootCmd()
	// Replace the self-update subcommand with the one carrying test deps.
	for _, sub := range root.Commands() {
		if sub.Use == "self-update" {
			root.RemoveCommand(sub)
			break
		}
	}
	root.AddCommand(newSelfUpdateCmdWithDeps(deps))

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true

	root.SetArgs(append([]string{"self-update"}, args...))
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

// ─── Tests: --check exit codes ────────────────────────────────────────────────

func TestSelfUpdateCheck_UpToDate(t *testing.T) {
	m := testManifest("0.1.0", "Initial release")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		noTTYOverride:  true,
	}
	res := runSelfUpdateCmd(t, []string{"--check"}, deps, "0.1.0")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.exitCode)
	}
	if !strings.Contains(res.stdout, "up to date") {
		t.Errorf("stdout should mention 'up to date', got: %q", res.stdout)
	}
}

func TestSelfUpdateCheck_UpdateAvailable(t *testing.T) {
	m := testManifest("0.2.0", "New release")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		noTTYOverride:  true,
	}
	res := runSelfUpdateCmd(t, []string{"--check"}, deps, "0.1.0")
	if res.exitCode != 10 {
		t.Errorf("exit code = %d, want 10", res.exitCode)
	}
	if !strings.Contains(res.stdout, "0.1.0") || !strings.Contains(res.stdout, "0.2.0") {
		t.Errorf("stdout should mention both versions, got: %q", res.stdout)
	}
}

func TestSelfUpdateCheck_NetworkError(t *testing.T) {
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{
			fetchErr: cnerr.New("network error", "check connectivity"),
		},
		noTTYOverride: true,
	}
	res := runSelfUpdateCmd(t, []string{"--check"}, deps, "0.1.0")
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.exitCode)
	}
	if !strings.Contains(res.stderr, "error") {
		t.Errorf("stderr should mention error, got: %q", res.stderr)
	}
}

func TestSelfUpdateCheck_DevBuild(t *testing.T) {
	deps := selfUpdateDeps{noTTYOverride: true}
	res := runSelfUpdateCmd(t, []string{"--check"}, deps, "dev")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0 for dev build check", res.exitCode)
	}
	if !strings.Contains(res.stdout, "dev") {
		t.Errorf("stdout should mention dev build, got: %q", res.stdout)
	}
}

// ─── Tests: happy path update ─────────────────────────────────────────────────

func TestSelfUpdate_HappyPath(t *testing.T) {
	m := testManifest("0.2.0", "Bug fixes")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		updater: &fakePerformer{
			result: &selfupdate.Result{
				Action:      "updated",
				FromVersion: "0.1.0",
				ToVersion:   "0.2.0",
			},
		},
		noTTYOverride: true,
	}
	res := runSelfUpdateCmd(t, []string{"--yes"}, deps, "0.1.0")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "updated") {
		t.Errorf("stdout should contain 'updated', got: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "0.1.0") || !strings.Contains(res.stdout, "0.2.0") {
		t.Errorf("stdout should mention both versions, got: %q", res.stdout)
	}
}

// ─── Tests: already up to date ────────────────────────────────────────────────

func TestSelfUpdate_AlreadyUpToDate(t *testing.T) {
	m := testManifest("0.1.0", "Initial")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		noTTYOverride:  true,
	}
	res := runSelfUpdateCmd(t, []string{"--yes"}, deps, "0.1.0")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.exitCode)
	}
	if !strings.Contains(res.stdout, "already up to date") {
		t.Errorf("stdout should mention 'already up to date', got: %q", res.stdout)
	}
}

// ─── Tests: permission denied → sudo hint ─────────────────────────────────────

func TestSelfUpdate_PermissionDenied_SudoHint(t *testing.T) {
	m := testManifest("0.2.0", "")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		updater: &fakePerformer{
			err: cnerr.Wrap(
				"selfupdate.probeWritable",
				errors.New("permission denied"),
				"try running with sudo: sudo crenein-agent self-update",
			),
		},
		noTTYOverride: true,
	}
	res := runSelfUpdateCmd(t, []string{"--yes"}, deps, "0.1.0")
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.exitCode)
	}
	if !strings.Contains(res.stderr, "sudo") {
		t.Errorf("stderr should contain 'sudo', got: %q", res.stderr)
	}
}

// ─── Tests: checksum mismatch ─────────────────────────────────────────────────

func TestSelfUpdate_ChecksumMismatch(t *testing.T) {
	m := testManifest("0.2.0", "")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		updater: &fakePerformer{
			err: cnerr.New(
				`selfupdate.verifyChecksum: SHA256 mismatch (got abc, want def)`,
				"",
			),
		},
		noTTYOverride: true,
	}
	res := runSelfUpdateCmd(t, []string{"--yes"}, deps, "0.1.0")
	if res.exitCode != 1 {
		t.Errorf("exit code = %d, want 1", res.exitCode)
	}
	if !strings.Contains(res.stderr, "checksum") {
		t.Errorf("stderr should mention checksum, got: %q", res.stderr)
	}
}

// ─── Tests: no TTY without --yes ─────────────────────────────────────────────

func TestSelfUpdate_NoTTY_WithoutYes(t *testing.T) {
	m := testManifest("0.2.0", "")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		noTTYOverride:  true, // no TTY
		// no --yes flag
	}
	res := runSelfUpdateCmd(t, []string{}, deps, "0.1.0")
	if res.exitCode != 64 {
		t.Errorf("exit code = %d, want 64 (no TTY without --yes → usage error)", res.exitCode)
	}
	if !strings.Contains(res.stderr, "--yes") {
		t.Errorf("stderr should mention --yes, got: %q", res.stderr)
	}
}

// ─── Tests: downgrade with --version ─────────────────────────────────────────

func TestSelfUpdate_Downgrade(t *testing.T) {
	m := testManifest("0.2.0", "")
	deps := selfUpdateDeps{
		manifestClient: &fakeManifestClient{manifest: m},
		updater: &fakePerformer{
			result: &selfupdate.Result{
				Action:      "downgraded",
				FromVersion: "0.2.0",
				ToVersion:   "0.1.0",
			},
		},
		noTTYOverride: true,
	}
	res := runSelfUpdateCmd(t, []string{"--version", "0.1.0", "--yes"}, deps, "0.2.0")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %q", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stdout, "downgraded") {
		t.Errorf("stdout should contain 'downgraded', got: %q", res.stdout)
	}
}

// ─── Tests: GitHubReleaseSource asset name convention ─────────────────────────

func TestGitHubReleaseSource_AssetNameConvention(t *testing.T) {
	// Verify the goreleaser naming contract: crenein-agent_<version>_<os>_<arch>.tar.gz
	src := &release.GitHubReleaseSource{
		GOOS:   "linux",
		GOARCH: "amd64",
	}
	cases := []struct {
		version string
		want    string
	}{
		{"0.1.0", "crenein-agent_0.1.0_linux_amd64.tar.gz"},
		{"0.2.0", "crenein-agent_0.2.0_linux_amd64.tar.gz"},
	}
	for _, tc := range cases {
		got := src.AssetName(tc.version)
		if got != tc.want {
			t.Errorf("AssetName(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

// ─── Tests: GitHubReleaseSource ResolveAsset with fake HTTP ──────────────────

func TestGitHubReleaseSource_ResolveAsset_Latest(t *testing.T) {
	releaseResp := map[string]any{
		"tag_name": "v0.2.0",
		"assets": []map[string]any{
			{
				"name":                 "crenein-agent_0.2.0_linux_amd64.tar.gz",
				"browser_download_url": "https://example.com/crenein-agent_0.2.0_linux_amd64.tar.gz",
			},
			{
				"name":                 "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt",
			},
		},
	}
	body, _ := json.Marshal(releaseResp)

	prober := dockerx.HTTPProberFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})

	src := &release.GitHubReleaseSource{
		HTTP:   prober,
		GOOS:   "linux",
		GOARCH: "amd64",
	}

	asset, err := src.ResolveAsset(context.Background(), "")
	if err != nil {
		t.Fatalf("ResolveAsset error: %v", err)
	}
	if asset.Name != "crenein-agent_0.2.0_linux_amd64.tar.gz" {
		t.Errorf("Name = %q, want crenein-agent_0.2.0_linux_amd64.tar.gz", asset.Name)
	}
	if asset.DownloadURL == "" {
		t.Error("DownloadURL is empty")
	}
	if asset.ChecksumsURL == "" {
		t.Error("ChecksumsURL is empty")
	}
}

func TestGitHubReleaseSource_ResolveAsset_ByTag(t *testing.T) {
	releaseResp := map[string]any{
		"tag_name": "v0.1.0",
		"assets": []map[string]any{
			{
				"name":                 "crenein-agent_0.1.0_linux_amd64.tar.gz",
				"browser_download_url": "https://example.com/crenein-agent_0.1.0_linux_amd64.tar.gz",
			},
			{
				"name":                 "checksums.txt",
				"browser_download_url": "https://example.com/checksums.txt",
			},
		},
	}
	body, _ := json.Marshal(releaseResp)

	var capturedURL string
	prober := dockerx.HTTPProberFunc(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL.String()
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})

	src := &release.GitHubReleaseSource{
		HTTP:   prober,
		GOOS:   "linux",
		GOARCH: "amd64",
	}

	asset, err := src.ResolveAsset(context.Background(), "0.1.0")
	if err != nil {
		t.Fatalf("ResolveAsset error: %v", err)
	}
	if asset.Name != "crenein-agent_0.1.0_linux_amd64.tar.gz" {
		t.Errorf("Name = %q, want crenein-agent_0.1.0_linux_amd64.tar.gz", asset.Name)
	}
	if !strings.Contains(capturedURL, "tags/v0.1.0") {
		t.Errorf("expected URL to contain 'tags/v0.1.0', got %q", capturedURL)
	}
}

func TestGitHubReleaseSource_ResolveAsset_MissingAsset(t *testing.T) {
	releaseResp := map[string]any{
		"tag_name": "v0.2.0",
		"assets":   []map[string]any{},
	}
	body, _ := json.Marshal(releaseResp)

	prober := dockerx.HTTPProberFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})

	src := &release.GitHubReleaseSource{
		HTTP:   prober,
		GOOS:   "linux",
		GOARCH: "amd64",
	}

	_, err := src.ResolveAsset(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when asset is missing, got nil")
	}
}

func TestGitHubReleaseSource_ResolveAsset_NetworkError(t *testing.T) {
	prober := dockerx.HTTPProberFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	})
	src := &release.GitHubReleaseSource{
		HTTP:   prober,
		GOOS:   "linux",
		GOARCH: "amd64",
	}
	_, err := src.ResolveAsset(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on network failure, got nil")
	}
}

// ─── Tests: exitCodeError mechanism ──────────────────────────────────────────

func TestExitCodeError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("some cause")
	ecErr := &exitCodeError{code: 10, err: cause}
	if ecErr.Error() == "" {
		t.Error("Error() should not be empty")
	}
	if !errors.Is(ecErr, cause) {
		t.Error("errors.Is should find the cause via Unwrap")
	}
}

// ─── Test helper: ensure --check with force-check calls FetchManifest(true) ──

func TestSelfUpdateCheck_ForceCheck(t *testing.T) {
	var bypassCacheCapture bool
	mc := &capturingManifestClient{
		manifest: testManifest("0.1.0", ""),
		onFetch:  func(bypass bool) { bypassCacheCapture = bypass },
	}
	deps := selfUpdateDeps{
		manifestClient: mc,
		noTTYOverride:  true,
	}
	res := runSelfUpdateCmd(t, []string{"--check", "--force-check"}, deps, "0.1.0")
	if res.exitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.exitCode)
	}
	if !bypassCacheCapture {
		t.Error("FetchManifest should be called with bypassCache=true when --force-check is set")
	}
}

// capturingManifestClient wraps fakeManifestClient to capture the bypassCache arg.
type capturingManifestClient struct {
	manifest *release.Manifest
	fetchErr *cnerr.Error
	onFetch  func(bypassCache bool)
}

func (c *capturingManifestClient) FetchManifest(ctx context.Context, bypassCache bool) (*release.Manifest, *cnerr.Error) {
	if c.onFetch != nil {
		c.onFetch(bypassCache)
	}
	return c.manifest, c.fetchErr
}

func (c *capturingManifestClient) DetectAgentVersion(_ context.Context) string { return "unknown" }

// Compile-time interface checks.
var _ release.Client = (*fakeManifestClient)(nil)
var _ release.Client = (*capturingManifestClient)(nil)
var _ selfupdate.Performer = (*fakePerformer)(nil)
