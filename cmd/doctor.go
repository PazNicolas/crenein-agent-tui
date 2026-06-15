package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// doctorFn is the engine seam — replaced in tests.
var doctorFn = func(ctx context.Context, deps engine.Deps, opts engine.DoctorOptions) engine.DoctorReport {
	return engine.Run(ctx, deps, opts)
}

// doctorDeps holds injectable dependencies for the doctor command.
type doctorDeps struct {
	now func() time.Time
}

// newDoctorCmd constructs the `doctor` subcommand wired to real deps.
func newDoctorCmd() *cobra.Command {
	return newDoctorCmdWithDeps(doctorDeps{})
}

// newDoctorCmdWithDeps constructs the `doctor` subcommand with injectable deps.
func newDoctorCmdWithDeps(ddeps doctorDeps) *cobra.Command {
	var flagJSON bool

	cmd := &cobra.Command{
		Use:           "doctor",
		Short:         "Run diagnostic checks on the CRENEIN agent stack",
		Long:          "Runs a series of read-only diagnostic checks on the host and the agent stack.\nReturns exit code 0 when all checks pass, 1 when warnings are found, 2 when critical issues are found.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runDoctor(ctx, cmd, ddeps, flagJSON)
		},
	}

	cmd.Flags().BoolVar(&flagJSON, "json", false, "emit machine-readable JSON to stdout")

	return cmd
}

// runDoctor runs all doctor checks and renders output in human or JSON mode.
func runDoctor(
	ctx context.Context,
	cmd *cobra.Command,
	ddeps doctorDeps,
	jsonMode bool,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	nowFn := ddeps.now
	if nowFn == nil {
		nowFn = time.Now
	}

	// Build engine deps.
	fs := dockerx.NewOSFS()
	runner := dockerx.NewOSCommandRunner()

	// For doctor, compose detection failure is non-fatal — we still run checks.
	// Use a real CLIClient in both cases so that docker.daemon reflects the real
	// daemon state. Using FakeClient here would give a false-positive daemon pass.
	composeInfo, composeErr := detect.Compose(ctx, runner)
	var engineClient dockerx.Client
	if composeErr != nil {
		// Compose not found: fall back to ComposeV2 variant so docker.daemon
		// (which calls docker info) still works. The skip-graph handles compose absence.
		engineClient = dockerx.NewCLIClient(dockerx.ComposeV2)
	} else {
		engineClient = dockerx.NewCLIClient(composeInfo.Variant)
	}

	engineDeps := engine.Deps{
		Client: engineClient,
		Runner: runner,
		FS:     fs,
		// NOTE: connectivity checks (net.dockerhub, net.cnetwork_api) will use this
		// insecure prober too. This is acceptable — those checks don't transfer secrets
		// and connectivity detection remains valid regardless of TLS verification.
		Prober:   dockerx.NewHTTPProber(newInsecureHTTPClient()),
		Reporter: engine.DiscardReporter{},
	}

	opts := engine.DoctorOptions{}

	report := doctorFn(ctx, engineDeps, opts)

	if jsonMode {
		mp := NewMachinePresenter(stdout, stderr)
		doc := buildJSONDoc(report, nowFn())
		if err := mp.EmitJSON(doc); err != nil {
			mp.WriteError("error: failed to encode JSON: " + err.Error())
			return &exitCodeError{code: ExitDoctorCritical, err: err}
		}
	} else {
		renderHuman(stdout, report)
	}

	return doctorExitCode(report)
}

// doctorExitCode returns nil (exit 0), exit 1 (warnings), or exit 2 (critical).
func doctorExitCode(report engine.DoctorReport) error {
	var hasCritical, hasWarning bool
	for _, c := range report.Checks {
		if c.Status == engine.StatusCritical {
			hasCritical = true
		} else if c.Status == engine.StatusWarning {
			hasWarning = true
		}
	}
	if hasCritical {
		return &exitCodeError{code: ExitDoctorCritical, err: fmt.Errorf("critical issues found")}
	}
	if hasWarning {
		return &exitCodeError{code: ExitDoctorWarning, err: fmt.Errorf("warnings found")}
	}
	return nil
}

// renderHuman writes the human-readable doctor report to w.
func renderHuman(w io.Writer, report engine.DoctorReport) {
	var total, passed, warnings, critical, skipped int
	for _, c := range report.Checks {
		total++
		var marker string
		switch c.Status {
		case engine.StatusOK:
			marker = "✓"
			passed++
		case engine.StatusWarning:
			marker = "!"
			warnings++
		case engine.StatusCritical:
			marker = "✗"
			critical++
		case engine.StatusSkip:
			marker = "-"
			skipped++
		default:
			marker = "?"
		}
		fmt.Fprintf(w, "  %s  %-40s  %s\n", marker, c.Name, c.Detail) //nolint:errcheck
		if c.Status != engine.StatusOK && c.Status != engine.StatusSkip && c.FixSuggestion != "" {
			fmt.Fprintf(w, "     fix: %s\n", c.FixSuggestion) //nolint:errcheck
		}
	}
	fmt.Fprintf(w, "\nTotal: %d  Passed: %d  Warnings: %d  Critical: %d  Skipped: %d\n", //nolint:errcheck
		total, passed, warnings, critical, skipped)
}

// ─── JSON document shape ─────────────────────────────────────────────────────

// doctorJSONDoc is the top-level JSON document for doctor --json.
type doctorJSONDoc struct {
	SchemaVersion int               `json:"schema_version"`
	Command       string            `json:"command"`
	Timestamp     string            `json:"timestamp"`
	CLIVersion    string            `json:"cli_version"`
	Summary       doctorJSONSummary `json:"summary"`
	Checks        []doctorJSONCheck `json:"checks"`
}

type doctorJSONSummary struct {
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Passed   int    `json:"passed"`
	Warnings int    `json:"warnings"`
	Critical int    `json:"critical"`
	Skipped  int    `json:"skipped"`
}

type doctorJSONCheck struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Severity   string  `json:"severity"`
	Status     string  `json:"status"`
	Message    string  `json:"message"`
	Fix        *string `json:"fix"`
	DurationMs int64   `json:"duration_ms"`
}

// buildJSONDoc constructs the JSON document from a DoctorReport.
func buildJSONDoc(report engine.DoctorReport, ts time.Time) doctorJSONDoc {
	var total, passed, warnings, critical, skipped int
	jsonChecks := make([]doctorJSONCheck, 0, len(report.Checks))

	for _, c := range report.Checks {
		total++
		var statusStr string
		switch c.Status {
		case engine.StatusOK:
			statusStr = "pass"
			passed++
		case engine.StatusWarning:
			statusStr = "warn"
			warnings++
		case engine.StatusCritical:
			statusStr = "fail"
			critical++
		case engine.StatusSkip:
			statusStr = "skip"
			skipped++
		default:
			statusStr = "skip"
			skipped++
		}

		var fix *string
		if c.FixSuggestion != "" {
			s := c.FixSuggestion
			fix = &s
		}

		jsonChecks = append(jsonChecks, doctorJSONCheck{
			ID:         c.ID,
			Name:       c.Name,
			Severity:   strings.ToLower(string(c.Severity)),
			Status:     statusStr,
			Message:    c.Detail,
			Fix:        fix,
			DurationMs: c.DurationMs,
		})
	}

	// Summary status: ok/warning/critical from worst finding. SKIP doesn't elevate.
	var summaryStatus string
	switch report.Summary {
	case engine.StatusCritical:
		summaryStatus = "critical"
	case engine.StatusWarning:
		summaryStatus = "warning"
	default:
		summaryStatus = "ok"
	}

	cliVersion := build.version
	if cliVersion == "" {
		cliVersion = "dev"
	}

	return doctorJSONDoc{
		SchemaVersion: 1,
		Command:       "doctor",
		Timestamp:     ts.UTC().Format(time.RFC3339),
		CLIVersion:    cliVersion,
		Summary: doctorJSONSummary{
			Status:   summaryStatus,
			Total:    total,
			Passed:   passed,
			Warnings: warnings,
			Critical: critical,
			Skipped:  skipped,
		},
		Checks: jsonChecks,
	}
}
