package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Fixed service list ────────────────────────────────────────────────────────

// logsValidServices is the fixed ordered list of CRENEIN agent stack services.
var logsValidServices = []string{"agent", "frontend", "mongodb", "influxdb", "redis"}

// ─── Dependency seam ──────────────────────────────────────────────────────────

// logsDeps holds injectable dependencies for the logs command.
// The zero value does NOT wire real implementations — use newLogsDepsReal()
// for production and supply a fully-constructed value in tests.
type logsDeps struct {
	// composeLogsStream streams compose logs to the provided writer.
	composeLogsStream func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error

	// readFile reads a file from the filesystem (for docker-compose.yml detection).
	readFile func(name string) ([]byte, error)

	// readDir lists directory entries (for /home/* discovery).
	readDir func(path string) ([]string, error)

	// installDir overrides installation directory detection when non-empty.
	installDir string
}

// ─── Real deps constructor ────────────────────────────────────────────────────

// newLogsDepsReal constructs real-production logsDeps.
func newLogsDepsReal() logsDeps {
	fs := dockerx.NewOSFS()
	runner := dockerx.NewOSCommandRunner()

	composeClient := func(ctx context.Context) dockerx.Client {
		variant := dockerx.ComposeV2
		if info, err := detect.Compose(ctx, runner); err == nil {
			variant = info.Variant
		}
		return dockerx.NewCLIClient(variant)
	}

	return logsDeps{
		composeLogsStream: func(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error {
			return composeClient(ctx).ComposeLogsStream(ctx, composeFile, service, tail, follow, noColor, stdout)
		},
		readFile: fs.ReadFile,
		readDir:  fs.ReadDir,
	}
}

// ─── Command constructor ──────────────────────────────────────────────────────

// newLogsCmd constructs the `logs` subcommand wired to real deps.
func newLogsCmd() *cobra.Command {
	return newLogsCmdWithDeps(newLogsDepsReal())
}

// newLogsCmdWithDeps constructs the `logs` subcommand with injectable deps.
func newLogsCmdWithDeps(deps logsDeps) *cobra.Command {
	var (
		flagFollow bool
		flagTail   int
	)

	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Stream or tail logs from the CRENEIN agent stack",
		Long: "Streams or tails compose logs for the CRENEIN agent stack.\n\n" +
			"Valid services: " + strings.Join(logsValidServices, ", ") + ".\n" +
			"Without a service argument, logs from all services are shown.\n\n" +
			"With --follow (-f) the command streams continuously until interrupted\n" +
			"(SIGINT/SIGTERM); exit code is 0 on clean interrupt.",
		Args:          cobra.MaximumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runLogs(ctx, cmd, deps, args, flagFollow, flagTail)
		},
	}

	cmd.Flags().BoolVarP(&flagFollow, "follow", "f", false, "stream logs continuously (follow mode)")
	cmd.Flags().IntVar(&flagTail, "tail", 100, "number of lines to show from the end of the logs")
	return cmd
}

// runLogs executes the logs logic.
func runLogs(
	ctx context.Context,
	cmd *cobra.Command,
	deps logsDeps,
	args []string,
	follow bool,
	tail int,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()

	// Validate positional service argument.
	service := ""
	if len(args) > 0 {
		service = args[0]
		if !isValidLogsService(service) {
			WriteError(stderr, "error: unknown service %q\n", service)
			WriteError(stderr, "valid services: %s\n", strings.Join(logsValidServices, ", "))
			return usageError(fmt.Sprintf("unknown service %q; valid services: %s", service, strings.Join(logsValidServices, ", ")))
		}
	}

	// Detect install dir (same logic as status).
	installDir := resolveInstallDir(deps.readFile, deps.readDir, deps.installDir)
	if installDir == "" {
		WriteError(stderr, "no CRENEIN installation found (no docker-compose.yml referencing crenein/c-network-agent-back in . or /root or /home/*/)\n")
		WriteError(stderr, "hint: run `crenein-agent install` to set up the agent stack\n")
		return preflightError(fmt.Errorf("no installation found"))
	}

	composeFile := installDir + "/docker-compose.yml"

	// Determine color: noColor when stdout is not a TTY or color is disabled.
	tty := DetectTTY()
	noColor := globalFlags.noColor || !tty.StdoutIsTTY || os.Getenv("NO_COLOR") != ""

	// In follow mode, set up signal handling to cancel the context on
	// SIGINT/SIGTERM, so the child compose process is terminated cleanly.
	if follow {
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigCh)

		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-cancelCtx.Done():
			}
		}()

		ctx = cancelCtx
	}

	if err := deps.composeLogsStream(ctx, composeFile, service, tail, follow, noColor, stdout); err != nil {
		WriteError(stderr, "error: %v\n", err)
		return opFailureError(err)
	}
	return nil
}

// isValidLogsService reports whether service is in the valid service list.
func isValidLogsService(service string) bool {
	for _, s := range logsValidServices {
		if s == service {
			return true
		}
	}
	return false
}
