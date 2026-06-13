package compose_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/compose"
)

// goldenPath returns the path to the golden file for the given test name.
func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name+".golden.yml")
}

// readGolden reads the golden file, creating it with the given content if it
// does not yet exist (update mode). Set the UPDATE_GOLDEN environment variable
// to force regeneration of existing golden files.
func readGolden(t *testing.T, name string, got []byte) []byte {
	t.Helper()
	path := goldenPath(t, name)
	if os.Getenv("UPDATE_GOLDEN") != "" || !fileExists(path) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdirall testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return got
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	return data
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// AD-5: no hardcoded InfluxDB token (a 64-char hex secret) may appear in the
// rendered compose — the template must only carry ${VAR} references. We detect
// any such token generically rather than embedding the legacy secret value in
// the repo. Note: "adminpassword" is the fixed InfluxDB admin UI password kept
// as-is per the spec (open question, flagged for future hardening); it appears
// intentionally as a literal in DOCKER_INFLUXDB_INIT_PASSWORD and is allowed.
var hex64Token = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

func TestRender_Mongo70(t *testing.T) {
	t.Parallel()
	params := compose.ComposeParams{
		MongoImage: "mongodb/mongodb-community-server:7.0-ubuntu2204",
	}
	got, err := compose.Render(params)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	// Golden-file comparison.
	want := readGolden(t, "mongo70", got)
	if !bytes.Equal(got, want) {
		t.Errorf("rendered compose does not match golden file %s\ngot:\n%s\nwant:\n%s",
			goldenPath(t, "mongo70"), got, want)
	}

	assertNoCredentials(t, got)
	assertContains(t, got, "mongodb/mongodb-community-server:7.0-ubuntu2204")
	assertComposeContract(t, got)
}

func TestRender_Mongo44(t *testing.T) {
	t.Parallel()
	params := compose.ComposeParams{
		MongoImage: "mongo:4.4",
	}
	got, err := compose.Render(params)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}

	want := readGolden(t, "mongo44", got)
	if !bytes.Equal(got, want) {
		t.Errorf("rendered compose does not match golden file %s\ngot:\n%s\nwant:\n%s",
			goldenPath(t, "mongo44"), got, want)
	}

	assertNoCredentials(t, got)
	assertContains(t, got, "mongo:4.4")
	assertComposeContract(t, got)
}

func TestRender_EmptyMongoImage_Error(t *testing.T) {
	t.Parallel()
	_, err := compose.Render(compose.ComposeParams{})
	if err == nil {
		t.Fatal("expected error for empty MongoImage")
	}
}

func TestEmbeddedDefaults(t *testing.T) {
	t.Parallel()
	if len(compose.DefaultVsftpdConf) == 0 {
		t.Error("DefaultVsftpdConf is empty")
	}
	if len(compose.DefaultTftpdHpa) == 0 {
		t.Error("DefaultTftpdHpa is empty")
	}
	if !bytes.Contains(compose.DefaultVsftpdConf, []byte("vsftpd")) {
		t.Error("DefaultVsftpdConf does not look like a vsftpd config")
	}
	if !bytes.Contains(compose.DefaultTftpdHpa, []byte("TFTP_DIRECTORY")) {
		t.Error("DefaultTftpdHpa does not contain TFTP_DIRECTORY")
	}
	if !bytes.Contains(compose.DefaultTftpdHpa, []byte("/data/files")) {
		t.Error("DefaultTftpdHpa does not reference /data/files")
	}
}

// ─── assertion helpers ───────────────────────────────────────────────────────

func assertNoCredentials(t *testing.T, data []byte) {
	t.Helper()
	if m := hex64Token.Find(data); m != nil {
		t.Errorf("rendered compose contains a hardcoded 64-char hex token %q; only ${VAR} references are allowed", m)
	}
}

func assertContains(t *testing.T, data []byte, needle string) {
	t.Helper()
	if !bytes.Contains(data, []byte(needle)) {
		t.Errorf("rendered compose does not contain %q", needle)
	}
}

// assertComposeContract validates structural invariants from the spec:
//   - version: '3.8'
//   - network agent-network
//   - named volumes mongodb_data, influxdb_data, redis_data
//   - services: agent, frontend, mongodb, influxdb, redis
//   - MongoDB port 27017 NOT mapped to host
//   - Redis port 6379 NOT mapped to host
//   - credentials appear only as ${VAR} references
func assertComposeContract(t *testing.T, data []byte) {
	t.Helper()
	content := string(data)

	required := []string{
		"version: '3.8'",
		"agent-network",
		"mongodb_data:",
		"influxdb_data:",
		"redis_data:",
		"agent:",
		"frontend:",
		"mongodb:",
		"influxdb:",
		"redis:",
		"influxdb:2.7",
		"redis:7-alpine",
		"crenein/c-network-agent-back:latest",
		"crenein/c-network-agent-front:latest",
		// Credential references (${VAR} form, not literal values).
		"${MONGODB_INITDB_ROOT_USERNAME}",
		"${MONGODB_INITDB_ROOT_PASSWORD}",
		"${REDIS_PASSWORD}",
		"${INFLUXDB_TOKEN}",
		// Ports that must be present.
		`"8000:8000"`,
		`"8443:8443"`,
		`"80:80"`,
		`"443:443"`,
		`"8086:8086"`,
	}
	for _, req := range required {
		if !strings.Contains(content, req) {
			t.Errorf("compose output missing required element: %q", req)
		}
	}

	// MongoDB and Redis must NOT expose host ports.
	forbidden := []string{
		`"27017:27017"`,
		`"6379:6379"`,
	}
	for _, f := range forbidden {
		if strings.Contains(content, f) {
			t.Errorf("compose output must not expose port mapping %q", f)
		}
	}
}
