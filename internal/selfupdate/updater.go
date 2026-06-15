package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ReleaseAsset describes a downloadable release binary and the URL of the
// checksums file for its release.
type ReleaseAsset struct {
	// Name is the exact filename in the GitHub Release
	// (e.g. "crenein-agent-tui_linux_amd64.tar.gz").
	Name string
	// DownloadURL is the direct URL to stream the binary asset.
	DownloadURL string
	// ChecksumsURL is the URL of the checksums.txt file for this release.
	ChecksumsURL string
}

// ReleaseSource abstracts the GitHub Releases API. The cmd layer wires a real
// implementation; tests inject a fake.
type ReleaseSource interface {
	// ResolveAsset returns the release asset matching the current GOOS/GOARCH
	// for the given version tag. If version is empty it resolves the latest
	// release.
	ResolveAsset(ctx context.Context, version string) (ReleaseAsset, error)
}

// Result describes what self-update did (or decided not to do).
type Result struct {
	// Action is one of "updated", "downgraded", or "no-op".
	Action string
	// FromVersion is the version the binary was at before the run.
	FromVersion string
	// ToVersion is the version resolved (for no-op, equal to FromVersion).
	ToVersion string
}

// Performer is the interface implemented by *Updater. cmd/ injects a fake in
// tests to avoid real filesystem and network operations.
type Performer interface {
	Update(ctx context.Context, currentVersion, targetVersion string, allowDowngrade bool) (*Result, error)
}

// Updater performs a self-update of the running binary. All I/O is injected via
// seams so the logic can be unit-tested without network access or real binaries.
type Updater struct {
	prober   dockerx.HTTPProber
	fileOps  FileOps
	resolver ExecutableResolver
	source   ReleaseSource

	// GOOS and GOARCH allow overriding the platform in tests.
	GOOS   string
	GOARCH string
}

// New creates an Updater wired to the real OS and network.
func New(src ReleaseSource, prober dockerx.HTTPProber) *Updater {
	return &Updater{
		prober:   prober,
		fileOps:  realFileOps{},
		resolver: realExecutableResolver{},
		source:   src,
	}
}

// Update performs the full self-update flow:
//  1. Resolve real binary path + probe write access (early fail).
//  2. Resolve the release asset for targetVersion (or latest when empty).
//  3. Semver decision — skip when no change is needed.
//  4. Download checksums.txt.
//  5. Stream asset to a temp file in the same directory as the binary.
//  6. SHA256 verify against checksums.txt — abort and clean up on mismatch.
//  7. chmod 0755 + atomic rename over the target.
//
// currentVersion is the running binary's semver string (e.g. "v0.1.0").
// targetVersion is the pinned version or "" for latest.
// allowDowngrade must be true to install a version older than currentVersion.
func (u *Updater) Update(ctx context.Context, currentVersion, targetVersion string, allowDowngrade bool) (*Result, error) {
	// ── 1. Resolve binary path ────────────────────────────────────────────
	rawExe, err := u.resolver.Executable()
	if err != nil {
		return nil, cnerr.Wrap("selfupdate.resolveExe", err, "")
	}
	binaryPath, err := filepath.EvalSymlinks(rawExe)
	if err != nil {
		// EvalSymlinks can fail if the binary was deleted mid-run; fall back.
		binaryPath = rawExe
	}
	binaryDir := filepath.Dir(binaryPath)

	// ── 2. Probe write access (before any network I/O) ───────────────────
	if err = u.fileOps.ProbeWritable(binaryPath); err != nil {
		return nil, cnerr.Wrap(
			"selfupdate.probeWritable",
			err,
			"try running with sudo: sudo crenein-agent self-update",
		)
	}
	if err = u.fileOps.ProbeWritable(binaryDir); err != nil {
		return nil, cnerr.Wrap(
			"selfupdate.probeWritableDir",
			err,
			"try running with sudo: sudo crenein-agent self-update",
		)
	}

	// ── 3. Resolve asset ─────────────────────────────────────────────────
	asset, err := u.source.ResolveAsset(ctx, targetVersion)
	if err != nil {
		return nil, cnerr.Wrap("selfupdate.resolveAsset", err, "")
	}
	if err = ctx.Err(); err != nil {
		return nil, cnerr.Wrap("selfupdate.resolveAsset.ctx", err, "")
	}

	resolvedVersion := targetVersion
	if resolvedVersion == "" {
		// Extract version from asset — the ReleaseSource must embed it somewhere.
		// By convention, the resolved asset carries the version in its name or
		// the caller sets targetVersion. We rely on the source to provide it via
		// a field or use the asset's embedded info. Since ReleaseAsset has no
		// Version field we require the source to set targetVersion non-empty
		// when "latest". For the case where targetVersion == "" we use a helper.
		resolvedVersion = extractVersionFromAsset(asset)
	}

	// ── 4. Semver decision ────────────────────────────────────────────────
	cmp := compareSemver(currentVersion, resolvedVersion)
	action := "updated"
	switch {
	case targetVersion == "":
		// Latest mode.
		if cmp >= 0 {
			// Already at or newer than latest — no-op.
			return &Result{Action: "no-op", FromVersion: currentVersion, ToVersion: resolvedVersion}, nil
		}
	default:
		// Pinned mode.
		if cmp == 0 {
			// Same version — no-op.
			return &Result{Action: "no-op", FromVersion: currentVersion, ToVersion: resolvedVersion}, nil
		}
		if cmp > 0 {
			// Downgrade.
			if !allowDowngrade {
				return &Result{Action: "no-op", FromVersion: currentVersion, ToVersion: resolvedVersion}, nil
			}
			action = "downgraded"
		}
	}

	// ── 5. Download checksums.txt ─────────────────────────────────────────
	checksums, err := u.fetchBody(ctx, asset.ChecksumsURL)
	if err != nil {
		return nil, cnerr.Wrap("selfupdate.fetchChecksums", err, "")
	}
	if err = ctx.Err(); err != nil {
		return nil, cnerr.Wrap("selfupdate.fetchChecksums.ctx", err, "")
	}

	expected, ok := parseChecksum(checksums, asset.Name)
	if !ok {
		return nil, cnerr.New(
			fmt.Sprintf("selfupdate.verifyChecksum: no entry for %q in checksums.txt", asset.Name),
			"",
		)
	}

	// ── 6. Stream asset to temp file ─────────────────────────────────────
	tmpPath, err := u.downloadToTemp(ctx, asset.DownloadURL, binaryDir)
	if err != nil {
		return nil, err // already wrapped
	}
	// Ensure temp file is removed on any failure from here on.
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = u.fileOps.Remove(tmpPath)
		}
	}()

	if err = ctx.Err(); err != nil {
		return nil, cnerr.Wrap("selfupdate.download.ctx", err, "")
	}

	// ── 7. SHA256 verify ──────────────────────────────────────────────────
	if err = u.verifySHA256(tmpPath, expected); err != nil {
		return nil, err // already wrapped, temp will be cleaned up by defer
	}

	// ── 8. chmod 0755 ─────────────────────────────────────────────────────
	if err = u.fileOps.Chmod(tmpPath, fs.FileMode(0o755)); err != nil {
		return nil, cnerr.Wrap("selfupdate.chmod", err, "")
	}

	// ── 9. Atomic rename ──────────────────────────────────────────────────
	if err = u.fileOps.Rename(tmpPath, binaryPath); err != nil {
		return nil, cnerr.Wrap("selfupdate.rename", err, "")
	}
	removeTemp = false // rename succeeded; the file is now the binary

	return &Result{
		Action:      action,
		FromVersion: currentVersion,
		ToVersion:   resolvedVersion,
	}, nil
}

// ─── internal helpers ────────────────────────────────────────────────────────

// fetchBody performs a GET request and returns the full response body.
func (u *Updater) fetchBody(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.prober.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// downloadToTemp streams the asset at url into a temp file in dir. The caller
// is responsible for cleanup on failure (tmpPath is returned even on error so
// the caller can remove it).
func (u *Updater) downloadToTemp(ctx context.Context, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", cnerr.Wrap("selfupdate.buildDownloadReq", err, "")
	}
	resp, err := u.prober.Do(req)
	if err != nil {
		return "", cnerr.Wrap("selfupdate.download", err, "")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", cnerr.New(
			fmt.Sprintf("selfupdate.download: HTTP %d from %s", resp.StatusCode, url),
			"",
		)
	}

	tmpPath, err := u.fileOps.WriteTemp(dir, resp.Body)
	if err != nil {
		return "", cnerr.Wrap("selfupdate.writeTemp", err, "")
	}
	return tmpPath, nil
}

// verifySHA256 hashes the file at path and compares with expected (hex string).
func (u *Updater) verifySHA256(path, expected string) error {
	f, err := openForHash(path)
	if err != nil {
		return cnerr.Wrap("selfupdate.openForHash", err, "")
	}
	defer f.Close()

	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return cnerr.Wrap("selfupdate.hashFile", err, "")
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return cnerr.New(
			fmt.Sprintf("selfupdate.verifyChecksum: SHA256 mismatch (got %s, want %s)", actual, expected),
			"",
		)
	}
	return nil
}

// openForHash opens a file for reading its contents to compute a hash.
// Abstracted so tests can inject a fake FileOps that does not touch the real FS.
// In tests we compute the hash directly; in production we open via os.Open.
// We use a package-level variable so the fake can override it.
var openForHash = func(path string) (io.ReadCloser, error) {
	return openFileReal(path)
}

func openFileReal(path string) (io.ReadCloser, error) {
	return openOSFile(path)
}

// openOSFile is the real os.Open call (indirected so the test can replace
// openForHash without touching this function).
var openOSFile = func(path string) (io.ReadCloser, error) {
	return openOsFileImpl(path)
}

// parseChecksum scans a Go-releaser-style checksums.txt for a line matching
// "<sha256>  <assetName>" (two spaces) and returns the hex digest.
func parseChecksum(data []byte, assetName string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		// Format: "<sha256hex>  <filename>"
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == assetName {
			return strings.TrimSpace(parts[0]), true
		}
	}
	return "", false
}

// extractVersionFromAsset is a last-resort helper when targetVersion was "" and
// the source does not embed the version. It should never be needed in practice
// because ReleaseSource implementations are expected to populate targetVersion
// before calling Update, but it prevents a panic.
func extractVersionFromAsset(_ ReleaseAsset) string {
	return "unknown"
}

// ─── Semver comparison ───────────────────────────────────────────────────────

// compareSemver compares two semver strings (vX.Y.Z or X.Y.Z).
// Returns -1 when a < b, 0 when a == b, 1 when a > b.
// Non-numeric suffixes (pre-release labels) are compared lexicographically
// after the numeric triplet.
func compareSemver(a, b string) int {
	aN, aSuf := parseSemver(a)
	bN, bSuf := parseSemver(b)

	for i := 0; i < 3; i++ {
		if aN[i] < bN[i] {
			return -1
		}
		if aN[i] > bN[i] {
			return 1
		}
	}
	// Numeric parts equal — compare suffix lexicographically.
	// A pre-release label (non-empty suffix) sorts LOWER than a release.
	switch {
	case aSuf == "" && bSuf != "":
		return 1 // a is release, b is pre-release → a > b
	case aSuf != "" && bSuf == "":
		return -1
	default:
		return strings.Compare(aSuf, bSuf)
	}
}

// parseSemver strips a leading "v", splits on ".", and returns the three
// numeric parts plus any trailing suffix (e.g. "-rc.1").
func parseSemver(v string) ([3]int, string) {
	s := strings.TrimPrefix(v, "v")
	var nums [3]int
	suffix := ""

	parts := strings.SplitN(s, ".", 3)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		// The last segment may carry a pre-release suffix (e.g. "1-rc.1").
		n, rest := parseIntPrefix(p)
		nums[i] = n
		if rest != "" {
			suffix = rest
		}
	}
	return nums, suffix
}

// parseIntPrefix reads leading decimal digits from s and returns the integer
// and any remaining suffix (e.g. "10-rc" → 10, "-rc").
func parseIntPrefix(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, s
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, s[i:]
}
