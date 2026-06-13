package dockerx

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// ─── FakeClient ──────────────────────────────────────────────────────────────

// Call records a single invocation of a FakeClient method.
type Call struct {
	// Method is the name of the interface method invoked (e.g. "Ping",
	// "ComposeUp").
	Method string
	// Args is the method's positional arguments as strings (for assertions).
	Args []string
}

// FakeClient is a recording test double for Client. Every method appends to
// Calls so tests can assert the exact invocation sequence.
//
// Stubs control what each method returns; if a stub is nil the method returns
// nil / zero values. Use the Stub* fields to inject per-call responses.
type FakeClient struct {
	mu sync.Mutex
	// Calls records every invocation in order.
	Calls []Call

	// Stubs — set these before the code under test runs.
	PingErr          error
	ComposeUpErr     error
	ComposePsResult  []ContainerState
	ComposePsErr     error
	ComposePullErr   error
	ComposeExecOut   []byte
	ComposeExecErr   error
	ImageInspectOut  ImageInfo
	ImageInspectErr  error
	ImageTagErr      error
	ImagePruneErr    error
	ContainerListOut []ContainerState
	ContainerListErr error
	// ComposeLogsOut maps service name to canned log output.
	// If the service is not in the map, empty output is returned.
	ComposeLogsOut map[string][]byte
	ComposeLogsErr error
}

func (f *FakeClient) record(method string, args ...string) {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Method: method, Args: args})
	f.mu.Unlock()
}

// Ping records the call and returns PingErr.
func (f *FakeClient) Ping(_ context.Context) error {
	f.record("Ping")
	return f.PingErr
}

// ComposeUp records the call and returns ComposeUpErr.
func (f *FakeClient) ComposeUp(_ context.Context, composeFile string, opts ComposeUpOptions) error {
	f.record("ComposeUp", composeFile,
		fmt.Sprintf("NoDeps=%v ForceRecreate=%v Detach=%v Services=%v",
			opts.NoDeps, opts.ForceRecreate, opts.Detach, opts.Services))
	return f.ComposeUpErr
}

// ComposePs records the call and returns ComposePsResult / ComposePsErr.
func (f *FakeClient) ComposePs(_ context.Context, composeFile string, services []string) ([]ContainerState, error) {
	f.record("ComposePs", composeFile, fmt.Sprintf("%v", services))
	return f.ComposePsResult, f.ComposePsErr
}

// ComposePull records the call and returns ComposePullErr.
func (f *FakeClient) ComposePull(_ context.Context, composeFile string, services []string) error {
	f.record("ComposePull", composeFile, fmt.Sprintf("%v", services))
	return f.ComposePullErr
}

// ComposeExec records the call and returns ComposeExecOut / ComposeExecErr.
func (f *FakeClient) ComposeExec(_ context.Context, composeFile string, opts ExecOptions) ([]byte, error) {
	f.record("ComposeExec", composeFile, opts.Service, fmt.Sprintf("%v", opts.Cmd))
	return f.ComposeExecOut, f.ComposeExecErr
}

// ImageInspect records the call and returns ImageInspectOut / ImageInspectErr.
func (f *FakeClient) ImageInspect(_ context.Context, ref string) (ImageInfo, error) {
	f.record("ImageInspect", ref)
	return f.ImageInspectOut, f.ImageInspectErr
}

// ImageTag records the call and returns ImageTagErr.
func (f *FakeClient) ImageTag(_ context.Context, source, target string) error {
	f.record("ImageTag", source, target)
	return f.ImageTagErr
}

// ImagePrune records the call and returns ImagePruneErr.
func (f *FakeClient) ImagePrune(_ context.Context) error {
	f.record("ImagePrune")
	return f.ImagePruneErr
}

// ContainerList records the call and returns ContainerListOut / ContainerListErr.
func (f *FakeClient) ContainerList(_ context.Context, nameFilter string) ([]ContainerState, error) {
	f.record("ContainerList", nameFilter)
	return f.ContainerListOut, f.ContainerListErr
}

// ComposeLogs records the call and returns canned log output for the service.
func (f *FakeClient) ComposeLogs(_ context.Context, composeFile, service string, tail int) ([]byte, error) {
	f.record("ComposeLogs", composeFile, service, fmt.Sprintf("tail=%d", tail))
	if f.ComposeLogsErr != nil {
		return nil, f.ComposeLogsErr
	}
	if f.ComposeLogsOut != nil {
		if out, ok := f.ComposeLogsOut[service]; ok {
			return out, nil
		}
	}
	return nil, nil
}

// ─── FakeCommandRunner ───────────────────────────────────────────────────────

// CmdInvocation records a single Run call to FakeCommandRunner.
type CmdInvocation struct {
	Name string
	Args []string
}

// FakeCommandRunner is a recording test double for CommandRunner. Tests can
// pre-load per-call responses via Responses; leftover calls return a zero
// response and nil error.
type FakeCommandRunner struct {
	mu sync.Mutex
	// Invocations records every Run call in order.
	Invocations []CmdInvocation
	// Responses is a list of pre-loaded responses consumed in order (FIFO).
	// When exhausted, subsequent calls return ("", nil).
	Responses []CmdResponse
	// LookPathFunc overrides LookPath when set.
	LookPathFunc func(name string) (string, error)
}

// CmdResponse is a single pre-loaded response for FakeCommandRunner.
type CmdResponse struct {
	Out []byte
	Err error
}

func (r *FakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.Invocations = append(r.Invocations, CmdInvocation{Name: name, Args: args})
	var resp CmdResponse
	if len(r.Responses) > 0 {
		resp = r.Responses[0]
		r.Responses = r.Responses[1:]
	}
	r.mu.Unlock()
	return resp.Out, resp.Err
}

func (r *FakeCommandRunner) LookPath(name string) (string, error) {
	if r.LookPathFunc != nil {
		return r.LookPathFunc(name)
	}
	return "/usr/bin/" + name, nil
}

// ─── FakeFS ──────────────────────────────────────────────────────────────────

// FakeFS is an in-memory implementation of FS for use in tests. Pre-populate
// Files with path→content to simulate existing files; Writes captures every
// WriteFile call for assertions.
type FakeFS struct {
	mu sync.Mutex
	// Files is the initial set of files (path → content).
	Files map[string][]byte
	// Modes tracks file permission bits set via WriteFile or Chmod (path → perm).
	Modes map[string]uint32
	// Writes captures each WriteFile call in order.
	Writes []FSWrite
	// Removes captures each RemoveAll call path in order.
	Removes []string
	// StatErr, if set, is returned by Stat for all paths not in Files.
	StatErr error
}

// FSWrite records one WriteFile call.
type FSWrite struct {
	Name string
	Data []byte
	Perm uint32
}

// NewFakeFS creates a FakeFS with the given initial file contents.
func NewFakeFS(files map[string][]byte) *FakeFS {
	if files == nil {
		files = make(map[string][]byte)
	}
	return &FakeFS{
		Files: files,
		Modes: make(map[string]uint32),
	}
}

func (f *FakeFS) ReadFile(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.Files[name]
	if !ok {
		return nil, fmt.Errorf("open %s: no such file or directory", name)
	}
	return data, nil
}

func (f *FakeFS) WriteFile(name string, data []byte, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Files == nil {
		f.Files = make(map[string][]byte)
	}
	if f.Modes == nil {
		f.Modes = make(map[string]uint32)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	f.Files[name] = cp
	f.Modes[name] = perm
	f.Writes = append(f.Writes, FSWrite{Name: name, Data: cp, Perm: perm})
	return nil
}

func (f *FakeFS) MkdirAll(path string, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Modes == nil {
		f.Modes = make(map[string]uint32)
	}
	f.Modes[path] = perm
	return nil
}

func (f *FakeFS) Stat(name string) (FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.Files[name]
	if !ok {
		if f.StatErr != nil {
			return FileInfo{}, f.StatErr
		}
		return FileInfo{}, fmt.Errorf("stat %s: no such file or directory", name)
	}
	perm := f.Modes[name]
	if perm == 0 {
		perm = 0o644
	}
	return FileInfo{
		Name: name,
		Mode: perm,
		Size: int64(len(data)),
	}, nil
}

func (f *FakeFS) Chmod(name string, perm uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Modes == nil {
		f.Modes = make(map[string]uint32)
	}
	f.Modes[name] = perm
	return nil
}

func (f *FakeFS) Chown(_ string, _, _ int) error {
	return nil // no-op in the fake
}

func (f *FakeFS) ReadDir(path string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := path + "/"
	seen := make(map[string]bool)
	for name := range f.Files {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			rest := name[len(prefix):]
			// Only immediate children (no further slashes).
			if idx := indexByte(rest, '/'); idx == -1 {
				seen[rest] = true
			} else {
				seen[rest[:idx]] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names, nil
}

// RemoveAll deletes path and any children (keys equal to path or prefixed by
// path+"/") from the in-memory maps and records the call in Removes.
func (f *FakeFS) RemoveAll(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Removes = append(f.Removes, path)
	prefix := path + "/"
	for name := range f.Files {
		if name == path || (len(name) > len(prefix) && name[:len(prefix)] == prefix) {
			delete(f.Files, name)
			delete(f.Modes, name)
		}
	}
	return nil
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// ─── FakeHTTPProber ──────────────────────────────────────────────────────────

// HTTPRequest records a single Do call to FakeHTTPProber.
type HTTPRequest struct {
	Method string
	URL    string
}

// FakeHTTPProber is a recording test double for HTTPProber. Pre-load Responses
// (consumed FIFO); each call appends to Requests.
type FakeHTTPProber struct {
	mu       sync.Mutex
	Requests []HTTPRequest
	// Responses is consumed FIFO. When exhausted, subsequent calls return a
	// 200 response with no body.
	Responses []HTTPResponse
}

// HTTPResponse is a pre-loaded response for FakeHTTPProber.
type HTTPResponse struct {
	Resp *http.Response
	Err  error
}

func (f *FakeHTTPProber) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, HTTPRequest{
		Method: req.Method,
		URL:    req.URL.String(),
	})
	if len(f.Responses) > 0 {
		resp := f.Responses[0]
		f.Responses = f.Responses[1:]
		return resp.Resp, resp.Err
	}
	// Default: 200 OK with no body.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}
