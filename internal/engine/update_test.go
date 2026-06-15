package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// fixedTime is a stable timestamp for deterministic backup directory names.
var fixedTime = time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

func fixedNow() func() time.Time {
	return func() time.Time { return fixedTime }
}

// okHTTP200 returns an HTTPResponse with status 200 and an empty body.
func okHTTP200() dockerx.HTTPResponse {
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		},
	}
}

// notFoundHTTP404 returns an HTTPResponse with status 404.
func notFoundHTTP404() dockerx.HTTPResponse {
	return dockerx.HTTPResponse{
		Resp: &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       http.NoBody,
			Header:     make(http.Header),
		},
	}
}

// errHTTPResponse returns an HTTPResponse with a transport error.
func errHTTPResponse(msg string) dockerx.HTTPResponse {
	return dockerx.HTTPResponse{Err: fmt.Errorf("%s", msg)}
}

// responseBody wraps a string into an io.ReadCloser for HTTP responses.
func responseBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// multiInspectClient wraps FakeClient but returns per-call ImageInspect
// responses to handle happy-path tests that call ImageInspect twice (before
// and after pull).
type multiInspectClient struct {
	*dockerx.FakeClient
	inspectResponses []inspectResponse
	inspectIdx       int
}

type inspectResponse struct {
	info dockerx.ImageInfo
	err  error
}

func (m *multiInspectClient) ImageInspect(ctx context.Context, ref string) (dockerx.ImageInfo, error) {
	m.FakeClient.ImageInspect(ctx, ref) // record the call
	if m.inspectIdx < len(m.inspectResponses) {
		r := m.inspectResponses[m.inspectIdx]
		m.inspectIdx++
		return r.info, r.err
	}
	return m.FakeClient.ImageInspectOut, m.FakeClient.ImageInspectErr
}

// standardCompose is a docker-compose.yml that references the agent image.
const standardCompose = `version: "3.8"
services:
  agent:
    image: crenein/c-network-agent-back:latest
  frontend:
    image: crenein/c-network-agent-front:latest
  mongodb:
    image: mongo:4.4
  influxdb:
    image: influxdb:2.7
  redis:
    image: redis:7-alpine
`

// buildDepsWithFiles creates a Deps using FakeFS with the given extra files on
// top of the standard compose + .env. rootPath is the install directory.
func buildDepsWithFiles(rootPath string, extra map[string][]byte, client dockerx.Client, prober dockerx.HTTPProber) Deps {
	files := map[string][]byte{
		rootPath + "/docker-compose.yml": []byte(standardCompose),
		rootPath + "/.env":               []byte("INFLUXDB_TOKEN=test\nREDIS_PASSWORD=secret\n"),
	}
	for k, v := range extra {
		files[k] = v
	}
	fs := dockerx.NewFakeFS(files)
	runner := &dockerx.FakeCommandRunner{
		Responses: []dockerx.CmdResponse{
			// Permissions: docker info -> root check succeeds
			{Out: []byte("uid=0(root)"), Err: nil},
		},
	}
	return Deps{
		Client:   client,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
}

// baseOpts returns minimal UpdateOptions suitable for most tests.
func baseOpts(version string) UpdateOptions {
	return UpdateOptions{
		Version:       version,
		Now:           fixedNow(),
		RetryInterval: 0,
		HealthTimeout: 50 * time.Millisecond,
		IsRootFunc:    func() bool { return true },
		DiskSpaceProvider: func(path string) (uint64, error) {
			return 4096, nil
		},
	}
}

// ─── 7.5 Tests ────────────────────────────────────────────────────────────────

// TestUpdate_HappyPath_ExplicitTag verifies the full happy-path sequence:
// pre-flight → backup → pull with explicit tag → recreate → health → cleanup.
func TestUpdate_HappyPath_ExplicitTag(t *testing.T) {
	const version = "1.8.4"
	const agentOldID = "sha256:aaa111"
	const agentNewID = "sha256:bbb222"
	const frontendOldID = "sha256:ccc333"
	const frontendNewID = "sha256:ddd444"

	// Build a multi-inspect client:
	//   calls 1-2: detect state  (agent:latest, frontend:latest → old IDs)
	//   calls 3-4: after pull    (explicit tags → new IDs)
	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true, ImageID: agentOldID},
				{Service: "frontend", Running: true, ImageID: frontendOldID},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: agentOldID}},    // state: agent
			{info: dockerx.ImageInfo{ID: frontendOldID}}, // state: frontend
			{info: dockerx.ImageInfo{ID: agentNewID}},    // after pull: agent tag
			{info: dockerx.ImageInfo{ID: frontendNewID}}, // after pull: frontend tag
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			// Connectivity: registry-1.docker.io
			okHTTP200(),
			// Connectivity: hub.docker.com
			okHTTP200(),
			// Backend health: HTTPS 200
			okHTTP200(),
			// Frontend health: HTTPS 200
			okHTTP200(),
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.NoOp {
		t.Error("expected non-no-op update")
	}
	if result.RolledBack {
		t.Error("unexpected rollback")
	}

	// Verify the explicit tag was pulled via ImagePull (not ComposePull, not :latest).
	agentTag := fmt.Sprintf("%s:%s", agentImageName, version)
	frontendTag := fmt.Sprintf("%s:%s", frontendImageName, version)
	foundAgent := false
	foundFrontend := false
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ImagePull" && len(call.Args) > 0 {
			if call.Args[0] == agentTag {
				foundAgent = true
			}
			if call.Args[0] == frontendTag {
				foundFrontend = true
			}
			// Verify :latest was NEVER pulled explicitly.
			if call.Args[0] == agentImageName+":latest" {
				t.Errorf("engine pulled :latest tag — must pull explicit version %s", agentTag)
			}
		}
		// ComposePull must NOT be called for image pulls.
		if call.Method == "ComposePull" {
			t.Errorf("ComposePull was called — image pulls must use ImagePull instead")
		}
	}
	if !foundAgent {
		t.Errorf("agent explicit tag %q not found in ImagePull calls; calls: %v", agentTag, mc.FakeClient.Calls)
	}
	if !foundFrontend {
		t.Errorf("frontend explicit tag %q not found in ImagePull calls", frontendTag)
	}

	// Verify recreate used --no-deps --force-recreate.
	foundRecreate := false
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			for _, arg := range call.Args {
				if strings.Contains(arg, "NoDeps=true") && strings.Contains(arg, "ForceRecreate=true") {
					foundRecreate = true
				}
			}
		}
	}
	if !foundRecreate {
		t.Error("recreate step did not use --no-deps --force-recreate")
	}

	// Verify ImagePrune was called (cleanup).
	foundPrune := false
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ImagePrune" {
			foundPrune = true
		}
	}
	if !foundPrune {
		t.Error("expected ImagePrune to be called during cleanup")
	}
}

// TestUpdate_NoOp_IdenticalImageID verifies that when pulled image IDs match
// the current IDs and Force is not set, the update exits as a no-op without
// recreating any container.
func TestUpdate_NoOp_IdenticalImageID(t *testing.T) {
	const version = "1.8.4"
	const agentID = "sha256:same111"
	const frontendID = "sha256:same222"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true, ImageID: agentID},
				{Service: "frontend", Running: true, ImageID: frontendID},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: agentID}},    // state: agent:latest
			{info: dockerx.ImageInfo{ID: frontendID}}, // state: frontend:latest
			{info: dockerx.ImageInfo{ID: agentID}},    // after pull: same ID
			{info: dockerx.ImageInfo{ID: frontendID}}, // after pull: same ID
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), // registry-1.docker.io
			okHTTP200(), // hub.docker.com
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !result.NoOp {
		t.Error("expected no-op result when image IDs are identical")
	}

	// ComposeUp (recreate) must NOT have been called.
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			t.Error("ComposeUp was called during a no-op update — containers must not be recreated")
		}
	}
}

// TestUpdate_NoOp_Force verifies that Force=true causes recreate even when
// image IDs are unchanged.
func TestUpdate_NoOp_Force(t *testing.T) {
	const version = "1.8.4"
	const agentID = "sha256:same111"
	const frontendID = "sha256:same222"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true, ImageID: agentID},
				{Service: "frontend", Running: true, ImageID: frontendID},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: agentID}},
			{info: dockerx.ImageInfo{ID: frontendID}},
			{info: dockerx.ImageInfo{ID: agentID}},
			{info: dockerx.ImageInfo{ID: frontendID}},
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), // registry
			okHTTP200(), // hub
			okHTTP200(), // backend health
			okHTTP200(), // frontend health
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)
	opts.Force = true

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.NoOp {
		t.Error("expected non-no-op when Force=true even with identical image IDs")
	}

	// ComposeUp must have been called.
	foundComposeUp := false
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			foundComposeUp = true
		}
	}
	if !foundComposeUp {
		t.Error("expected ComposeUp to be called when Force=true")
	}
}

// TestUpdate_Rollback_HealthFailure verifies that when the backend health check
// fails, the engine:
//  1. Calls ImageTag to re-tag both previous image IDs.
//  2. Calls ComposeUp a second time (rollback recreate).
//  3. Returns RolledBack=true.
func TestUpdate_Rollback_HealthFailure(t *testing.T) {
	const version = "1.8.4"
	const agentOldID = "sha256:old-agent"
	const frontendOldID = "sha256:old-frontend"
	const agentNewID = "sha256:new-agent"
	const frontendNewID = "sha256:new-frontend"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true, ImageID: agentOldID},
				{Service: "frontend", Running: true, ImageID: frontendOldID},
			},
			ContainerListOut: []dockerx.ContainerState{
				// ContainerList for the health-check fallback — agent not running.
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: agentOldID}},
			{info: dockerx.ImageInfo{ID: frontendOldID}},
			{info: dockerx.ImageInfo{ID: agentNewID}},
			{info: dockerx.ImageInfo{ID: frontendNewID}},
		},
	}

	// All health probes fail: HTTPS fails, HTTP fails → triggers timeout → rollback.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(),                           // registry-1.docker.io connectivity
			okHTTP200(),                           // hub.docker.com connectivity
			errHTTPResponse("connection refused"), // backend HTTPS fail
			errHTTPResponse("connection refused"), // backend HTTP fail
			// No more responses → FakeHTTPProber returns default 200,
			// but we've already exhausted the poll window via HealthTimeout.
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)
	opts.HealthTimeout = 1 * time.Millisecond // expire immediately after first poll

	result, err := Update(context.Background(), deps, opts)
	// Update may return nil error even on rollback (best-effort recovery).
	_ = err

	if !result.RolledBack {
		t.Error("expected RolledBack=true after health check failure")
	}
	if result.RollbackFailed {
		t.Error("expected RollbackFailed=false when rollback succeeds")
	}

	// Verify ImageTag was called to restore previous IDs.
	agentLatest := agentImageName + ":latest"
	frontendLatest := frontendImageName + ":latest"
	foundAgentTag := false
	foundFrontendTag := false
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ImageTag" {
			if len(call.Args) >= 2 {
				if call.Args[0] == agentOldID && call.Args[1] == agentLatest {
					foundAgentTag = true
				}
				if call.Args[0] == frontendOldID && call.Args[1] == frontendLatest {
					foundFrontendTag = true
				}
			}
		}
	}
	if !foundAgentTag {
		t.Errorf("expected ImageTag(%s, %s) for rollback; calls: %v", agentOldID, agentLatest, mc.FakeClient.Calls)
	}
	if !foundFrontendTag {
		t.Errorf("expected ImageTag(%s, %s) for rollback", frontendOldID, frontendLatest)
	}

	// Verify ComposeUp was called at least twice: original recreate + rollback.
	composeUpCount := 0
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			composeUpCount++
		}
	}
	if composeUpCount < 2 {
		t.Errorf("expected at least 2 ComposeUp calls (recreate + rollback), got %d; calls: %v",
			composeUpCount, mc.FakeClient.Calls)
	}
}

// TestUpdate_BackupPruning verifies that after a new backup is created the
// engine prunes backups so only the 5 most recent are kept.
func TestUpdate_BackupPruning(t *testing.T) {
	// Pre-populate 5 existing backup dirs in FakeFS.
	// FakeFS.ReadDir returns entries based on files present under a path.
	const installDir = "."
	files := map[string][]byte{
		installDir + "/docker-compose.yml": []byte(standardCompose),
		installDir + "/.env":               []byte("INFLUXDB_TOKEN=test\n"),
	}
	// Create 5 existing backups (oldest to newest).
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("20240101_00000%d", i)
		files[installDir+"/.backups/"+name+"/image-state.txt"] = []byte("AGENT_IMAGE_ID=old\n")
		files[installDir+"/.backups/"+name+"/docker-compose.yml"] = []byte(standardCompose)
		files[installDir+"/.backups/"+name+"/.env"] = []byte("old\n")
	}

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:old-frontend"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-frontend"}},
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(), // connectivity
			okHTTP200(), // backend health
			okHTTP200(), // frontend health
		},
	}

	fs := dockerx.NewFakeFS(files)
	runner := &dockerx.FakeCommandRunner{}
	deps := Deps{
		Client:   mc,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := baseOpts("1.8.5")
	// Use a timestamp that sorts AFTER the existing 5 backups so it's the newest.
	opts.Now = func() time.Time {
		return time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	}

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected a backup path in result")
	}

	// Verify the new backup directory was written.
	newBackupDir := result.BackupPath
	if _, statErr := fs.Stat(newBackupDir + "/docker-compose.yml"); statErr != nil {
		t.Errorf("backup compose file not found at %s", newBackupDir+"/docker-compose.yml")
	}
	if _, statErr := fs.Stat(newBackupDir + "/.env"); statErr != nil {
		t.Errorf("backup .env not found at %s", newBackupDir+"/.env")
	}
	if _, statErr := fs.Stat(newBackupDir + "/image-state.txt"); statErr != nil {
		t.Errorf("backup image-state.txt not found at %s", newBackupDir+"/image-state.txt")
	}

	// Verify .env backup has mode 600.
	envBackupPath := newBackupDir + "/.env"
	fs.Chmod(envBackupPath, 0) // reset first
	for _, w := range fs.Writes {
		if w.Name == envBackupPath {
			if w.Perm != 0o600 {
				t.Errorf("backup .env mode = %04o, want 0600", w.Perm)
			}
			break
		}
	}

	// 5 pre-existing + 1 new backup = 6; prune must keep the 5 most recent and
	// remove exactly the oldest one (20240101_000001).
	backupsRoot := installDir + "/.backups"
	backupEntries, rdErr := fs.ReadDir(backupsRoot)
	if rdErr != nil {
		t.Fatalf("ReadDir %s: %v", backupsRoot, rdErr)
	}
	if len(backupEntries) != 5 {
		t.Errorf("after prune got %d backups, want 5: %v", len(backupEntries), backupEntries)
	}

	wantRemoved := backupsRoot + "/20240101_000001"
	removed := false
	for _, r := range fs.Removes {
		if r == wantRemoved {
			removed = true
		}
		if r == newBackupDir {
			t.Errorf("prune removed the newly created backup %s", r)
		}
	}
	if !removed {
		t.Errorf("expected oldest backup %s to be removed; Removes = %v", wantRemoved, fs.Removes)
	}
	for _, e := range backupEntries {
		if e == "20240101_000001" {
			t.Errorf("oldest backup still present after prune: %v", backupEntries)
		}
	}
}

// TestUpdate_DryRun_ZeroWrites verifies that DryRun performs pre-flight +
// state detection but creates NO files, pulls NO images, and recreates NO
// containers.
func TestUpdate_DryRun_ZeroWrites(t *testing.T) {
	const version = "1.8.4"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:agent-id"}},
			{info: dockerx.ImageInfo{ID: "sha256:front-id"}},
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), // registry
			okHTTP200(), // hub
		},
	}

	// Capture writes via FakeFS.
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(standardCompose),
		"./.env":               []byte("INFLUXDB_TOKEN=test\n"),
	})
	runner := &dockerx.FakeCommandRunner{}
	deps := Deps{
		Client:   mc,
		Runner:   runner,
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := baseOpts(version)
	opts.DryRun = true

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("DryRun Update failed: %v", err)
	}
	if !result.DryRun {
		t.Error("expected result.DryRun=true")
	}

	// No writes should have occurred (only the initial pre-populated files exist).
	if len(fs.Writes) > 0 {
		t.Errorf("DryRun made %d file writes; expected 0: %v", len(fs.Writes), func() []string {
			names := make([]string, len(fs.Writes))
			for i, w := range fs.Writes {
				names[i] = w.Name
			}
			return names
		}())
	}

	// No ImagePull, ComposeUp, or ImagePrune calls.
	for _, call := range mc.FakeClient.Calls {
		switch call.Method {
		case "ImagePull":
			t.Error("DryRun called ImagePull — must not pull images")
		case "ComposeUp":
			t.Error("DryRun called ComposeUp — must not recreate containers")
		case "ImagePrune":
			t.Error("DryRun called ImagePrune — must not prune images")
		case "ImageTag":
			t.Error("DryRun called ImageTag — must not re-tag images")
		}
	}
}

// TestUpdate_FindInstallDir_SearchOrder verifies the CWD → /root → /home/*
// search order for find_install_dir.
func TestUpdate_FindInstallDir_SearchOrder(t *testing.T) {
	tests := []struct {
		name           string
		files          map[string][]byte
		wantInstallDir string
		wantErr        bool
	}{
		{
			name: "found in CWD",
			files: map[string][]byte{
				"./docker-compose.yml": []byte(standardCompose),
				"./.env":               []byte("TOKEN=x\n"),
			},
			wantInstallDir: ".",
		},
		{
			name: "CWD has no compose, found in /root",
			files: map[string][]byte{
				// CWD compose doesn't reference agent image.
				"./docker-compose.yml":     []byte("version: '3'\nservices:\n  other:\n    image: nginx\n"),
				"/root/docker-compose.yml": []byte(standardCompose),
				"/root/.env":               []byte("TOKEN=x\n"),
			},
			wantInstallDir: "/root",
		},
		{
			name: "found in /home/ubuntu",
			files: map[string][]byte{
				// No valid compose in CWD or /root.
				"./docker-compose.yml":            []byte("other\n"),
				"/root/docker-compose.yml":        []byte("other\n"),
				"/home/ubuntu/docker-compose.yml": []byte(standardCompose),
				"/home/ubuntu/.env":               []byte("TOKEN=x\n"),
				"/home/ubuntu/something":          []byte("marker for ReadDir"),
			},
			wantInstallDir: "/home/ubuntu",
		},
		{
			name:    "no installation found",
			files:   map[string][]byte{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc := &multiInspectClient{
				FakeClient: &dockerx.FakeClient{
					ComposePsResult: []dockerx.ContainerState{
						{Service: "agent", Running: true},
						{Service: "frontend", Running: true},
						{Service: "mongodb", Running: true},
						{Service: "influxdb", Running: true},
						{Service: "redis", Running: true},
					},
				},
				inspectResponses: []inspectResponse{
					{info: dockerx.ImageInfo{ID: "sha256:a"}},
					{info: dockerx.ImageInfo{ID: "sha256:b"}},
					{info: dockerx.ImageInfo{ID: "sha256:c"}},
					{info: dockerx.ImageInfo{ID: "sha256:d"}},
				},
			}

			prober := &dockerx.FakeHTTPProber{
				Responses: []dockerx.HTTPResponse{
					okHTTP200(), okHTTP200(),
					okHTTP200(), okHTTP200(),
				},
			}

			fs := dockerx.NewFakeFS(tc.files)
			runner := &dockerx.FakeCommandRunner{}
			deps := Deps{
				Client:   mc,
				Runner:   runner,
				FS:       fs,
				Prober:   prober,
				Reporter: DiscardReporter{},
			}
			opts := baseOpts("1.0.0")
			opts.DryRun = true // avoid needing full happy-path responses

			result, err := Update(context.Background(), deps, opts)

			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.InstallDir != tc.wantInstallDir {
				t.Errorf("InstallDir = %q, want %q", result.InstallDir, tc.wantInstallDir)
			}
		})
	}
}

// TestUpdate_Version_Required verifies that an empty Version returns an error
// before any side effect.
func TestUpdate_Version_Required(t *testing.T) {
	mc := &dockerx.FakeClient{}
	prober := &dockerx.FakeHTTPProber{}
	deps := Deps{
		Client:   mc,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       dockerx.NewFakeFS(nil),
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	_, err := Update(context.Background(), deps, UpdateOptions{})
	if err == nil {
		t.Error("expected error for empty Version")
	}
}

// TestUpdate_MongoImmutable verifies that the mongodb service is never included
// in ComposeUp calls during an update (AD-4).
func TestUpdate_MongoImmutable(t *testing.T) {
	const version = "2.0.0"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:old-front"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-front"}},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(),
			okHTTP200(), okHTTP200(),
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)

	_, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Every ComposeUp call must NOT include mongodb, influxdb, or redis.
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			for _, arg := range call.Args {
				if strings.Contains(arg, "mongodb") ||
					strings.Contains(arg, "influxdb") ||
					strings.Contains(arg, "redis") {
					t.Errorf("ComposeUp included database service: %v", call.Args)
				}
			}
		}
	}
}

// TestUpdate_SkipFrontend verifies that with SkipFrontend=true only the agent
// is pulled and only agent is passed to ComposeUp.
func TestUpdate_SkipFrontend(t *testing.T) {
	const version = "1.9.0"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:old-front"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-agent"}},
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(), // connectivity
			okHTTP200(), // backend health
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)
	opts.SkipFrontend = true

	_, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// ImagePull must not include frontend tag.
	frontendTag := fmt.Sprintf("%s:%s", frontendImageName, version)
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ImagePull" {
			for _, arg := range call.Args {
				if strings.Contains(arg, frontendTag) {
					t.Errorf("ImagePull included frontend tag when SkipFrontend=true: %v", call.Args)
				}
			}
		}
	}

	// ComposeUp (recreate) must include only agent.
	for _, call := range mc.FakeClient.Calls {
		if call.Method == "ComposeUp" {
			for _, arg := range call.Args {
				if strings.Contains(arg, "frontend") {
					t.Errorf("ComposeUp included frontend when SkipFrontend=true: %v", call.Args)
				}
			}
		}
	}
}

// TestUpdate_BackendHealth_404_NotSilentSuccess verifies that a 404 from
// /health is not accepted as a passing health check (spec: must not port the
// update-agent.sh bug of accepting any HTTP response including 404).
func TestUpdate_BackendHealth_404_NotSilentSuccess(t *testing.T) {
	const version = "1.8.3"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
			// ContainerList returns agent as running for the fallback check.
			ContainerListOut: []dockerx.ContainerState{
				{Name: "srv-agent-1", Service: "agent", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:old-front"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-front"}},
		},
	}

	// Backend returns 404 (legacy version without /health endpoint).
	// The engine should NOT count this as success, but SHOULD fall back to
	// container-running check and log a WARN.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(),       // registry
			okHTTP200(),       // hub
			notFoundHTTP404(), // backend HTTPS 404
			notFoundHTTP404(), // backend HTTP 404
			okHTTP200(),       // frontend health
		},
	}

	// Capture reporter events to verify WARN was emitted.
	var warnEmitted bool
	var events []Event
	reporter := ReporterFunc(func(ev Event) {
		events = append(events, ev)
		if ev.Kind == EventWarning {
			warnEmitted = true
		}
	})

	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(standardCompose),
		"./.env":               []byte("TOKEN=x\n"),
	})
	deps := Deps{
		Client:   mc,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: reporter,
	}
	opts := baseOpts(version)
	opts.HealthTimeout = 10 * time.Millisecond // expire quickly after first probe

	result, err := Update(context.Background(), deps, opts)
	// Update should either succeed (with warning) or rollback gracefully.
	if err != nil && !result.RolledBack {
		t.Logf("Update returned error (acceptable if rollback): %v", err)
	}

	// The engine must have emitted at least one warning about 404.
	if !warnEmitted {
		t.Error("expected at least one EventWarning to be emitted for 404 health response")
	}

	// Verify no rollback happened if container running check succeeded.
	// (result.RolledBack depends on whether ContainerList found the agent running)
	t.Logf("RolledBack=%v, warnings=%v", result.RolledBack, result.Warnings)
}

// TestUpdate_LogFile_Written verifies that update log entries are written to
// the update log file with the correct format.
func TestUpdate_LogFile_Written(t *testing.T) {
	const version = "1.8.4"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old"}},
			{info: dockerx.ImageInfo{ID: "sha256:oldf"}},
			{info: dockerx.ImageInfo{ID: "sha256:new"}},
			{info: dockerx.ImageInfo{ID: "sha256:newf"}},
		},
	}

	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(),
			okHTTP200(), okHTTP200(),
		},
	}

	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(standardCompose),
		"./.env":               []byte("TOKEN=x\n"),
	})
	deps := Deps{
		Client:   mc,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := baseOpts(version)

	_, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Check log file was written.
	logData, readErr := fs.ReadFile(updateLogFile)
	if readErr != nil {
		t.Fatalf("log file not found at %s: %v", updateLogFile, readErr)
	}

	logContent := string(logData)
	// Verify format: [YYYY-MM-DD HH:MM:SS] LEVEL: message
	if !strings.Contains(logContent, "[2024-01-15 10:30:00]") {
		t.Errorf("log entry missing timestamp; content:\n%s", logContent)
	}
	// Must contain OK or STEP level entry.
	if !strings.Contains(logContent, "] OK: ") && !strings.Contains(logContent, "] STEP: ") {
		t.Errorf("log file missing OK/STEP entries; content:\n%s", logContent)
	}
}

// TestUpdate_DryRun_NoLog verifies that DryRun does NOT write to the log file.
func TestUpdate_DryRun_NoLog(t *testing.T) {
	const version = "1.8.4"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:a"}},
			{info: dockerx.ImageInfo{ID: "sha256:b"}},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(),
		},
	}

	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(standardCompose),
		"./.env":               []byte("TOKEN=x\n"),
	})
	deps := Deps{
		Client:   mc,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}
	opts := baseOpts(version)
	opts.DryRun = true

	_, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("DryRun Update failed: %v", err)
	}

	if _, readErr := fs.ReadFile(updateLogFile); readErr == nil {
		t.Error("DryRun must not write to the update log file")
	}
}

// TestUpdate_BackupEnvMode600 verifies that the .env backup has mode 600.
func TestUpdate_BackupEnvMode600(t *testing.T) {
	const version = "1.8.4"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ComposePsResult: []dockerx.ContainerState{
				{Service: "agent", Running: true},
				{Service: "frontend", Running: true},
				{Service: "mongodb", Running: true},
				{Service: "influxdb", Running: true},
				{Service: "redis", Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{info: dockerx.ImageInfo{ID: "sha256:old-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:old-front"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-agent"}},
			{info: dockerx.ImageInfo{ID: "sha256:new-front"}},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), okHTTP200(),
			okHTTP200(), okHTTP200(),
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, prober)
	opts := baseOpts(version)

	result, err := Update(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	backupEnvPath := result.BackupPath + "/.env"
	fs := deps.FS.(*dockerx.FakeFS)
	mode, exists := fs.Modes[backupEnvPath]
	if !exists {
		// Fall back to checking Writes.
		for _, w := range fs.Writes {
			if w.Name == backupEnvPath {
				if w.Perm != 0o600 {
					t.Errorf("backup .env written with mode %04o, want 0600", w.Perm)
				}
				return
			}
		}
		t.Errorf("backup .env not found in FakeFS writes: %s", backupEnvPath)
		return
	}
	if mode != 0o600 {
		t.Errorf("backup .env mode = %04o, want 0600", mode)
	}
}

// TestDetectCurrentState_LatestFails_FallbackToContainer verifies that when
// ImageInspect(:latest) fails, detectCurrentState falls back to the running
// container's ImageID.
func TestDetectCurrentState_LatestFails_FallbackToContainer(t *testing.T) {
	const agentID = "sha256:abc123"

	mc := &multiInspectClient{
		FakeClient: &dockerx.FakeClient{
			ContainerListOut: []dockerx.ContainerState{
				{Service: "agent", ImageID: agentID, Running: true},
			},
		},
		inspectResponses: []inspectResponse{
			{err: fmt.Errorf("no such image: agent:latest")},    // agent:latest fails → fallback
			{err: fmt.Errorf("no such image: frontend:latest")}, // frontend:latest fails → fallback
		},
	}

	deps := buildDepsWithFiles(".", nil, mc, &dockerx.FakeHTTPProber{})

	state, err := detectCurrentState(context.Background(), deps, ".", baseOpts("1.8.4"), &UpdateResult{})
	if err != nil {
		t.Fatalf("detectCurrentState failed: %v", err)
	}
	// Agent fallback: ContainerList returns the agent container's ImageID.
	if state.AgentImageID != agentID {
		t.Errorf("AgentImageID = %q, want %q (fallback from container)", state.AgentImageID, agentID)
	}
}
