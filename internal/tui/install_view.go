package tui

// install_view.go — Install Wizard (Lote 3: tasks 3.1–3.6)
//
// State machine:
//   stepExistingGuard  (if existing install detected — task 3.2)
//   stepChecks         (system checks — task 3.1)
//   stepConfig         (configuration form — task 3.3)
//   stepPreview        (preview of planned actions — task 3.4)
//   stepExecution      (live engine events — task 3.5)
//   stepSummary        (access summary — task 3.6)

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
	"github.com/PazNicolas/crenein-agent-tui/internal/status"
	"github.com/PazNicolas/crenein-agent-tui/internal/tui/styles"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// defaultHTTPProber returns a lenient HTTP prober for connectivity checks.
func defaultHTTPProber() dockerx.HTTPProber {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	return dockerx.NewHTTPProber(client)
}

// ─── wizard step constants ────────────────────────────────────────────────────

type installStep int

const (
	stepExistingGuard installStep = iota
	stepChecks
	stepConfig
	stepPreview
	stepExecution
	stepSummary
)

// ─── internal messages ────────────────────────────────────────────────────────

// installGuardCheckedMsg is sent after the existing-installation guard check finishes.
type installGuardCheckedMsg struct {
	installDir string // non-empty = existing install found
}

// sysChecksResultMsg is sent when all system checks complete.
type sysChecksResultMsg struct {
	checks []checkResult
}

// checkResult holds the outcome of one system check.
type checkResult struct {
	name    string
	fatal   bool   // fatal failures block advancing
	ok      bool   // true = check passed
	warn    bool   // true = non-fatal warning
	message string // human-readable detail
	fix     string // fix suggestion when not ok
}

// installFinishedMsg is sent when the engine.Install goroutine returns.
type installFinishedMsg struct {
	res *engine.InstallResult
	err error
}

// opRunningMsg signals the root model that an engine operation has started/stopped.
type opRunningMsg struct{ running bool }

// spinnerTickMsg drives the loading-spinner animation on the active execution step.
type spinnerTickMsg struct{}

// spinnerTickCmd schedules the next spinner frame.
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// ─── installViewDeps — injection seam for testability ────────────────────────

// installViewDeps holds injectable dependencies for the install wizard.
// Real production code wires real implementations; tests supply fakes.
type installViewDeps struct {
	// installFn calls the real or fake engine.Install.
	installFn func(ctx context.Context, deps engine.Deps, opts engine.InstallOptions) (*engine.InstallResult, error)

	// readFile and readDir are used for existing-install detection.
	// readDir returns entry names, matching status.ResolveInstallDir's signature.
	readFile func(name string) ([]byte, error)
	readDir  func(name string) ([]string, error)
}

// ─── config form field indices ────────────────────────────────────────────────

const (
	fieldAdminEmail = iota
	fieldAdminPassword
	fieldAPIURL
	fieldAPIToken
	numEditableFields
)

// ─── installView ─────────────────────────────────────────────────────────────

// installView is the Install Wizard Bubbletea sub-model.
type installView struct {
	version string
	profile styles.Profile
	deps    installViewDeps

	// wizard step
	step installStep

	// spinner tick counter (for "loading" animation)
	spinnerTick int

	// ── stepExistingGuard ──
	existingDir string // non-empty = block

	// ── stepChecks ──
	checksRunning   bool
	checks          []checkResult
	checksHaveFatal bool

	// ── stepConfig ──
	inputs       [numEditableFields]textinput.Model
	focusedField int
	configErrors [numEditableFields]string

	// ── stepPreview ──
	previewConfirmed bool
	mongoImage       string // resolved from checks

	// ── stepExecution ──
	execSteps  []execStepState
	execCtx    context.Context //nolint:containedctx
	execCancel context.CancelFunc
	execDone   bool
	execError  error
	execResult *engine.InstallResult
	execCh     <-chan engine.Event // receive channel for ListenEngine

	// ── stepSummary ──
	// (uses execResult)

	width  int
	height int
}

// execStepState tracks one engine step in the execution view.
type execStepState struct {
	name   string
	status string // "pending" | "running" | "done" | "failed" | "skipped"
	errMsg string
	fix    string
}

// orderedExecSteps is the canonical order of engine steps to display.
var orderedExecSteps = []string{
	"preflight",
	"system-prep",
	"directories",
	"ftp-tftp-config",
	"backups-user",
	"env-file",
	"certificates",
	"stack-up",
	"service-verification",
	"influx-health",
	"admin-user",
	"influx-buckets",
}

// ─── constructors ─────────────────────────────────────────────────────────────

// newInstallView constructs an installView with injectable deps.
func newInstallView(version string, profile styles.Profile, deps installViewDeps) *installView {
	v := &installView{
		version: version,
		profile: profile,
		deps:    deps,
		step:    stepExistingGuard,
	}
	v.initInputs()
	v.initExecSteps()
	return v
}

// NewModelWithInstallDeps creates a root tea.Model with injectable install deps.
// Keep NewModel and NewModelWithStatusDeps unchanged — this is additive.
func NewModelWithInstallDeps(version string, profile styles.Profile, ideps installViewDeps) tea.Model {
	d := status.NewDepsReal()
	mc := newRealManifestClient()
	sv := newStatusView(version, profile, d, mc)
	iv := newInstallView(version, profile, ideps)
	uv := newUpdateView(version, profile, updateViewDeps{
		updateFn:       engine.Update,
		manifestClient: mc,
	})
	return Model{
		version: version,
		profile: profile,
		views: map[ViewID]tea.Model{
			ViewStatus:  sv,
			ViewInstall: iv,
			ViewUpdate:  uv,
			ViewDoctor:  newDoctorViewReal(profile),
			ViewLogs:    newLogsViewReal(profile),
		},
		stack: []ViewID{ViewStatus},
	}
}

// ─── textinput initialisation ─────────────────────────────────────────────────

func (v *installView) initInputs() {
	labels := [numEditableFields]string{
		"Admin email",
		"Admin password",
		"API URL",
		"API token",
	}
	defaults := [numEditableFields]string{
		"admin@example.com",
		"admin123",
		"http://localhost:8000",
		"your-api-token-here",
	}

	for i := 0; i < numEditableFields; i++ {
		ti := textinput.New()
		ti.Placeholder = labels[i]
		ti.SetValue(defaults[i])
		ti.Width = 40
		if i == fieldAdminPassword {
			ti.EchoMode = textinput.EchoPassword
		}
		v.inputs[i] = ti
	}
	// Focus first field
	cmd := v.inputs[0].Focus()
	_ = cmd
}

// ─── exec steps initialisation ───────────────────────────────────────────────

func (v *installView) initExecSteps() {
	v.execSteps = make([]execStepState, len(orderedExecSteps))
	for i, name := range orderedExecSteps {
		v.execSteps[i] = execStepState{name: name, status: "pending"}
	}
}

// ─── tea.Model implementation ─────────────────────────────────────────────────

// Init kicks off the existing-installation guard check.
func (v *installView) Init() tea.Cmd {
	return v.guardCheckCmd()
}

// Update handles all messages for the install wizard.
func (v *installView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil

	// ── spinner tick (animates the active execution step) ──
	case spinnerTickMsg:
		// Keep ticking only while execution is in progress.
		if v.step == stepExecution && !v.execDone {
			v.spinnerTick++
			return v, spinnerTickCmd()
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)

	// ── guard check result ──
	case installGuardCheckedMsg:
		if msg.installDir != "" {
			v.existingDir = msg.installDir
			v.step = stepExistingGuard
		} else {
			v.step = stepChecks
			v.checksRunning = true
			return v, v.runChecksCmd()
		}
		return v, nil

	// ── system checks done ──
	case sysChecksResultMsg:
		v.checksRunning = false
		v.checks = msg.checks
		v.checksHaveFatal = false
		for _, c := range v.checks {
			if c.fatal && !c.ok {
				v.checksHaveFatal = true
				break
			}
		}
		// Resolve mongo image from AVX check
		v.mongoImage = detect.MongoImage(false) // default no-AVX
		for _, c := range v.checks {
			if c.name == "AVX" {
				if strings.Contains(c.message, "AVX supported") {
					v.mongoImage = detect.MongoImage(true)
				}
				break
			}
		}
		return v, nil

	// ── engine step events ──
	case StepStartedMsg:
		v.markExecStep(msg.Step, "running", "", "")
		return v, v.listenExecCh()

	case StepDoneMsg:
		v.markExecStep(msg.Step, "done", "", "")
		return v, v.listenExecCh()

	case StepFailedMsg:
		fix := ""
		var cnerErr *cnerr.Error
		if errors.As(msg.Err, &cnerErr) {
			fix = cnerErr.FixSuggestion
		}
		v.markExecStep(msg.Step, "failed", msg.Err.Error(), fix)
		// Mark remaining pending steps as skipped
		v.markRemainingSkipped(msg.Step)
		return v, v.listenExecCh()

	case StepProgressMsg:
		return v, v.listenExecCh()

	case OperationFinishedMsg:
		// Engine channel closed — wait for installFinishedMsg
		return v, nil

	// ── install goroutine finished ──
	case installFinishedMsg:
		v.execDone = true
		v.execResult = msg.res
		v.execError = msg.err
		if v.execCancel != nil {
			v.execCancel()
		}
		if msg.err == nil {
			v.step = stepSummary
		}
		// Signal root that operation is done
		return v, func() tea.Msg { return opRunningMsg{running: false} }
	}

	return v, nil
}

// View renders the current wizard step.
func (v *installView) View() string {
	switch v.step {
	case stepExistingGuard:
		if v.existingDir != "" {
			return v.renderExistingGuard()
		}
		return v.profile.ActiveViewStyle().Render("Checking for existing installation…")
	case stepChecks:
		return v.renderChecks()
	case stepConfig:
		return v.renderConfig()
	case stepPreview:
		return v.renderPreview()
	case stepExecution:
		return v.renderExecution()
	case stepSummary:
		return v.renderSummary()
	}
	return v.profile.ActiveViewStyle().Render("Install Wizard")
}

// ─── key handling ─────────────────────────────────────────────────────────────

func (v *installView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.step {

	case stepChecks:
		switch msg.String() {
		case "enter", " ":
			// If no fatal failures, advance to config
			if !v.checksHaveFatal && len(v.checks) > 0 {
				v.step = stepConfig
			}
			return v, nil
		case "r":
			// Re-run checks
			v.checks = nil
			v.checksRunning = true
			v.checksHaveFatal = false
			return v, v.runChecksCmd()
		}

	case stepConfig:
		switch msg.String() {
		case "tab", "down":
			v.inputs[v.focusedField].Blur()
			v.focusedField = (v.focusedField + 1) % numEditableFields
			return v, v.inputs[v.focusedField].Focus()
		case "shift+tab", "up":
			v.inputs[v.focusedField].Blur()
			v.focusedField = (v.focusedField - 1 + numEditableFields) % numEditableFields
			return v, v.inputs[v.focusedField].Focus()
		case "enter":
			// Validate all fields
			v.validateConfig()
			anyErr := false
			for _, e := range v.configErrors {
				if e != "" {
					anyErr = true
					break
				}
			}
			if !anyErr {
				v.step = stepPreview
			}
			return v, nil
		default:
			// Forward to focused input
			var cmds []tea.Cmd
			var cmd tea.Cmd
			v.inputs[v.focusedField], cmd = v.inputs[v.focusedField].Update(msg)
			cmds = append(cmds, cmd)
			// Clear error for this field on edit
			v.configErrors[v.focusedField] = ""
			return v, tea.Batch(cmds...)
		}

	case stepPreview:
		switch msg.String() {
		case "enter", "y":
			v.previewConfirmed = true
			v.step = stepExecution
			return v, v.startExecutionCmd()
		}

	case stepSummary:
		// Any key navigates back
		switch msg.String() {
		case "esc", "q", "enter":
			return v, func() tea.Msg { return NavigateBackMsg{} }
		}

	case stepExecution:
		// Execution in progress — keys handled by root for quit confirmation
	}

	return v, nil
}

// ─── validation ───────────────────────────────────────────────────────────────

func (v *installView) validateConfig() {
	// Admin email
	email := v.inputs[fieldAdminEmail].Value()
	if _, err := mail.ParseAddress(email); err != nil || email == "" {
		v.configErrors[fieldAdminEmail] = "Invalid email address"
	} else {
		v.configErrors[fieldAdminEmail] = ""
	}

	// Admin password
	if v.inputs[fieldAdminPassword].Value() == "" {
		v.configErrors[fieldAdminPassword] = "Password cannot be empty"
	} else {
		v.configErrors[fieldAdminPassword] = ""
	}

	// API URL
	apiURL := v.inputs[fieldAPIURL].Value()
	if apiURL == "" || (!strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://")) {
		v.configErrors[fieldAPIURL] = "URL must start with http:// or https://"
	} else {
		v.configErrors[fieldAPIURL] = ""
	}

	// API token — any non-empty value
	v.configErrors[fieldAPIToken] = ""
}

// ─── commands ─────────────────────────────────────────────────────────────────

// guardCheckCmd checks for an existing installation.
func (v *installView) guardCheckCmd() tea.Cmd {
	readFile := v.deps.readFile
	readDir := v.deps.readDir

	// Provide no-op fallbacks so we never nil-deref.
	readFileAdapt := func(name string) ([]byte, error) {
		if readFile != nil {
			return readFile(name)
		}
		return nil, fmt.Errorf("no readFile")
	}
	readDirAdapt := func(path string) ([]string, error) {
		if readDir != nil {
			return readDir(path)
		}
		return nil, fmt.Errorf("no readDir")
	}

	return func() tea.Msg {
		dir := status.ResolveInstallDir(readFileAdapt, readDirAdapt, "")
		return installGuardCheckedMsg{installDir: dir}
	}
}

// runChecksCmd runs all system detectors concurrently-ish in a single goroutine
// (engine detectors are fast synchronous calls against injected fakes in tests).
func (v *installView) runChecksCmd() tea.Cmd {
	return func() tea.Msg {
		checks := runSystemChecks()
		return sysChecksResultMsg{checks: checks}
	}
}

// runSystemChecks runs the system detectors and returns a slice of checkResult.
// In production this runs real detect functions; tests can override via startExecutionCmd
// with a fake installFn that never calls these checks.
func runSystemChecks() []checkResult {
	ctx := context.Background()
	var results []checkResult

	// ── Distro / OS check ──
	fs := dockerx.NewOSFS()
	distro, err := detect.Distro(ctx, fs)
	if err != nil {
		fix := ""
		var cnerErr *cnerr.Error
		if errors.As(err, &cnerErr) {
			fix = cnerErr.FixSuggestion
		}
		results = append(results, checkResult{
			name:    "Distro/OS",
			fatal:   true,
			ok:      false,
			message: err.Error(),
			fix:     fix,
		})
	} else {
		results = append(results, checkResult{
			name:    "Distro/OS",
			ok:      true,
			message: "OS: " + distro.PrettyName,
		})
	}

	// ── AVX check ──
	hasAVX, avxErr := detect.AVX(ctx, fs)
	if avxErr != nil {
		results = append(results, checkResult{
			name:    "AVX",
			ok:      false,
			warn:    true, // non-fatal — falls back to mongo:4.4
			message: "AVX detection failed; defaulting to mongo:4.4",
		})
	} else if hasAVX {
		results = append(results, checkResult{
			name:    "AVX",
			ok:      true,
			message: "AVX supported → mongodb/mongodb-community-server:7.0-ubuntu2204",
		})
	} else {
		results = append(results, checkResult{
			name:    "AVX",
			ok:      false,
			warn:    true,
			message: "No AVX → mongo:4.4 will be used",
			fix:     "CPU does not support AVX (required for MongoDB ≥5.0). Using mongo:4.4.",
		})
	}

	// ── Docker installed + daemon check ──
	runner := dockerx.NewOSCommandRunner()
	client := dockerx.NewCLIClient(dockerx.ComposeV2)
	dockerInfo := detect.Docker(ctx, runner, client)
	if !dockerInfo.Installed {
		results = append(results, checkResult{
			name:    "Docker installed",
			fatal:   true,
			ok:      false,
			message: "Docker is not installed",
			fix:     "Install Docker: apt-get install docker-ce docker-ce-cli containerd.io docker-compose-plugin",
		})
	} else {
		results = append(results, checkResult{
			name:    "Docker installed",
			ok:      true,
			message: "Docker binary found",
		})
	}

	if dockerInfo.Installed && !dockerInfo.DaemonRunning {
		results = append(results, checkResult{
			name:    "Docker daemon",
			fatal:   true,
			ok:      false,
			message: "Docker daemon is not running",
			fix:     "Start the daemon: systemctl start docker",
		})
	} else if dockerInfo.Installed {
		results = append(results, checkResult{
			name:    "Docker daemon",
			ok:      true,
			message: "Docker daemon is running",
		})
	}

	// ── Compose v1/v2 check ──
	composeInfo, composeErr := detect.Compose(ctx, runner)
	if composeErr != nil {
		fix := ""
		var cnerErr *cnerr.Error
		if errors.As(composeErr, &cnerErr) {
			fix = cnerErr.FixSuggestion
		}
		results = append(results, checkResult{
			name:    "Docker Compose",
			fatal:   true,
			ok:      false,
			message: "Docker Compose not found",
			fix:     fix,
		})
	} else {
		results = append(results, checkResult{
			name:    "Docker Compose",
			ok:      true,
			message: "Compose variant: " + composeInfo.Variant.String(),
		})
	}

	// ── Disk space check ──
	freeMB, diskErr := detect.DiskSpace(ctx, "/")
	if diskErr != nil {
		fix := ""
		var cnerErr *cnerr.Error
		if errors.As(diskErr, &cnerErr) {
			fix = cnerErr.FixSuggestion
		}
		results = append(results, checkResult{
			name:    "Disk space",
			fatal:   true,
			ok:      false,
			message: fmt.Sprintf("Insufficient disk space (%d MB free, need %d MB)", freeMB, detect.MinDiskMB),
			fix:     fix,
		})
	} else {
		results = append(results, checkResult{
			name:    "Disk space",
			ok:      true,
			message: fmt.Sprintf("%d MB free", freeMB),
		})
	}

	// ── Connectivity check ──
	prober := defaultHTTPProber()
	connResults, connErr := detect.Connectivity(ctx, prober, detect.DefaultConnectivityURLs)
	if connErr != nil {
		results = append(results, checkResult{
			name:    "Connectivity",
			fatal:   true,
			ok:      false,
			message: "Connectivity check failed: " + connErr.Error(),
			fix:     "Check firewall and outbound HTTPS access",
		})
	} else {
		allReachable := true
		for _, r := range connResults {
			if !r.Reachable {
				allReachable = false
				break
			}
		}
		if allReachable {
			results = append(results, checkResult{
				name:    "Connectivity",
				ok:      true,
				message: "All endpoints reachable",
			})
		} else {
			results = append(results, checkResult{
				name:    "Connectivity",
				fatal:   true,
				ok:      false,
				message: "One or more endpoints unreachable",
				fix:     "Check firewall and outbound HTTPS access to Docker Hub and crenein.com",
			})
		}
	}

	// ── Permissions / root check ──
	permInfo, permErr := detect.Permissions(ctx, runner)
	if permErr != nil {
		results = append(results, checkResult{
			name:    "Root permissions",
			fatal:   true,
			ok:      false,
			message: "Permissions check failed: " + permErr.Error(),
			fix:     "Re-run as root: sudo ./crenein-agent install",
		})
	} else if !permInfo.IsRoot {
		results = append(results, checkResult{
			name:    "Root permissions",
			fatal:   true,
			ok:      false,
			message: "Not running as root",
			fix:     "Re-run as root: sudo ./crenein-agent install",
		})
	} else {
		results = append(results, checkResult{
			name:    "Root permissions",
			ok:      true,
			message: "Running as root",
		})
	}

	return results
}

// listenExecCh returns a ListenEngine Cmd consuming v.execCh.
func (v *installView) listenExecCh() tea.Cmd {
	ch := v.execCh
	return ListenEngine(ch)
}

// startExecutionCmd starts the engine install goroutine and the listen loop.
func (v *installView) startExecutionCmd() tea.Cmd {
	v.initExecSteps()

	reporter, ch := NewChanReporter(64)
	v.execCh = ch

	ctx, cancel := context.WithCancel(context.Background())
	v.execCtx = ctx
	v.execCancel = cancel

	// Build engine.Deps with real implementations (replaced in tests via installFn fake)
	deps := engine.Deps{
		Client:   dockerx.NewCLIClient(dockerx.ComposeV2),
		Runner:   dockerx.NewOSCommandRunner(),
		FS:       dockerx.NewOSFS(),
		Prober:   defaultHTTPProber(),
		Reporter: reporter,
	}

	opts := engine.InstallOptions{
		AdminEmail:    v.inputs[fieldAdminEmail].Value(),
		AdminPassword: v.inputs[fieldAdminPassword].Value(),
		APIURL:        v.inputs[fieldAPIURL].Value(),
		APIToken:      v.inputs[fieldAPIToken].Value(),
	}
	if v.mongoImage != "" {
		opts.MongoImageOverride = v.mongoImage
	}

	installFn := v.deps.installFn
	if installFn == nil {
		installFn = engine.Install
	}

	listenCmd := v.listenExecCh()

	return tea.Batch(
		// Signal root that operation is running
		func() tea.Msg { return opRunningMsg{running: true} },
		// Animate the active-step spinner
		spinnerTickCmd(),
		// Listen loop (Cmd A): reads one event per invocation
		listenCmd,
		// Engine goroutine (Cmd B): runs install, closes channel when done
		func() tea.Msg {
			res, err := installFn(ctx, deps, opts)
			reporter.Close()
			return installFinishedMsg{res: res, err: err}
		},
	)
}

// markExecStep updates a step's status by name.
func (v *installView) markExecStep(name, status, errMsg, fix string) {
	for i := range v.execSteps {
		if v.execSteps[i].name == name {
			v.execSteps[i].status = status
			v.execSteps[i].errMsg = errMsg
			v.execSteps[i].fix = fix
			return
		}
	}
}

// markRemainingSkipped marks steps after the failed step as skipped.
func (v *installView) markRemainingSkipped(failedStep string) {
	past := false
	for i := range v.execSteps {
		if v.execSteps[i].name == failedStep {
			past = true
			continue
		}
		if past && v.execSteps[i].status == "pending" {
			v.execSteps[i].status = "skipped"
		}
	}
}

// ─── renderers ───────────────────────────────────────────────────────────────

func (v *installView) renderExistingGuard() string {
	p := v.profile
	title := p.TitleStyle().Render("Existing Installation Detected")
	lines := []string{
		title,
		"",
		fmt.Sprintf("An existing CRENEIN agent installation was found at:  %s", v.existingDir),
		"",
		"Installing over an existing installation is NOT permitted.",
		"This wizard will NOT overwrite docker-compose.yml, .env, or /data/* files.",
		"",
		"To update the agent, press  u  to open the Update wizard instead.",
		"To return to the status screen, press  esc.",
	}
	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *installView) renderChecks() string {
	p := v.profile
	title := p.TitleStyle().Render("Step 1 of 5 — System Checks")
	lines := []string{title, ""}

	if v.checksRunning {
		lines = append(lines, "Running system checks…")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	if len(v.checks) == 0 {
		lines = append(lines, "Preparing checks…")
		return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
	}

	for _, c := range v.checks {
		var glyph string
		if c.ok {
			glyph = p.Glyph(styles.GlyphOK)
		} else if c.warn {
			glyph = p.Glyph(styles.GlyphWarn)
		} else {
			glyph = p.Glyph(styles.GlyphFail)
		}
		line := fmt.Sprintf("%s  %-20s  %s", glyph, c.name, c.message)
		lines = append(lines, line)
		if !c.ok && c.fix != "" {
			lines = append(lines, fmt.Sprintf("         Fix: %s", c.fix))
		}
	}

	lines = append(lines, "")

	if v.checksHaveFatal {
		lines = append(lines, "One or more FATAL checks failed. Fix the issues above and press  r  to re-run.")
		lines = append(lines, "Press  esc  to return to Status.")
	} else {
		// Show mongo image selection info
		lines = append(lines, fmt.Sprintf("MongoDB image selected: %s", v.mongoImage))
		lines = append(lines, "")
		lines = append(lines, "All checks passed. Press  enter  to continue to configuration.")
	}

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *installView) renderConfig() string {
	p := v.profile
	title := p.TitleStyle().Render("Step 2 of 5 — Configuration")
	lines := []string{title, "", "Editable fields (tab/shift+tab to move, enter to confirm):"}

	fieldLabels := [numEditableFields]string{
		"Admin email   ",
		"Admin password",
		"API URL       ",
		"API token     ",
	}

	for i := 0; i < numEditableFields; i++ {
		prefix := "  "
		if i == v.focusedField {
			prefix = "> "
		}
		lines = append(lines, fmt.Sprintf("%s%s: %s", prefix, fieldLabels[i], v.inputs[i].View()))
		if v.configErrors[i] != "" {
			lines = append(lines, fmt.Sprintf("          ERROR: %s", v.configErrors[i]))
		}
	}

	lines = append(lines, "")
	lines = append(lines, "Read-only settings (auto-generated on install):")
	lines = append(lines, "  MongoDB password : •••••••• (auto-generated, written to .env on install)")
	lines = append(lines, "  Redis password   : •••••••• (auto-generated, written to .env on install)")
	lines = append(lines, "  Mongo user       : cnetwork_admin")
	lines = append(lines, "  Ports            : 8000 / 8443 / 80 / 443 / 8086")
	lines = append(lines, "  Data root        : /data")
	lines = append(lines, "")
	lines = append(lines, "Press  enter  to preview the installation plan.")

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *installView) renderPreview() string {
	p := v.profile
	title := p.TitleStyle().Render("Step 3 of 5 — Preview")
	lines := []string{title, "", "The following actions will be performed:", ""}

	lines = append(lines, "Packages to install:")
	lines = append(lines,
		"  apt-transport-https ca-certificates curl gnupg lsb-release fping vsftpd tftpd-hpa jq")
	if !detectDockerAlreadyInstalled() {
		lines = append(lines, "  docker-ce docker-ce-cli containerd.io docker-compose-plugin")
	}

	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("MongoDB image: %s", v.mongoImage))
	lines = append(lines, "")
	lines = append(lines, "Files to be written:")
	lines = append(lines, "  ./docker-compose.yml")
	lines = append(lines, "  ./.env  (keys: INFLUXDB_TOKEN, MONGODB_INITDB_ROOT_USERNAME,")
	lines = append(lines, "           MONGODB_INITDB_ROOT_PASSWORD, REDIS_PASSWORD,")
	lines = append(lines, "           CNETWORK_API_URL, CNETWORK_API_TOKEN)")
	lines = append(lines, "  (Secret values will NOT be shown here)")
	lines = append(lines, "")
	lines = append(lines, "Directories to create:")
	lines = append(lines, "  /data/mongodb   /data/influxdb2   /data/redis   /data/files")
	lines = append(lines, "")
	lines = append(lines, "Certificates to generate:")
	lines = append(lines, "  ./c-network-agent-back/certs/  (cert.pem + key.pem, 365 days)")
	lines = append(lines, "  ./c-network-agent-front/certs/ (cert.pem + key.pem, 365 days)")
	lines = append(lines, "")
	lines = append(lines, "Press  enter  or  y  to confirm and begin installation.")
	lines = append(lines, "Press  esc  to go back.")

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

// detectDockerAlreadyInstalled is a best-effort check for preview text.
func detectDockerAlreadyInstalled() bool {
	runner := dockerx.NewOSCommandRunner()
	_, err := runner.LookPath("docker")
	return err == nil
}

func (v *installView) renderExecution() string {
	p := v.profile
	title := p.TitleStyle().Render("Step 4 of 5 — Installing…")
	lines := []string{title, ""}

	spinner := []string{"|", "/", "-", "\\"}[v.spinnerTick%4]

	for _, s := range v.execSteps {
		var marker string
		switch s.status {
		case "done":
			marker = p.Glyph(styles.GlyphOK)
		case "failed":
			marker = p.Glyph(styles.GlyphFail)
		case "running":
			marker = spinner
		case "skipped":
			marker = "  —  "
		default: // pending
			marker = "     "
		}
		lines = append(lines, fmt.Sprintf("  %s  %s", marker, s.name))
		if s.status == "failed" && s.errMsg != "" {
			lines = append(lines, fmt.Sprintf("       Error: %s", s.errMsg))
			if s.fix != "" {
				lines = append(lines, fmt.Sprintf("       Fix:   %s", s.fix))
			}
		}
	}

	if v.execDone && v.execError != nil {
		lines = append(lines, "")
		lines = append(lines, p.Glyph(styles.GlyphFail)+" Installation failed. Press  esc  to return to Status.")
	}

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}

func (v *installView) renderSummary() string {
	p := v.profile
	title := p.TitleStyle().Render("Step 5 of 5 — Installation Complete")
	lines := []string{title, "", p.Glyph(styles.GlyphOK) + " Installation successful!", ""}

	if v.execResult != nil {
		if len(v.execResult.AccessSummary) > 0 {
			lines = append(lines, "Access Summary:")
			lines = append(lines, strings.Repeat("-", 50))
			for _, entry := range v.execResult.AccessSummary {
				lines = append(lines, fmt.Sprintf("  %-30s  %s", entry.Label+":", entry.Value))
			}
		}
		if len(v.execResult.Warnings) > 0 {
			lines = append(lines, "")
			lines = append(lines, "Warnings:")
			for _, w := range v.execResult.Warnings {
				lines = append(lines, "  "+p.Glyph(styles.GlyphWarn)+" "+w)
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, "Press  esc  or  enter  to return to Status.")

	return p.ActiveViewStyle().Render(strings.Join(lines, "\n"))
}
