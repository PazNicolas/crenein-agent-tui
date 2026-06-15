// Package tui provides the Bubbletea root model and supporting types for the
// crenein-agent interactive dashboard.
package tui

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/release"
	"github.com/PazNicolas/crenein-agent-tui/internal/status"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	tea "github.com/charmbracelet/bubbletea"
)

// ViewID identifies which pane is currently active.
type ViewID int

const (
	ViewStatus  ViewID = iota
	ViewInstall ViewID = iota
	ViewUpdate  ViewID = iota
	ViewDoctor  ViewID = iota
	ViewLogs    ViewID = iota
)

// viewName returns a human-readable label for a view.
func viewName(v ViewID) string {
	switch v {
	case ViewStatus:
		return "Status"
	case ViewInstall:
		return "Install"
	case ViewUpdate:
		return "Update"
	case ViewDoctor:
		return "Doctor"
	case ViewLogs:
		return "Logs"
	default:
		return "Unknown"
	}
}

// NavigateToMsg tells the root model to push a new view onto the stack.
type NavigateToMsg struct{ View ViewID }

// NavigateBackMsg tells the root model to pop the top view off the stack.
type NavigateBackMsg struct{}

// Model is the root Bubbletea model that owns the navigation stack and
// forwards messages to the currently active child view.
type Model struct {
	version   string
	width     int
	height    int
	views     map[ViewID]tea.Model
	stack     []ViewID // navigation stack; top element is the active view
	opRunning bool     // true while an engine operation is in progress
	quitting  bool     // true when quit-confirmation overlay is shown
	profile   styles.Profile
}

// newRealManifestClient constructs a production release.Client for the TUI.
// It mirrors the pattern used in status.NewDepsReal but keeps the responsibility
// of building the network client here so there is a single "real" entry point.
func newRealManifestClient() release.Client {
	insecureTransport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: insecureTransport}
	prober := dockerx.NewHTTPProber(httpClient)
	fs := dockerx.NewOSFS()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/root"
	}
	return release.NewManifestClient(prober, nil, fs, homeDir, time.Now)
}

// NewModel creates an initialised root model ready to be handed to tea.NewProgram.
// It wires real production dependencies for status collection and manifest fetching.
func NewModel(version string, profile styles.Profile) Model {
	deps := status.NewDepsReal()
	mc := newRealManifestClient()
	return NewModelWithStatusDeps(version, profile, deps, mc)
}

// NewModelWithStatusDeps creates a root model with injectable status deps and
// manifest client. Use this constructor in tests to supply fakes.
func NewModelWithStatusDeps(version string, profile styles.Profile, deps status.Deps, mc release.Client) Model {
	sv := newStatusView(version, profile, deps, mc)
	// Wire a real installView with OS-level deps.
	osFSInst := dockerx.NewOSFS()
	iv := newInstallView(version, profile, installViewDeps{
		installFn: engine.Install,
		readFile:  osFSInst.ReadFile,
		readDir:   osFSInst.ReadDir,
	})
	// Wire a real updateView with production manifest client and engine.
	uv := newUpdateView(version, profile, updateViewDeps{
		updateFn:       engine.Update,
		manifestClient: newRealManifestClient(),
	})
	// Wire a real doctorView with OS-level deps and the real engine.Run.
	dv := newDoctorViewReal(profile)
	// Wire a real logsView with production deps.
	lv := newLogsViewReal(profile)
	return Model{
		version: version,
		profile: profile,
		views: map[ViewID]tea.Model{
			ViewStatus:  sv,
			ViewInstall: iv,
			ViewUpdate:  uv,
			ViewDoctor:  dv,
			ViewLogs:    lv,
		},
		stack: []ViewID{ViewStatus},
	}
}

// newLogsViewReal returns a *logsView wired with real production OS-level deps.
func newLogsViewReal(profile styles.Profile) *logsView {
	osFS := dockerx.NewOSFS()
	runner := dockerx.NewOSCommandRunner()

	composeClient := func(ctx context.Context) dockerx.Client {
		variant := dockerx.ComposeV2
		if info, err := detect.Compose(ctx, runner); err == nil {
			variant = info.Variant
		}
		return dockerx.NewCLIClient(variant)
	}

	streamFn := func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error {
		return composeClient(ctx).ComposeLogsStream(ctx, composeFile, service, tail, follow, noColor, stdout)
	}

	return newLogsView(profile, logsViewDeps{
		logsStreamFn: streamFn,
		readFile:     osFS.ReadFile,
		readDir:      osFS.ReadDir,
	})
}

// activeView returns the ID of the topmost view on the stack.
func (m Model) activeView() ViewID {
	if len(m.stack) == 0 {
		return ViewStatus
	}
	return m.stack[len(m.stack)-1]
}

// Init satisfies tea.Model. Delegates to the active view's Init so that the
// Status view (or any other initial view) can kick off its loading cycle.
func (m Model) Init() tea.Cmd {
	active := m.activeView()
	if child, ok := m.views[active]; ok {
		return child.Init()
	}
	return nil
}

// Update handles all incoming messages and returns the updated model plus any
// command to run.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Propagate to active child view so it can reflow its content.
		active := m.activeView()
		if child, ok := m.views[active]; ok {
			updated, _ := child.Update(msg)
			m.views[active] = updated
		}
		return m, nil

	case opRunningMsg:
		m.opRunning = msg.running
		return m, nil

	case NavigateToMsg:
		m.stack = append(m.stack, msg.View)
		return m, nil

	case NavigateBackMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case tea.KeyMsg:
		// Quit-confirmation overlay intercepts all keys when quitting==true.
		if m.quitting {
			switch msg.String() {
			case "y":
				return m, tea.Quit
			case "n":
				m.quitting = false
				return m, nil
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			if m.opRunning {
				m.quitting = true
				return m, nil
			}
			return m, tea.Quit

		case "s":
			if !m.opRunning {
				m.stack = append(m.stack, ViewStatus)
			}
			return m, nil

		case "i":
			if !m.opRunning {
				m.stack = append(m.stack, ViewInstall)
			}
			return m, nil

		case "u":
			if !m.opRunning {
				m.stack = append(m.stack, ViewUpdate)
			}
			return m, nil

		case "d":
			if !m.opRunning {
				m.stack = append(m.stack, ViewDoctor)
			}
			return m, nil

		case "l":
			if !m.opRunning {
				m.stack = append(m.stack, ViewLogs)
			}
			return m, nil

		case "esc":
			if !m.opRunning {
				if len(m.stack) > 1 {
					m.stack = m.stack[:len(m.stack)-1]
				}
				// If stack is now empty or has only root, ensure ViewStatus is present.
				if len(m.stack) == 0 {
					m.stack = []ViewID{ViewStatus}
				}
			}
			return m, nil
		}

	}

	// Forward unhandled messages to the active child view.
	active := m.activeView()
	if child, ok := m.views[active]; ok {
		updated, cmd := child.Update(msg)
		m.views[active] = updated
		return m, cmd
	}
	return m, nil
}

// View renders the full TUI frame.
func (m Model) View() string {
	// Minimum terminal size guard.
	if m.width < 80 || m.height < 24 {
		return fmt.Sprintf(
			"Terminal too small (have %dx%d, need 80x24)\n",
			m.width, m.height,
		)
	}

	active := m.activeView()

	// Header line.
	headerText := fmt.Sprintf("crenein-agent %s | %s", m.version, viewName(active))
	header := m.profile.HeaderStyle().Render(headerText)

	// Footer line.
	footer := m.profile.FooterStyle().Render(
		"s:Status  i:Install  u:Update  d:Doctor  l:Logs  esc:Back  q:Quit",
	)

	// Body: active view gets height-2 rows.
	var body string
	if child, ok := m.views[active]; ok {
		body = child.View()
	}

	// Quit-confirmation overlay appended when needed.
	if m.quitting {
		lines := strings.Split(body, "\n")
		// Pad body to height-2 lines so the confirmation is always at the bottom.
		for len(lines) < m.height-2 {
			lines = append(lines, "")
		}
		lines = append(lines, "Operation running. Press y to confirm quit, n to cancel.")
		body = strings.Join(lines, "\n")
	}

	return header + "\n" + body + "\n" + footer
}
