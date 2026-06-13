package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ─── fakeFileOps ────────────────────────────────────────────────────────────

// fakeFileOps is a recording test double for FileOps.
// It stores named "files" in an in-memory map and records operations.
type fakeFileOps struct {
	mu sync.Mutex

	// files maps path → content.
	files map[string][]byte
	// modes maps path → FileMode.
	modes map[string]fs.FileMode

	// Recorded operations (in order).
	renames   [][2]string // [old, new]
	removes   []string
	chmods    []chmodCall
	writeTmps []string // paths of temp files created

	// Injected errors.
	probeErr    error
	writeTmpErr error
	renameErr   error
	chmodErr    error

	// tmpCounter generates unique temp file names.
	tmpCounter int
}

type chmodCall struct {
	path string
	mode fs.FileMode
}

func newFakeFileOps(initial map[string][]byte) *fakeFileOps {
	f := &fakeFileOps{
		files: make(map[string][]byte),
		modes: make(map[string]fs.FileMode),
	}
	for k, v := range initial {
		cp := make([]byte, len(v))
		copy(cp, v)
		f.files[k] = cp
	}
	return f
}

func (f *fakeFileOps) Rename(old, new string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renameErr != nil {
		return f.renameErr
	}
	f.renames = append(f.renames, [2]string{old, new})
	data := f.files[old]
	mode := f.modes[old]
	delete(f.files, old)
	delete(f.modes, old)
	f.files[new] = data
	f.modes[new] = mode
	return nil
}

func (f *fakeFileOps) WriteTemp(dir string, r io.Reader) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeTmpErr != nil {
		return "", f.writeTmpErr
	}
	f.tmpCounter++
	path := fmt.Sprintf("%s/.crenein-agent.new-%04d", dir, f.tmpCounter)
	data, err := io.ReadAll(r)
	if err != nil {
		return path, err
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.files[path] = cp
	f.writeTmps = append(f.writeTmps, path)
	return path, nil
}

func (f *fakeFileOps) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, path)
	delete(f.files, path)
	delete(f.modes, path)
	return nil
}

func (f *fakeFileOps) Chmod(path string, mode fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chmodErr != nil {
		return f.chmodErr
	}
	f.chmods = append(f.chmods, chmodCall{path: path, mode: mode})
	f.modes[path] = mode
	return nil
}

func (f *fakeFileOps) Stat(path string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("stat %s: no such file or directory", path)
	}
	return &fakeFileInfo{name: path, size: int64(len(data)), mode: f.modes[path]}, nil
}

func (f *fakeFileOps) ProbeWritable(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeErr
}

// readFile is a test helper to read the in-memory file.
func (f *fakeFileOps) readFile(path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.files[path]
	return d, ok
}

// hasFile reports whether path exists in the fake.
func (f *fakeFileOps) hasFile(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[path]
	return ok
}

// ─── fakeFileInfo ────────────────────────────────────────────────────────────

type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (fi *fakeFileInfo) Name() string       { return fi.name }
func (fi *fakeFileInfo) Size() int64        { return fi.size }
func (fi *fakeFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *fakeFileInfo) IsDir() bool        { return false }
func (fi *fakeFileInfo) Sys() any           { return nil }

// Compile-time check: fakeFileInfo implements fs.FileInfo.
var _ fs.FileInfo = (*fakeFileInfo)(nil)

// ─── fakeExecutableResolver ─────────────────────────────────────────────────

type fakeExecutableResolver struct {
	path string
	err  error
}

func (r *fakeExecutableResolver) Executable() (string, error) {
	return r.path, r.err
}

// ─── fakeReleaseSource ──────────────────────────────────────────────────────

type fakeReleaseSource struct {
	asset ReleaseAsset
	err   error
}

func (s *fakeReleaseSource) ResolveAsset(_ context.Context, _ string) (ReleaseAsset, error) {
	return s.asset, s.err
}

// ─── fakeHTTPProber ─────────────────────────────────────────────────────────

// fakeHTTPProber serves pre-loaded responses in FIFO order, recording requests.
type fakeHTTPProber struct {
	mu        sync.Mutex
	responses []httpFakeResp
	requests  []string
}

type httpFakeResp struct {
	body   string
	status int
	err    error
}

func (p *fakeHTTPProber) Do(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req.URL.String())
	if len(p.responses) == 0 {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	}
	r := p.responses[0]
	p.responses = p.responses[1:]
	if r.err != nil {
		return nil, r.err
	}
	code := r.status
	if code == 0 {
		code = 200
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     make(http.Header),
	}, nil
}

func (p *fakeHTTPProber) addResponse(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, httpFakeResp{status: status, body: body})
}

func (p *fakeHTTPProber) addError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, httpFakeResp{err: err})
}

// ─── in-memory hash helper ───────────────────────────────────────────────────

// fakeHashContent is a map of path → content used to override openForHash in
// tests. Set it before running a test case and clear it after.
var fakeHashContent map[string][]byte

// installFakeHasher overrides openForHash to serve content from fakeHashContent.
func installFakeHasher() {
	openForHash = func(path string) (io.ReadCloser, error) {
		if fakeHashContent == nil {
			return nil, fmt.Errorf("fakeHashContent not set for %s", path)
		}
		data, ok := fakeHashContent[path]
		if !ok {
			return nil, fmt.Errorf("no fake hash content for %s", path)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// restoreRealHasher restores the real os.Open-based openForHash.
func restoreRealHasher() {
	openForHash = func(path string) (io.ReadCloser, error) {
		return openFileReal(path)
	}
}
