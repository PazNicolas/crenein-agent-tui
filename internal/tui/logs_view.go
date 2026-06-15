package tui

// logs_view.go — Logs view (Lote 5: tasks 6.1–6.4)
//
// Architecture: writer→channel bridge.
//
//	ComposeLogsStream writes to an io.Writer and blocks until ctx cancels.
//	We bridge it to the bubbletea message loop via a lineChanWriter that splits
//	incoming bytes into complete lines and sends them to a buffered chan string.
//
//	Two tea.Cmds drive the bridge:
//	  • listenLogLine  — reads ONE line from the chan, returns logLineMsg; the
//	                     Update handler re-issues it until the channel is closed
//	                     (logsEndedMsg signals channel-closed).
//	  • startStreamCmd — goroutine that calls logsStreamFn (blocking with follow=true);
//	                     when it returns it closes the channel and sends logsEndedMsg.
//
//	Cancellation: changing the service filter or re-starting the stream calls the
//	stored cancel() on the active stream context, which unblocks the goroutine.
//	Navigating away from the view does NOT cancel the stream (no notification from
//	root). The ring buffer bounds memory so this is acceptable.
//
// Layout (AD-5): uses bubbles/viewport for the scrollback area; the viewport is
// sized to height-4 (header + footer + status line + title row).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/PazNicolas/crenein-agent-tui/internal/status"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── constants ────────────────────────────────────────────────────────────────

const (
	// logsRingCap is the maximum number of lines kept in memory.
	logsRingCap = 2000
	// logsDefaultTail is the number of historical lines requested on open.
	logsDefaultTail = 100
	// logsChanBuf is the size of the internal line channel buffer.
	logsChanBuf = 256
)

// logsFilterCycle is the ordered filter cycle (spec §6.2).
var logsFilterCycle = []string{"all", "agent", "frontend", "mongodb", "influxdb", "redis"}

// ─── messages ────────────────────────────────────────────────────────────────

// logLineMsg carries one log line from the stream goroutine to the UI loop.
type logLineMsg struct{ line string }

// logsEndedMsg signals that the stream goroutine has returned.
type logsEndedMsg struct{ err error }

// ─── lineChanWriter — writer→channel bridge ───────────────────────────────────

// lineChanWriter implements io.Writer. It accumulates bytes into an internal
// buffer, splits on '\n', and sends each complete line to ch. The final
// incomplete line (no trailing '\n') is held until the next Write or until
// Flush is called.
//
// Concurrent safety: ComposeLogsStream calls Write from a single goroutine, so
// no locking is needed on the write side. The channel is the only shared
// resource between producer and consumer.
type lineChanWriter struct {
	ch  chan<- string
	mu  sync.Mutex // protects buf
	buf bytes.Buffer
}

// newLineChanWriter creates a lineChanWriter and returns the companion read channel.
func newLineChanWriter() (*lineChanWriter, <-chan string) {
	ch := make(chan string, logsChanBuf)
	return &lineChanWriter{ch: ch}, ch
}

// Write splits p by '\n' and sends every complete line to ch. If the channel
// buffer is full the oldest in-flight line is dropped (non-blocking send).
func (w *lineChanWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p) //nolint:errcheck
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := string(data[:idx])
		w.buf.Next(idx + 1)
		// Non-blocking send: drop if buffer is full so the stream goroutine
		// never stalls.
		select {
		case w.ch <- line:
		default:
			// Buffer full: drop this line.
		}
	}
	return len(p), nil
}

// Close sends any remaining buffered bytes (without a trailing '\n') as a final
// line and then closes the channel, signalling EOF to the consumer.
func (w *lineChanWriter) Close() {
	w.mu.Lock()
	if w.buf.Len() > 0 {
		line := w.buf.String()
		w.buf.Reset()
		select {
		case w.ch <- line:
		default:
		}
	}
	w.mu.Unlock()
	close(w.ch)
}

// ─── ring buffer ─────────────────────────────────────────────────────────────

// ringBuffer is a fixed-capacity FIFO line store. When full, the oldest line is
// silently dropped to make room for the newest.
type ringBuffer struct {
	cap  int
	data []string
}

func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{cap: cap, data: make([]string, 0, min(cap, 256))}
}

// push appends a line, evicting the oldest when at capacity.
func (r *ringBuffer) push(line string) {
	if len(r.data) >= r.cap {
		// Shift: drop index 0.
		r.data = r.data[1:]
	}
	r.data = append(r.data, line)
}

// content returns all lines joined by '\n'.
func (r *ringBuffer) content() string {
	return strings.Join(r.data, "\n")
}

// len returns the number of lines stored.
func (r *ringBuffer) len() int { return len(r.data) }

// ─── logsViewDeps — injection seam ───────────────────────────────────────────

// logsViewDeps holds injectable dependencies for the Logs view.
type logsViewDeps struct {
	// logsStreamFn streams compose logs to stdout. Default: real client.
	logsStreamFn func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error

	// readFile is used by status.ResolveInstallDir.
	readFile func(name string) ([]byte, error)

	// readDir is used by status.ResolveInstallDir.
	readDir func(path string) ([]string, error)
}

// ─── logsView ────────────────────────────────────────────────────────────────

// logsView is the Logs screen Bubbletea sub-model (§6).
// It replaces the stub logsView{} defined in views.go.
type logsView struct {
	profile styles.Profile
	deps    logsViewDeps

	// layout
	width  int
	height int

	// install resolution (populated on Init / restart)
	composeFile string
	noInstall   bool // true when no installation was found

	// active stream
	filterIdx  int // index into logsFilterCycle
	lineChan   <-chan string
	errCh      <-chan error       // receives the stream's final error before lineChan closes
	cancelFunc context.CancelFunc // cancels the running stream; nil when no stream

	// display state
	buf     *ringBuffer
	vp      viewport.Model
	paused  bool
	vpReady bool // true after first WindowSizeMsg and composeFile resolved

	// stream-ended state
	streamEnded bool
	streamErr   error // nil on clean EOF
}

// activeService returns the current service filter string ("" = all services).
func (v *logsView) activeService() string {
	svc := logsFilterCycle[v.filterIdx]
	if svc == "all" {
		return ""
	}
	return svc
}

// filterLabel returns the display label for the current filter.
func (v *logsView) filterLabel() string {
	return logsFilterCycle[v.filterIdx]
}

// ─── tea.Model implementation ─────────────────────────────────────────────────

// Init resolves the installation directory and starts the first stream.
func (v *logsView) Init() tea.Cmd {
	return v.initCmd()
}

// initCmd is a helper so we can call it from both Init() and restart logic.
func (v *logsView) initCmd() tea.Cmd {
	return func() tea.Msg {
		// Resolve install dir.
		readFile := v.deps.readFile
		readDir := v.deps.readDir
		installDir := status.ResolveInstallDir(readFile, readDir, "")
		if installDir == "" {
			// Signal: no installation.
			return logsEndedMsg{err: fmt.Errorf("no installation found")}
		}
		// Return a synthetic "ready" message carrying the composeFile.
		return logsComposeReadyMsg{composeFile: installDir + "/docker-compose.yml"}
	}
}

// logsComposeReadyMsg is returned when the install-dir resolution succeeds.
type logsComposeReadyMsg struct{ composeFile string }

// Update handles all messages for the Logs view.
func (v *logsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		bodyH := v.bodyHeight()
		if !v.vpReady {
			v.vp = viewport.New(v.width, bodyH)
			v.vpReady = true
		} else {
			v.vp.Width = v.width
			v.vp.Height = bodyH
		}
		return v, nil

	case logsComposeReadyMsg:
		v.composeFile = msg.composeFile
		v.noInstall = false
		return v, v.startStream()

	case logsEndedMsg:
		if msg.err != nil && msg.err.Error() == "no installation found" {
			v.noInstall = true
			v.streamEnded = false
			return v, nil
		}
		// Don't nil lineChan immediately: an outstanding listenLogLine may still
		// deliver buffered lines before it reads the channel-close. We mark the
		// stream as ended; any subsequent logLineMsg will still be processed.
		v.streamEnded = true
		v.streamErr = msg.err
		v.cancelFunc = nil
		// Refresh viewport to show the final buffered lines.
		v.vp.SetContent(v.buf.content())
		v.vp.GotoBottom()
		return v, nil

	case logLineMsg:
		if v.streamEnded {
			// Stream already ended; still accept any late-arriving lines.
			v.buf.push(msg.line)
			v.vp.SetContent(v.buf.content())
			v.vp.GotoBottom()
			return v, nil
		}
		v.buf.push(msg.line)
		if !v.paused {
			v.vp.SetContent(v.buf.content())
			v.vp.GotoBottom()
		}
		// Re-issue the listen cmd to read the next line.
		if v.lineChan == nil {
			return v, nil
		}
		return v, listenLogLine(v.lineChan, v.errCh)

	case tea.KeyMsg:
		switch msg.String() {
		case "f", "tab":
			// Cycle service filter.
			return v, v.cycleFilter()

		case " ":
			// Toggle pause/resume.
			if v.paused {
				// Resume: jump to bottom.
				v.paused = false
				v.vp.SetContent(v.buf.content())
				v.vp.GotoBottom()
			} else {
				v.paused = true
			}
			return v, nil

		case "r":
			// Restart stream.
			return v, v.restartStream()
		}

		// Forward scroll keys to the viewport.
		if v.vpReady {
			var vpCmd tea.Cmd
			v.vp, vpCmd = v.vp.Update(msg)
			return v, vpCmd
		}
		return v, nil
	}

	// Forward all other messages to the viewport (e.g. wheel events).
	if v.vpReady {
		var vpCmd tea.Cmd
		v.vp, vpCmd = v.vp.Update(msg)
		return v, vpCmd
	}
	return v, nil
}

// View renders the Logs view body.
func (v *logsView) View() string {
	p := v.profile

	if v.noInstall {
		lines := []string{
			p.TitleStyle().Render("Logs"),
			"",
			"  No installation found.",
			"  Press i to open the Install wizard.",
			"",
			p.FooterStyle().Render("i: install  esc: back"),
		}
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	// Status line: filter + paused indicator.
	filterStr := fmt.Sprintf("Service: %s", v.filterLabel())
	stateStr := "FOLLOWING"
	if v.paused {
		stateStr = "PAUSED"
	}
	statusLine := p.FooterStyle().Render(fmt.Sprintf("%s  |  %s", filterStr, stateStr))

	// Stream-ended notice.
	if v.streamEnded {
		msg := "Stream ended."
		if v.streamErr != nil {
			msg = fmt.Sprintf("Stream ended: %v", v.streamErr)
		}
		footer := p.FooterStyle().Render(
			"f/tab: filter  space: pause/resume  r: restart  esc: back",
		)
		if !v.vpReady {
			return p.ActiveViewStyle().Render(strings.Join([]string{
				p.TitleStyle().Render("Logs"),
				statusLine,
				"",
				"  " + msg,
				"  Press r to restart the stream.",
				"",
				footer,
			}, "\n"))
		}
		v.vp.SetContent(v.buf.content())
		return strings.Join([]string{
			p.TitleStyle().Render("Logs"),
			statusLine,
			v.vp.View(),
			"  " + msg + "  Press r to restart.",
			footer,
		}, "\n")
	}

	footer := p.FooterStyle().Render(
		"f/tab: filter  space: pause/resume  ↑↓/pgup/pgdn: scroll  esc: back",
	)

	if !v.vpReady || v.composeFile == "" {
		return p.ActiveViewStyle().Render(strings.Join([]string{
			p.TitleStyle().Render("Logs"),
			"  Loading…",
		}, "\n"))
	}

	return strings.Join([]string{
		p.TitleStyle().Render("Logs"),
		statusLine,
		v.vp.View(),
		footer,
	}, "\n")
}

// ─── stream management ────────────────────────────────────────────────────────

// bodyHeight returns the number of rows available for the viewport.
// Title row (1) + status line (1) + footer (1) = 3 rows overhead.
func (v *logsView) bodyHeight() int {
	h := v.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

// startStream opens a new writer→channel pair, wires a cancellable context,
// launches the stream goroutine, and returns two Cmds.
//
// Termination design: the stream goroutine writes to the lineChanWriter and
// then calls writer.Close(), which closes the inner chan string. The
// listenLogLine cmd detects the closed channel and returns logsEndedMsg. This
// makes the listen loop the single source of termination notification,
// eliminating the race between the stream-goroutine's logsEndedMsg and
// outstanding logLineMsg deliveries.
//
// The stream error is communicated via a 1-buffered errCh that is sent to just
// before the writer is closed; listenLogLine reads from it in the closed-channel
// branch and attaches the error to logsEndedMsg.
func (v *logsView) startStream() tea.Cmd {
	// Cancel any previous stream.
	if v.cancelFunc != nil {
		v.cancelFunc()
	}

	// Reset buffer and viewport.
	v.buf = newRingBuffer(logsRingCap)
	v.streamEnded = false
	v.streamErr = nil

	writer, lineChan := newLineChanWriter()
	v.lineChan = lineChan

	// errCh carries the final stream error from the goroutine to listenLogLine.
	// Buffered 1 so the goroutine never blocks.
	errCh := make(chan error, 1)
	v.errCh = errCh

	ctx, cancel := context.WithCancel(context.Background())
	v.cancelFunc = cancel

	composeFile := v.composeFile
	service := v.activeService()
	streamFn := v.deps.logsStreamFn
	noColor := v.profile == styles.ProfileMono

	return tea.Batch(
		// Listen-loop: reads one line and re-issues itself.
		listenLogLineWithErr(lineChan, errCh),
		// Stream goroutine: writes lines then closes the writer channel.
		func() tea.Msg {
			err := streamFn(ctx, composeFile, service, logsDefaultTail, true, noColor, writer)
			// Context cancellation is a clean exit.
			if err != nil && ctx.Err() != nil {
				err = nil
			}
			// Send error (or nil) before closing so listenLogLine can read it.
			// This is safe because errCh is buffered(1) and only written once.
			errCh <- err
			writer.Close()
			// Return nil msg — the listen-loop sends logsEndedMsg on channel close.
			return nil
		},
	)
}

// cycleFilter advances the service filter, cancels the existing stream, and
// starts a new one with the updated service.
func (v *logsView) cycleFilter() tea.Cmd {
	v.filterIdx = (v.filterIdx + 1) % len(logsFilterCycle)
	v.paused = false
	return v.startStream()
}

// restartStream cancels any running stream and starts fresh.
func (v *logsView) restartStream() tea.Cmd {
	v.streamEnded = false
	v.streamErr = nil
	v.paused = false
	if v.composeFile == "" {
		// Re-resolve install dir first.
		return v.initCmd()
	}
	return v.startStream()
}

// ─── listen-loop cmds ─────────────────────────────────────────────────────────

// listenLogLineWithErr returns a tea.Cmd that reads ONE line from ch. It
// returns logLineMsg when a line is available, or logsEndedMsg when ch is
// closed. On channel-close, it reads the final error (if any) from errCh.
// errCh must be a 1-buffered channel that has already received the error
// before ch is closed.
func listenLogLineWithErr(ch <-chan string, errCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			// Channel closed: read the stream result error (non-blocking; it
			// was sent before Close() was called).
			var err error
			select {
			case err = <-errCh:
			default:
			}
			return logsEndedMsg{err: err}
		}
		return logLineMsg{line: line}
	}
}

// listenLogLine re-issues itself using the same errCh. Stored on the logsView
// so cycleFilter can create a new channel pair. This helper wraps
// listenLogLineWithErr for re-issue from the logLineMsg handler.
func listenLogLine(ch <-chan string, errCh <-chan error) tea.Cmd {
	return listenLogLineWithErr(ch, errCh)
}

// ─── newLogsView constructors ─────────────────────────────────────────────────

// newLogsView constructs a logsView with injectable deps.
func newLogsView(profile styles.Profile, deps logsViewDeps) *logsView {
	return &logsView{
		profile: profile,
		deps:    deps,
		buf:     newRingBuffer(logsRingCap),
	}
}
