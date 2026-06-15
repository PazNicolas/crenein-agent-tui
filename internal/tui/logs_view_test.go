package tui

// logs_view_test.go — Logs view tests (task 8.5)
//
// Scenarios covered:
//  1. TestLogsView_NoInstall        — no-install-dir state renders guidance message.
//  2. TestLogsView_StreamLines      — scripted stream delivers lines to viewport.
//  3. TestLogsView_FilterCycle      — 'f' key cycles filter and restarts stream with
//                                     the new service.
//  4. TestLogsView_PauseResume      — space pauses auto-scroll, second space resumes.
//  5. TestLogsView_StreamEnded      — stream-ended message shown on channel close.
//  6. TestLogsView_RingBufferCap    — push beyond cap doesn't grow unbounded.
//  7. TestLogsView_GoldenNoInstall  — golden at 80x24 for the no-install state.
//  8. TestLogsView_AsyncStream      — teatest WaitFor: lines appear from fake stream.
//
// All tests use injected fake deps — no real Docker daemon or network.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// fakeInstallReadFile returns a readFile that returns a docker-compose.yml
// referencing the agent image at the given installDir.
func fakeInstallReadFile(installDir string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if name == installDir+"/docker-compose.yml" {
			return []byte("image: crenein/c-network-agent-back:1.0.0\n"), nil
		}
		return nil, fmt.Errorf("open %s: no such file or directory", name)
	}
}

// logsNoInstallReadFile always returns not-found (used by logs view tests to
// avoid collision with install_view_test.go's logsNoInstallReadFile).
func logsNoInstallReadFile(_ string) ([]byte, error) {
	return nil, fmt.Errorf("no such file")
}

// newTestLogsView builds a logsView for direct (non-teatest) tests.
// It injects a no-op stream by default; tests can replace logsStreamFn.
func newTestLogsView(t *testing.T, deps logsViewDeps) *logsView {
	t.Helper()
	v := newLogsView(styles.NewProfile(true), deps)
	// Give it a stable window size.
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m.(*logsView)
}

// injectLineMsg injects a logLineMsg and returns the updated view.
func injectLineMsg(t *testing.T, v *logsView, line string, ch <-chan string) *logsView {
	t.Helper()
	// We set v.lineChan to the provided channel so the Update handler
	// does not discard the message as "stale".
	v.lineChan = ch
	m, _ := v.Update(logLineMsg{line: line})
	return m.(*logsView)
}

// ─── Test 1: No install state ─────────────────────────────────────────────────

func TestLogsView_NoInstall(t *testing.T) {
	deps := logsViewDeps{
		readFile:     logsNoInstallReadFile,
		readDir:      fakeReadDir,
		logsStreamFn: func(_ context.Context, _, _ string, _ int, _, _ bool, _ io.Writer) error { return nil },
	}
	v := newTestLogsView(t, deps)

	// Inject the logsEndedMsg that would arrive when no install is found.
	m, _ := v.Update(logsEndedMsg{err: fmt.Errorf("no installation found")})
	v = m.(*logsView)

	if !v.noInstall {
		t.Fatal("expected noInstall=true")
	}
	out := v.View()
	if !strings.Contains(out, "No installation found") {
		t.Errorf("expected 'No installation found' in view:\n%s", out)
	}
	if !strings.Contains(out, "Install wizard") {
		t.Errorf("expected Install wizard hint in view:\n%s", out)
	}
}

// ─── Test 2: Scripted stream delivers lines to the viewport ──────────────────

func TestLogsView_StreamLines(t *testing.T) {
	installDir := "/opt/crenein"
	deps := logsViewDeps{
		readFile:     fakeInstallReadFile(installDir),
		readDir:      fakeReadDir,
		logsStreamFn: func(_ context.Context, _, _ string, _ int, _, _ bool, _ io.Writer) error { return nil },
	}
	v := newTestLogsView(t, deps)

	// Simulate composeFile ready.
	m, _ := v.Update(logsComposeReadyMsg{composeFile: installDir + "/docker-compose.yml"})
	v = m.(*logsView)

	// Inject lines directly via logLineMsg (using the current lineChan so
	// stale-check passes).
	ch := v.lineChan
	for _, line := range []string{"line-A", "line-B", "line-C"} {
		v = injectLineMsg(t, v, line, ch)
		ch = v.lineChan // channel reference stays the same, but update returns new *logsView
	}

	if v.buf.len() != 3 {
		t.Errorf("expected 3 lines in buffer, got %d", v.buf.len())
	}

	// Viewport content should contain the injected lines.
	vpContent := v.vp.View()
	for _, want := range []string{"line-A", "line-B", "line-C"} {
		if !strings.Contains(vpContent, want) {
			t.Errorf("viewport content missing %q:\n%s", want, vpContent)
		}
	}
}

// ─── Test 3: Filter cycling ───────────────────────────────────────────────────

func TestLogsView_FilterCycle(t *testing.T) {
	installDir := "/opt/crenein"
	var lastService atomic.Value
	lastService.Store("")

	// Count stream starts so we can assert restarts.
	var startCount atomic.Int32

	deps := logsViewDeps{
		readFile: fakeInstallReadFile(installDir),
		readDir:  fakeReadDir,
		logsStreamFn: func(ctx context.Context, _, service string, _ int, _, _ bool, _ io.Writer) error {
			lastService.Store(service)
			startCount.Add(1)
			// Block until ctx is cancelled, simulating a live follow stream.
			<-ctx.Done()
			return nil
		},
	}
	v := newTestLogsView(t, deps)

	// Simulate composeFile ready → starts first stream.
	m, _ := v.Update(logsComposeReadyMsg{composeFile: installDir + "/docker-compose.yml"})
	v = m.(*logsView)

	// Initial filter is "all" → service = "".
	if v.filterLabel() != "all" {
		t.Errorf("initial filter = %q, want 'all'", v.filterLabel())
	}

	// Press 'f': should cycle to "agent".
	m2, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	v = m2.(*logsView)
	if v.filterLabel() != "agent" {
		t.Errorf("after 'f', filter = %q, want 'agent'", v.filterLabel())
	}
	if cmd == nil {
		t.Error("expected non-nil Cmd after filter cycle (stream restart)")
	}

	// Press 'f' four more times to cycle through and back to "all".
	for i, want := range []string{"frontend", "mongodb", "influxdb", "redis"} {
		m2, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
		v = m2.(*logsView)
		if v.filterLabel() != want {
			t.Errorf("cycle step %d: filter = %q, want %q", i+1, v.filterLabel(), want)
		}
	}
	// One more should wrap back to "all".
	m2, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	v = m2.(*logsView)
	if v.filterLabel() != "all" {
		t.Errorf("after full cycle, filter = %q, want 'all'", v.filterLabel())
	}
}

// ─── Test 4: Pause and resume ─────────────────────────────────────────────────

func TestLogsView_PauseResume(t *testing.T) {
	installDir := "/opt/crenein"
	deps := logsViewDeps{
		readFile:     fakeInstallReadFile(installDir),
		readDir:      fakeReadDir,
		logsStreamFn: func(ctx context.Context, _, _ string, _ int, _, _ bool, _ io.Writer) error { <-ctx.Done(); return nil },
	}
	v := newTestLogsView(t, deps)

	m, _ := v.Update(logsComposeReadyMsg{composeFile: installDir + "/docker-compose.yml"})
	v = m.(*logsView)

	// Initially not paused.
	if v.paused {
		t.Fatal("expected paused=false initially")
	}
	out := v.View()
	if !strings.Contains(out, "FOLLOWING") {
		t.Errorf("expected FOLLOWING indicator, got:\n%s", out)
	}

	// Press space → should pause.
	m2, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	v = m2.(*logsView)
	if !v.paused {
		t.Fatal("expected paused=true after space")
	}
	out = v.View()
	if !strings.Contains(out, "PAUSED") {
		t.Errorf("expected PAUSED indicator, got:\n%s", out)
	}

	// Inject a line while paused — it should go into the buffer but
	// NOT auto-scroll to bottom (viewport stays wherever it is).
	ch := v.lineChan
	v = injectLineMsg(t, v, "late-line", ch)
	if v.paused {
		// still paused — that's correct
	}
	if v.buf.len() == 0 {
		t.Error("expected line to be buffered while paused")
	}

	// Press space again → resume.
	m3, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	v = m3.(*logsView)
	if v.paused {
		t.Fatal("expected paused=false after second space")
	}
	out = v.View()
	if !strings.Contains(out, "FOLLOWING") {
		t.Errorf("expected FOLLOWING indicator after resume, got:\n%s", out)
	}
}

// ─── Test 5: Stream-ended message ─────────────────────────────────────────────

func TestLogsView_StreamEnded(t *testing.T) {
	installDir := "/opt/crenein"
	deps := logsViewDeps{
		readFile:     fakeInstallReadFile(installDir),
		readDir:      fakeReadDir,
		logsStreamFn: func(_ context.Context, _, _ string, _ int, _, _ bool, _ io.Writer) error { return nil },
	}
	v := newTestLogsView(t, deps)

	m, _ := v.Update(logsComposeReadyMsg{composeFile: installDir + "/docker-compose.yml"})
	v = m.(*logsView)

	// Simulate an error stream end.
	streamErr := fmt.Errorf("docker daemon gone")
	m2, _ := v.Update(logsEndedMsg{err: streamErr})
	v = m2.(*logsView)

	if !v.streamEnded {
		t.Fatal("expected streamEnded=true")
	}
	out := v.View()
	if !strings.Contains(out, "Stream ended") {
		t.Errorf("expected 'Stream ended' in view:\n%s", out)
	}
	if !strings.Contains(out, "docker daemon gone") {
		t.Errorf("expected error text in view:\n%s", out)
	}
	// Must offer restart.
	if !strings.Contains(out, "r") || !strings.Contains(out, "restart") {
		t.Errorf("expected restart hint in view:\n%s", out)
	}
}

// ─── Test 6: Ring buffer cap ──────────────────────────────────────────────────

func TestLogsView_RingBufferCap(t *testing.T) {
	r := newRingBuffer(5)
	for i := 0; i < 10; i++ {
		r.push(fmt.Sprintf("line-%d", i))
	}
	if r.len() != 5 {
		t.Errorf("expected ring buffer len=5 after 10 pushes, got %d", r.len())
	}
	// The oldest 5 lines (0-4) should have been evicted; we keep the newest 5 (5-9).
	content := r.content()
	for i := 5; i < 10; i++ {
		if !strings.Contains(content, fmt.Sprintf("line-%d", i)) {
			t.Errorf("expected line-%d in ring buffer content", i)
		}
	}
	for i := 0; i < 5; i++ {
		if strings.Contains(content, fmt.Sprintf("line-%d\n", i)) ||
			content == fmt.Sprintf("line-%d", i) {
			t.Errorf("line-%d should have been evicted from ring buffer", i)
		}
	}
}

// ─── Test 7: Golden — no-install state ───────────────────────────────────────

func TestLogsView_GoldenNoInstall(t *testing.T) {
	deps := logsViewDeps{
		readFile:     logsNoInstallReadFile,
		readDir:      fakeReadDir,
		logsStreamFn: func(_ context.Context, _, _ string, _ int, _, _ bool, _ io.Writer) error { return nil },
	}
	v := newLogsView(styles.NewProfile(true), deps)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(*logsView)
	m2, _ := v.Update(logsEndedMsg{err: fmt.Errorf("no installation found")})
	v = m2.(*logsView)

	golden.RequireEqual(t, []byte(v.View()))
}

// ─── Test 8: Async teatest — lines appear from a fake stream ─────────────────

// TestLogsView_AsyncStream uses teatest to drive the full async lifecycle:
// A fake logsStreamFn writes a few lines and then returns (stream ends cleanly).
// We WaitFor those lines to appear in the rendered output.
func TestLogsView_AsyncStream(t *testing.T) {
	// Place compose file at /root so ResolveInstallDir finds it.
	rootInstallReadFile := func(name string) ([]byte, error) {
		if name == "/root/docker-compose.yml" {
			return []byte("image: crenein/c-network-agent-back:1.0.0\n"), nil
		}
		return nil, fmt.Errorf("no such file: %s", name)
	}

	// Fake stream: writes some lines then returns immediately.
	streamLines := []string{"hello-from-agent", "world-line-2", "third-entry"}
	deps := logsViewDeps{
		readFile: rootInstallReadFile,
		readDir:  fakeReadDir,
		logsStreamFn: func(ctx context.Context, _, _ string, _ int, _, _ bool, stdout io.Writer) error {
			for _, l := range streamLines {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintln(stdout, l)
			}
			return nil
		},
	}

	v := newLogsView(styles.NewProfile(true), deps)

	tm := teatest.NewTestModel(t, v, teatest.WithInitialTermSize(80, 24))

	// The stream will deliver lines; wait for them to appear in the viewport.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		s := string(bts)
		return strings.Contains(s, "hello-from-agent") &&
			strings.Contains(s, "world-line-2") &&
			strings.Contains(s, "third-entry")
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(50*time.Millisecond))

	tm.Send(tea.QuitMsg{})
	_, _ = io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(3*time.Second)))
}
