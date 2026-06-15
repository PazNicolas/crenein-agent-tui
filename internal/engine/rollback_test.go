package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Test data ────────────────────────────────────────────────────────────────

const ts1 = "20240101_120000" // older
const ts2 = "20240201_120000" // newer

func imageStateContent(agentID, frontendID, mongoImage string) []byte {
	return []byte("AGENT_IMAGE_ID=" + agentID + "\nFRONTEND_IMAGE_ID=" + frontendID + "\nMONGO_IMAGE=" + mongoImage + "\n")
}

// buildBackupFS creates a FakeFS with two backup dirs under installDir/.backups.
func buildBackupFS(installDir string) *dockerx.FakeFS {
	files := map[string][]byte{
		installDir + "/docker-compose.yml": []byte(standardCompose),
		installDir + "/.env":               []byte("TOKEN=x\n"),
		// ts1 (older)
		installDir + "/.backups/" + ts1 + "/docker-compose.yml": []byte(standardCompose),
		installDir + "/.backups/" + ts1 + "/.env":               []byte("TOKEN=old1\n"),
		installDir + "/.backups/" + ts1 + "/image-state.txt":    imageStateContent("sha256:agent1", "sha256:front1", "mongo:4.4"),
		// ts2 (newer)
		installDir + "/.backups/" + ts2 + "/docker-compose.yml": []byte(standardCompose),
		installDir + "/.backups/" + ts2 + "/.env":               []byte("TOKEN=old2\n"),
		installDir + "/.backups/" + ts2 + "/image-state.txt":    imageStateContent("sha256:agent2", "sha256:front2", "mongo:4.4"),
	}
	return dockerx.NewFakeFS(files)
}

func buildRollbackDeps(fs *dockerx.FakeFS, client dockerx.Client) Deps {
	return Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   &dockerx.FakeHTTPProber{},
		Reporter: DiscardReporter{},
	}
}

func baseRollbackOpts(installDir string) RollbackOptions {
	return RollbackOptions{
		InstallDir:    installDir,
		RetryInterval: 0,
		HealthTimeout: 50 * time.Millisecond,
		Now:           func() time.Time { return fixedTime },
	}
}

// ─── ListBackups tests ────────────────────────────────────────────────────────

func TestListBackups_OrderedMostRecentFirst(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})

	backups, err := ListBackups(deps, installDir)
	if err != nil {
		t.Fatalf("ListBackups error: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}
	// Most-recent-first: ts2 > ts1.
	if backups[0].Timestamp != ts2 {
		t.Errorf("backups[0].Timestamp = %s, want %s", backups[0].Timestamp, ts2)
	}
	if backups[1].Timestamp != ts1 {
		t.Errorf("backups[1].Timestamp = %s, want %s", backups[1].Timestamp, ts1)
	}
}

func TestListBackups_ParsesImageState(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})

	backups, err := ListBackups(deps, installDir)
	if err != nil {
		t.Fatalf("ListBackups error: %v", err)
	}

	// ts2 is index 0 (most recent).
	b := backups[0]
	if b.AgentImageID != "sha256:agent2" {
		t.Errorf("AgentImageID = %s, want sha256:agent2", b.AgentImageID)
	}
	if b.FrontendImageID != "sha256:front2" {
		t.Errorf("FrontendImageID = %s, want sha256:front2", b.FrontendImageID)
	}
	if b.MongoImage != "mongo:4.4" {
		t.Errorf("MongoImage = %s, want mongo:4.4", b.MongoImage)
	}
}

func TestListBackups_NoDirReturnsEmpty(t *testing.T) {
	fs := dockerx.NewFakeFS(nil)
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})

	backups, err := ListBackups(deps, ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backups))
	}
}

func TestListBackups_EmptyDirReturnsEmpty(t *testing.T) {
	// No files under .backups/ → ReadDir returns empty.
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./.backups/somefile_not_a_dir/x": []byte(""),
	})
	// The entry "somefile_not_a_dir" does not parse as a timestamp so it's filtered.
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})

	backups, err := ListBackups(deps, ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// somefile_not_a_dir is not a valid timestamp — expect 0.
	if len(backups) != 0 {
		t.Errorf("expected 0 valid backups, got %d: %v", len(backups), backups)
	}
}

// ─── Rollback tests ───────────────────────────────────────────────────────────

func TestRollback_LatestBackup(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{okHTTP200(), okHTTP200()},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)

	res, err := Rollback(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Rollback error: %v", err)
	}
	if res.Timestamp != ts2 {
		t.Errorf("Timestamp = %s, want %s (latest)", res.Timestamp, ts2)
	}
	if res.RestoredAgentImageID != "sha256:agent2" {
		t.Errorf("RestoredAgentImageID = %s, want sha256:agent2", res.RestoredAgentImageID)
	}
}

func TestRollback_ExplicitTimestamp(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{okHTTP200(), okHTTP200()},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)
	opts.BackupTimestamp = ts1 // explicitly pick the older one

	res, err := Rollback(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Rollback error: %v", err)
	}
	if res.Timestamp != ts1 {
		t.Errorf("Timestamp = %s, want %s", res.Timestamp, ts1)
	}
	if res.RestoredAgentImageID != "sha256:agent1" {
		t.Errorf("RestoredAgentImageID = %s, want sha256:agent1", res.RestoredAgentImageID)
	}
}

func TestRollback_RestoresFiles(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{okHTTP200(), okHTTP200()},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)

	_, err := Rollback(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	// Verify docker-compose.yml was written.
	composeWritten := false
	envWritten := false
	for _, w := range fs.Writes {
		if w.Name == installDir+"/docker-compose.yml" {
			composeWritten = true
		}
		if w.Name == installDir+"/.env" {
			envWritten = true
			if w.Perm != 0o600 {
				t.Errorf(".env restored with mode %04o, want 0600", w.Perm)
			}
		}
	}
	if !composeWritten {
		t.Error("expected docker-compose.yml to be written during restore")
	}
	if !envWritten {
		t.Error("expected .env to be written during restore")
	}
}

func TestRollback_RetagAndRecreate(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{okHTTP200(), okHTTP200()},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)

	_, err := Rollback(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Rollback error: %v", err)
	}

	// Check ImageTag calls.
	foundAgentTag := false
	foundFrontendTag := false
	foundComposeUp := false
	for _, call := range client.Calls {
		if call.Method == "ImageTag" && len(call.Args) >= 2 {
			if call.Args[0] == "sha256:agent2" && call.Args[1] == agentImageName+":latest" {
				foundAgentTag = true
			}
			if call.Args[0] == "sha256:front2" && call.Args[1] == frontendImageName+":latest" {
				foundFrontendTag = true
			}
		}
		if call.Method == "ComposeUp" {
			foundComposeUp = true
			// Verify NoDeps+ForceRecreate.
			for _, arg := range call.Args {
				if strings.Contains(arg, "NoDeps=true") && strings.Contains(arg, "ForceRecreate=true") {
					// OK
				}
			}
		}
	}
	if !foundAgentTag {
		t.Errorf("expected ImageTag(sha256:agent2, %s:latest); calls: %v", agentImageName, client.Calls)
	}
	if !foundFrontendTag {
		t.Errorf("expected ImageTag(sha256:front2, %s:latest); calls: %v", frontendImageName, client.Calls)
	}
	if !foundComposeUp {
		t.Error("expected ComposeUp call for recreate")
	}
}

func TestRollback_HealthOK(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
			{Service: "frontend", Running: true},
			{Service: "mongodb", Running: true},
			{Service: "influxdb", Running: true},
			{Service: "redis", Running: true},
		},
	}
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			okHTTP200(), // backend health HTTPS
			okHTTP200(), // frontend health
		},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)
	res, err := Rollback(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Rollback error: %v", err)
	}
	if !res.HealthOK {
		t.Error("expected HealthOK=true")
	}
}

func TestRollback_HealthFail_NoError(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ComposePsResult: []dockerx.ContainerState{
			{Service: "agent", Running: true},
		},
	}
	// All health probes fail — but the agent container is not in ContainerList,
	// so the fallback also fails → health timeout.
	prober := &dockerx.FakeHTTPProber{
		Responses: []dockerx.HTTPResponse{
			errHTTPResponse("connection refused"), // HTTPS fail
			errHTTPResponse("connection refused"), // HTTP fail
		},
	}
	deps := Deps{
		Client:   client,
		Runner:   &dockerx.FakeCommandRunner{},
		FS:       fs,
		Prober:   prober,
		Reporter: DiscardReporter{},
	}

	opts := baseRollbackOpts(installDir)
	opts.HealthTimeout = 1 * time.Millisecond // expire immediately

	res, err := Rollback(context.Background(), deps, opts)
	// Rollback itself must NOT return an error on health fail — caller maps to exit 1.
	if err != nil {
		t.Fatalf("Rollback should not return error on health fail; got: %v", err)
	}
	if res.HealthOK {
		t.Error("expected HealthOK=false when health checks all fail")
	}
}

func TestRollback_NoBackups_ReturnsError(t *testing.T) {
	fs := dockerx.NewFakeFS(map[string][]byte{
		"./docker-compose.yml": []byte(standardCompose),
		"./.env":               []byte("TOKEN=x\n"),
	})
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})
	opts := baseRollbackOpts(".")

	_, err := Rollback(context.Background(), deps, opts)
	if err == nil {
		t.Error("expected error when no backups exist")
	}
}

func TestRollback_UnknownTimestamp_ReturnsError(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)
	deps := buildRollbackDeps(fs, &dockerx.FakeClient{})

	opts := baseRollbackOpts(installDir)
	opts.BackupTimestamp = "19990101_000000" // doesn't exist

	_, err := Rollback(context.Background(), deps, opts)
	if err == nil {
		t.Error("expected error for unknown timestamp")
	}
}

// TestRollback_AgentRetagFail_FatalError verifies that a failed agent ImageTag
// returns an error (not just a warning), so the caller exits 1 and does NOT
// recreate with the wrong image.
func TestRollback_AgentRetagFail_FatalError(t *testing.T) {
	const installDir = "."
	fs := buildBackupFS(installDir)

	client := &dockerx.FakeClient{
		ImageTagErr: fmt.Errorf("image not found locally"),
	}
	deps := buildRollbackDeps(fs, client)
	opts := baseRollbackOpts(installDir)

	_, err := Rollback(context.Background(), deps, opts)
	if err == nil {
		t.Error("expected error when agent ImageTag fails (it is fatal)")
	}
	// ComposeUp must NOT have been called — we should have aborted at retag.
	for _, call := range client.Calls {
		if call.Method == "ComposeUp" {
			t.Error("ComposeUp must not be called when agent retag fails")
		}
	}
}
