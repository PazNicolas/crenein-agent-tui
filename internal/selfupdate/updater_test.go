package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
)

// binaryDir is the fake directory where the binary lives in tests.
const binaryDir = "/usr/local/bin"
const binaryPath = "/usr/local/bin/crenein-agent"

// assetName is the fake asset filename used in tests.
const assetName = "crenein-agent-tui_linux_amd64"

// assetContent is the content of the "downloaded" binary in tests.
var assetContent = []byte("fake-binary-v0.2.0")

// sha256Hex computes the hex-encoded SHA256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// checksumsFor returns a checksums.txt body with the given name and hash.
func checksumsFor(name string, data []byte) string {
	return fmt.Sprintf("%s  %s\n", sha256Hex(data), name)
}

// buildUpdater constructs an Updater with all fake deps.
func buildUpdater(
	t *testing.T,
	fops *fakeFileOps,
	resolver *fakeExecutableResolver,
	source *fakeReleaseSource,
	prober *fakeHTTPProber,
) *Updater {
	t.Helper()
	u := &Updater{
		prober:   prober,
		fileOps:  fops,
		resolver: resolver,
		source:   source,
		GOOS:     "linux",
		GOARCH:   "amd64",
	}
	return u
}

// TestUpdate_HappyPath verifies that a newer version is downloaded, verified,
// and atomically installed.
func TestUpdate_HappyPath(t *testing.T) {
	// Set up fake filesystem with the current binary.
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: []byte("fake-binary-v0.1.0"),
	})

	// HTTP responses: first checksums.txt, then the asset.
	prober := &fakeHTTPProber{}
	prober.addResponse(200, checksumsFor(assetName, assetContent))
	prober.addResponse(200, string(assetContent))

	// Release source resolves v0.2.0.
	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}

	resolver := &fakeExecutableResolver{path: binaryPath}

	u := buildUpdater(t, fops, resolver, source, prober)

	// Install the fake hash lookup.
	installFakeHasher()
	defer restoreRealHasher()

	ctx := context.Background()

	// We need to make openForHash return the asset content for the temp file.
	// The temp file path will be set after WriteTemp is called. We set it lazily
	// by overriding openForHash to look up in fops.
	openForHash = func(path string) (io.ReadCloser, error) {
		data, ok := fops.readFile(path)
		if !ok {
			return nil, fmt.Errorf("openForHash: no file at %s", path)
		}
		return ioReadCloserFromBytes(data), nil
	}

	result, err := u.Update(ctx, "v0.1.0", "v0.2.0", false)
	if err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	if result.Action != "updated" {
		t.Errorf("expected action=updated, got %q", result.Action)
	}
	if result.FromVersion != "v0.1.0" {
		t.Errorf("FromVersion = %q, want v0.1.0", result.FromVersion)
	}
	if result.ToVersion != "v0.2.0" {
		t.Errorf("ToVersion = %q, want v0.2.0", result.ToVersion)
	}
	// Binary must have been replaced.
	newData, ok := fops.readFile(binaryPath)
	if !ok {
		t.Fatal("binary not found after update")
	}
	if string(newData) != string(assetContent) {
		t.Errorf("binary content = %q, want %q", newData, assetContent)
	}
	// Mode must be 0755.
	if len(fops.chmods) == 0 {
		t.Error("chmod was never called")
	} else if fops.chmods[0].mode != 0o755 {
		t.Errorf("chmod mode = %o, want 0755", fops.chmods[0].mode)
	}
	// No temp file residual.
	for _, p := range fops.writeTmps {
		if fops.hasFile(p) {
			t.Errorf("temp file %s still exists after successful update", p)
		}
	}
}

// TestUpdate_ChecksumMismatch verifies that a hash mismatch aborts without
// touching the target binary, and removes the temp file.
func TestUpdate_ChecksumMismatch(t *testing.T) {
	original := []byte("fake-binary-v0.1.0")
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: original,
	})

	// Checksums.txt contains the correct hash of assetContent, but the HTTP
	// download returns corrupted bytes.
	corruptContent := []byte("corrupted-data")
	prober := &fakeHTTPProber{}
	prober.addResponse(200, checksumsFor(assetName, assetContent)) // correct checksum
	prober.addResponse(200, string(corruptContent))                // wrong content

	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}
	resolver := &fakeExecutableResolver{path: binaryPath}
	u := buildUpdater(t, fops, resolver, source, prober)

	openForHash = func(path string) (io.ReadCloser, error) {
		data, ok := fops.readFile(path)
		if !ok {
			return nil, fmt.Errorf("openForHash: no file at %s", path)
		}
		return ioReadCloserFromBytes(data), nil
	}
	defer restoreRealHasher()

	_, err := u.Update(context.Background(), "v0.1.0", "v0.2.0", false)
	if err == nil {
		t.Fatal("expected error on checksum mismatch, got nil")
	}
	if !containsString(err.Error(), "mismatch") {
		t.Errorf("error should mention 'mismatch', got: %v", err)
	}

	// Binary must be byte-identical to original.
	data, ok := fops.readFile(binaryPath)
	if !ok {
		t.Fatal("original binary disappeared")
	}
	if string(data) != string(original) {
		t.Error("binary was overwritten despite checksum mismatch")
	}

	// Temp file must have been removed.
	for _, p := range fops.writeTmps {
		if fops.hasFile(p) {
			t.Errorf("temp file %s still exists after checksum failure", p)
		}
	}
}

// TestUpdate_CtxCancelDuringDownload verifies that cancelling the context
// during download leaves no temp residual.
func TestUpdate_CtxCancelDuringDownload(t *testing.T) {
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: []byte("fake-binary-v0.1.0"),
	})

	// The prober returns an error for the asset download (simulates ctx cancel).
	prober := &fakeHTTPProber{}
	prober.addResponse(200, checksumsFor(assetName, assetContent)) // checksums OK
	prober.addError(errors.New("context canceled"))                // asset download fails

	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}
	resolver := &fakeExecutableResolver{path: binaryPath}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	u := buildUpdater(t, fops, resolver, source, prober)

	installFakeHasher()
	defer restoreRealHasher()

	_, err := u.Update(ctx, "v0.1.0", "v0.2.0", false)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}

	// No temp file residual.
	for _, p := range fops.writeTmps {
		if fops.hasFile(p) {
			t.Errorf("temp file %s still exists after interrupted download", p)
		}
	}
}

// TestUpdate_WriteProbePermissionDenied verifies that a write-probe failure
// returns a cnerr with a "sudo" fix suggestion before any download.
func TestUpdate_WriteProbePermissionDenied(t *testing.T) {
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: []byte("fake-binary-v0.1.0"),
	})
	fops.probeErr = errors.New("permission denied")

	prober := &fakeHTTPProber{}
	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}
	resolver := &fakeExecutableResolver{path: binaryPath}
	u := buildUpdater(t, fops, resolver, source, prober)

	_, err := u.Update(context.Background(), "v0.1.0", "v0.2.0", false)
	if err == nil {
		t.Fatal("expected error for permission denied, got nil")
	}

	var cnErr *cnerr.Error
	if !errors.As(err, &cnErr) {
		t.Fatalf("expected *cnerr.Error, got %T: %v", err, err)
	}
	if !containsString(cnErr.FixSuggestion, "sudo") {
		t.Errorf("FixSuggestion should contain 'sudo', got %q", cnErr.FixSuggestion)
	}

	// No HTTP calls should have been made (early fail).
	if len(prober.requests) > 0 {
		t.Errorf("HTTP requests were made despite permission denied: %v", prober.requests)
	}
}

// TestUpdate_DowngradePin verifies that an explicit older version is installed
// when allowDowngrade=true.
func TestUpdate_DowngradePin(t *testing.T) {
	downgradeContent := []byte("fake-binary-v0.1.0")
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: []byte("fake-binary-v0.2.0"),
	})

	prober := &fakeHTTPProber{}
	prober.addResponse(200, checksumsFor(assetName, downgradeContent))
	prober.addResponse(200, string(downgradeContent))

	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}
	resolver := &fakeExecutableResolver{path: binaryPath}
	u := buildUpdater(t, fops, resolver, source, prober)

	openForHash = func(path string) (io.ReadCloser, error) {
		data, ok := fops.readFile(path)
		if !ok {
			return nil, fmt.Errorf("openForHash: no file at %s", path)
		}
		return ioReadCloserFromBytes(data), nil
	}
	defer restoreRealHasher()

	result, err := u.Update(context.Background(), "v0.2.0", "v0.1.0", true)
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if result.Action != "downgraded" {
		t.Errorf("expected action=downgraded, got %q", result.Action)
	}

	newData, ok := fops.readFile(binaryPath)
	if !ok {
		t.Fatal("binary not found after downgrade")
	}
	if string(newData) != string(downgradeContent) {
		t.Errorf("binary content = %q, want %q", newData, downgradeContent)
	}
}

// TestUpdate_SameVersionNoOp verifies that an identical version returns no-op
// without any I/O beyond resolving the asset.
func TestUpdate_SameVersionNoOp(t *testing.T) {
	fops := newFakeFileOps(map[string][]byte{
		binaryPath: []byte("fake-binary-v0.1.0"),
	})
	prober := &fakeHTTPProber{}
	source := &fakeReleaseSource{
		asset: ReleaseAsset{
			Name:         assetName,
			DownloadURL:  "http://example.com/asset",
			ChecksumsURL: "http://example.com/checksums.txt",
		},
	}
	resolver := &fakeExecutableResolver{path: binaryPath}
	u := buildUpdater(t, fops, resolver, source, prober)

	result, err := u.Update(context.Background(), "v0.1.0", "v0.1.0", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Action != "no-op" {
		t.Errorf("expected action=no-op, got %q", result.Action)
	}

	// No file operations performed.
	if len(fops.writeTmps) > 0 {
		t.Error("WriteTemp should not have been called for no-op")
	}
	if len(fops.renames) > 0 {
		t.Error("Rename should not have been called for no-op")
	}
	// No HTTP calls beyond what the source makes.
	if len(prober.requests) > 0 {
		t.Errorf("unexpected HTTP requests: %v", prober.requests)
	}
}

// ─── compareSemver unit tests ────────────────────────────────────────────────

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.1.0", 1},
		{"v0.1.0", "v0.1.0", 0},
		{"0.1.0", "v0.1.0", 0},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.1.10", "v0.1.9", 1},
		{"v0.1.0", "v0.1.0", 0},
		{"v2.0.0", "v1.9.9", 1},
	}
	for _, tc := range cases {
		got := compareSemver(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ioReadCloserFromBytes wraps a []byte in an io.ReadCloser.
func ioReadCloserFromBytes(data []byte) io.ReadCloser {
	return io.NopCloser(newBytesReader(data))
}

// newBytesReader returns a minimal bytes reader.
func newBytesReader(data []byte) *bytesReader {
	return &bytesReader{data: data}
}

type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
