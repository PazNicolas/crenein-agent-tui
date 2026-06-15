package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/detect"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/engine"
)

// installFn is the engine.Install call seam. Tests replace it with a fake.
var installFn = func(ctx context.Context, deps engine.Deps, opts engine.InstallOptions) (*engine.InstallResult, error) {
	return engine.Install(ctx, deps, opts)
}

// installDeps holds injectable dependencies for the install command.
// The zero value wires real implementations.
type installDeps struct {
	// avxDetect overrides AVX detection. When nil, detect.AVX(ctx, fs) is called.
	avxDetect func(ctx context.Context, fs dockerx.FS) (bool, error)
	// stdinIsTTY overrides the stdin TTY detection (for tests).
	stdinIsTTY *bool
	// stderrIsTTY overrides the stderr TTY detection (for tests).
	stderrIsTTY *bool
}

// newInstallCmd constructs the `install` subcommand wired to real deps.
func newInstallCmd() *cobra.Command {
	return newInstallCmdWithDeps(installDeps{})
}

// newInstallCmdWithDeps constructs the `install` subcommand with injectable
// dependencies (used by tests).
func newInstallCmdWithDeps(deps installDeps) *cobra.Command {
	var (
		flagYes      bool
		flagDir      string
		flagMongo    string
		flagAPIURL   string
		flagAPIToken string
		flagEmail    string
		flagPassword string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the CRENEIN C-Network agent stack",
		Long: "Installs Docker, pulls the CRENEIN C-Network agent stack images, " +
			"writes the compose configuration, and starts all services.\n\n" +
			"Requires root (sudo). Run on Ubuntu or Debian.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			return runInstall(ctx, cmd, deps,
				flagYes, flagDir, flagMongo,
				flagAPIURL, flagAPIToken,
				flagEmail, flagPassword)
		},
	}

	cmd.Flags().BoolVar(&flagYes, "yes", false, "skip interactive prompts and use defaults for all missing values")
	cmd.Flags().StringVar(&flagDir, "dir", "", "installation directory (default: current working directory, env: CRENEIN_INSTALL_DIR)")
	cmd.Flags().StringVar(&flagMongo, "mongo", "", "MongoDB major version: auto|7|4 (default: auto, env: CRENEIN_MONGO_MAJOR)")
	cmd.Flags().StringVar(&flagAPIURL, "api-url", "", "C-Network API URL (default: http://localhost:8000, env: CRENEIN_API_URL)")
	cmd.Flags().StringVar(&flagAPIToken, "api-token", "", "C-Network API token (env: CRENEIN_API_TOKEN)")
	cmd.Flags().StringVar(&flagEmail, "admin-email", "", "admin account email (default: admin@example.com, env: CRENEIN_ADMIN_EMAIL)")
	cmd.Flags().StringVar(&flagPassword, "admin-password", "", "admin account password (default: admin123, env: CRENEIN_ADMIN_PASSWORD)")

	return cmd
}

// installInputDefs defines the stable order of promptable install inputs.
// The Default field is the fallback used by --yes and TTY prompts (empty input).
var installInputDefs = []InputDef{
	{
		Label:   "Installation directory",
		Flag:    "dir",
		EnvVar:  "CRENEIN_INSTALL_DIR",
		Default: cwdDefault(),
		Secret:  false,
	},
	{
		Label:   "MongoDB version (auto|7|4)",
		Flag:    "mongo",
		EnvVar:  "CRENEIN_MONGO_MAJOR",
		Default: "auto",
		Secret:  false,
	},
	{
		Label:   "API URL",
		Flag:    "api-url",
		EnvVar:  "CRENEIN_API_URL",
		Default: "http://localhost:8000",
		Secret:  false,
	},
	{
		Label:   "API token",
		Flag:    "api-token",
		EnvVar:  "CRENEIN_API_TOKEN",
		Default: "your-api-token-here",
		Secret:  true,
	},
	{
		Label:   "Admin email",
		Flag:    "admin-email",
		EnvVar:  "CRENEIN_ADMIN_EMAIL",
		Default: "admin@example.com",
		Secret:  false,
	},
	{
		Label:   "Admin password",
		Flag:    "admin-password",
		EnvVar:  "CRENEIN_ADMIN_PASSWORD",
		Default: "admin123",
		Secret:  true,
	},
}

// cwdDefault returns the current working directory, or "." on error.
func cwdDefault() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func runInstall(
	ctx context.Context,
	cmd *cobra.Command,
	deps installDeps,
	flagYes bool,
	flagDir, flagMongo, flagAPIURL, flagAPIToken, flagEmail, flagPassword string,
) error {
	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()
	// Wrap stdin in a single shared buffered reader so that ResolveAll and the
	// confirmation prompt read from the same position in the stream.
	stdinR := bufio.NewReader(cmd.InOrStdin())

	// Build flag values slice matching installInputDefs order.
	flagValues := []string{
		flagDir,
		flagMongo,
		flagAPIURL,
		flagAPIToken,
		flagEmail,
		flagPassword,
	}

	// Detect TTY state for the resolver.
	tty := DetectTTY()
	stdinIsTTY := tty.StdinIsTTY
	stderrIsTTY := tty.StderrIsTTY
	if deps.stdinIsTTY != nil {
		stdinIsTTY = *deps.stdinIsTTY
	}
	if deps.stderrIsTTY != nil {
		stderrIsTTY = *deps.stderrIsTTY
	}

	// With --yes: apply flag > env > default for each input, no prompts.
	var resolved []string
	if flagYes {
		resolved = make([]string, len(installInputDefs))
		for i, def := range installInputDefs {
			fv := flagValues[i]
			if fv != "" {
				resolved[i] = fv
				continue
			}
			if def.EnvVar != "" {
				if v := os.Getenv(def.EnvVar); v != "" {
					resolved[i] = v
					continue
				}
			}
			resolved[i] = def.Default
		}
	} else {
		// Interactive or non-interactive without --yes: use ResolveAll.
		// ResolveAll prompts via TTY when available, or returns exit-64 if not.
		rdeps := ResolverDeps{
			Stdin:       stdinR,
			Stderr:      stderr,
			StdinIsTTY:  stdinIsTTY,
			StderrIsTTY: stderrIsTTY,
		}

		var err error
		resolved, err = ResolveAll(flagValues, installInputDefs, rdeps)
		if err != nil {
			return err
		}

		// Show resolved-values summary with secrets masked.
		fmt.Fprintln(stderr, "\nInstallation summary:")
		for i, def := range installInputDefs {
			val := resolved[i]
			if def.Secret {
				val = "****"
			}
			fmt.Fprintf(stderr, "  %-28s %s\n", def.Label+":", val)
		}

		// Final confirmation prompt.
		fmt.Fprint(stderr, "\nProceed? [y/N] ")
		answer, _ := stdinR.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			return abortedError()
		}
	}

	// Unpack resolved values.
	installDir := resolved[0]
	mongoVal := strings.TrimSpace(resolved[1])
	apiURL := resolved[2]
	apiToken := resolved[3]
	adminEmail := resolved[4]
	adminPassword := resolved[5]

	// Validate and map --mongo flag.
	var mongoImageOverride string
	fs := dockerx.NewOSFS()

	switch mongoVal {
	case "auto", "":
		// Engine auto-detects via AVX; MongoImageOverride stays "".
		mongoImageOverride = ""
	case "7":
		// Validate AVX: MongoDB ≥5.0 requires it.
		var hasAVX bool
		var avxErr error
		if deps.avxDetect != nil {
			hasAVX, avxErr = deps.avxDetect(ctx, fs)
		} else {
			hasAVX, avxErr = detect.AVX(ctx, fs)
		}
		if avxErr != nil {
			return preflightError(fmt.Errorf("AVX detection failed: %w; use --mongo 4 to force MongoDB 4.4 or --mongo auto", avxErr))
		}
		if !hasAVX {
			return preflightError(fmt.Errorf("MongoDB ≥5.0 requiere AVX; usá --mongo 4"))
		}
		mongoImageOverride = detect.MongoImage(true)
	case "4":
		mongoImageOverride = detect.MongoImage(false)
	default:
		return usageError(fmt.Sprintf("invalid --mongo value %q: must be auto, 7, or 4", mongoVal))
	}

	// Build engine deps (real production deps).
	ttyState := DetectTTY()
	noColorEnv := os.Getenv("NO_COLOR")
	policy := NewDecorPolicy(ttyState, false, globalFlags.noColor, globalFlags.quiet, noColorEnv)
	presenter := NewHumanPresenter(stderr, policy)

	// Detect compose variant for the real CLI client.
	composeInfo, composeErr := detect.Compose(ctx, dockerx.NewOSCommandRunner())
	if composeErr != nil {
		return preflightError(fmt.Errorf("docker compose not found: %w", composeErr))
	}

	engineDeps := engine.Deps{
		Client:   dockerx.NewCLIClient(composeInfo.Variant),
		Runner:   dockerx.NewOSCommandRunner(),
		FS:       fs,
		Prober:   dockerx.NewHTTPProber(newInsecureHTTPClient()),
		Reporter: presenter,
	}

	opts := engine.InstallOptions{
		MongoImageOverride: mongoImageOverride,
		InstallDir:         installDir,
		AdminEmail:         adminEmail,
		AdminPassword:      adminPassword,
		APIURL:             apiURL,
		APIToken:           apiToken,
	}

	result, err := installFn(ctx, engineDeps, opts)
	if err != nil {
		if isPreflightErr(err) {
			WriteError(stderr, "preflight failed: %v\n", err)
			return preflightError(err)
		}
		WriteError(stderr, "install failed: %v\n", err)
		return opFailureError(err)
	}

	// Reinstall / reused components info to stderr.
	if result.ReinstallMode {
		fmt.Fprintf(stderr, "reinstall mode: reusing %s\n", strings.Join(result.ReusedComponents, ", "))
	}

	// Warnings to stderr.
	for _, w := range result.Warnings {
		WriteError(stderr, "warning: %s\n", w)
	}

	// Access summary to stdout.
	fmt.Fprintln(stdout, "\nInstallation complete. Access summary:")
	for _, entry := range result.AccessSummary {
		fmt.Fprintf(stdout, "  %-32s %s\n", entry.Label+":", entry.Value)
	}

	return nil
}

// isPreflightErr reports whether err is a pre-flight check failure, by
// inspecting the cnerr.Error Op (e.g. "engine.install.preflight") rather than
// matching the message text. This keeps the 3-vs-1 exit-code classification
// deterministic and decoupled from human-readable wording.
func isPreflightErr(err error) bool {
	var cnErr *cnerr.Error
	if errors.As(err, &cnErr) {
		return strings.HasSuffix(cnErr.Op, ".preflight")
	}
	return false
}
