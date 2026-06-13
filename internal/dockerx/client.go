package dockerx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
)

// CLIClient is the real, CLI-backed implementation of Client. It shells out to
// the docker and docker-compose binaries. The compose variant (v1 vs v2) is
// selected at construction time via the variant parameter; use detect.Compose
// to populate it.
//
// All calls honour the supplied context, so callers can impose deadlines.
type CLIClient struct {
	variant ComposeVariant
}

// NewCLIClient returns a CLIClient that dispatches compose commands using the
// given variant. variant must be ComposeV1 or ComposeV2 (ComposeNone returns
// an error on any compose call).
func NewCLIClient(variant ComposeVariant) *CLIClient {
	return &CLIClient{variant: variant}
}

// Ping runs `docker info` and returns nil when the daemon is reachable.
func (c *CLIClient) Ping(ctx context.Context) error {
	out, err := runCmd(ctx, "docker", "info")
	if err != nil {
		return cnerr.Wrap("dockerx.Ping", fmt.Errorf("%w: %s", err, out),
			"ensure the Docker daemon is running: systemctl start docker")
	}
	return nil
}

// ComposeUp calls compose up with the supplied options.
func (c *CLIClient) ComposeUp(ctx context.Context, composeFile string, opts ComposeUpOptions) error {
	args := c.composeArgs(composeFile, "up")
	if opts.Detach {
		args = append(args, "-d")
	}
	if opts.NoDeps {
		args = append(args, "--no-deps")
	}
	if opts.ForceRecreate {
		args = append(args, "--force-recreate")
	}
	args = append(args, opts.Services...)
	out, err := c.runCompose(ctx, args...)
	if err != nil {
		return cnerr.Wrap("dockerx.ComposeUp", fmt.Errorf("%w: %s", err, out),
			"check docker compose logs for details")
	}
	return nil
}

// ComposePs returns container states for the given compose file.
func (c *CLIClient) ComposePs(ctx context.Context, composeFile string, services []string) ([]ContainerState, error) {
	args := c.composeArgs(composeFile, "ps", "--format", "json")
	args = append(args, services...)
	out, err := c.runCompose(ctx, args...)
	if err != nil {
		return nil, cnerr.Wrap("dockerx.ComposePs", fmt.Errorf("%w: %s", err, out),
			"check that the compose stack is running")
	}
	return parseComposePs(out)
}

// ComposePull pulls images for the named services (or all when services is empty).
func (c *CLIClient) ComposePull(ctx context.Context, composeFile string, services []string) error {
	args := c.composeArgs(composeFile, "pull")
	args = append(args, services...)
	out, err := c.runCompose(ctx, args...)
	if err != nil {
		return cnerr.Wrap("dockerx.ComposePull", fmt.Errorf("%w: %s", err, out),
			"check your network connection to registry-1.docker.io and hub.docker.com")
	}
	return nil
}

// ComposeExec runs a command inside a compose service container.
func (c *CLIClient) ComposeExec(ctx context.Context, composeFile string, opts ExecOptions) ([]byte, error) {
	args := c.composeArgs(composeFile, "exec", "-T", opts.Service)
	args = append(args, opts.Cmd...)
	cmd := c.composeCmd(ctx, args...)
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, cnerr.Wrap("dockerx.ComposeExec",
			fmt.Errorf("%w: %s", err, out),
			fmt.Sprintf("inspect the %q service logs for details", opts.Service))
	}
	return out, nil
}

// ImageInspect returns image metadata for the given reference.
func (c *CLIClient) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	out, err := runCmd(ctx, "docker", "image", "inspect", "--format",
		`{"ID":"{{.Id}}","Size":{{.Size}},"RepoTags":{{json .RepoTags}}}`, ref)
	if err != nil {
		return ImageInfo{}, cnerr.Wrap("dockerx.ImageInspect",
			fmt.Errorf("%w: %s", err, out),
			fmt.Sprintf("ensure image %q exists locally or pull it first", ref))
	}
	var info ImageInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return ImageInfo{}, cnerr.Wrap("dockerx.ImageInspect", err, "unexpected docker inspect output format")
	}
	return info, nil
}

// ImageTag tags source as target.
func (c *CLIClient) ImageTag(ctx context.Context, source, target string) error {
	out, err := runCmd(ctx, "docker", "image", "tag", source, target)
	if err != nil {
		return cnerr.Wrap("dockerx.ImageTag", fmt.Errorf("%w: %s", err, out),
			fmt.Sprintf("ensure image %q exists locally", source))
	}
	return nil
}

// ImagePrune removes dangling images.
func (c *CLIClient) ImagePrune(ctx context.Context) error {
	out, err := runCmd(ctx, "docker", "image", "prune", "-f")
	if err != nil {
		return cnerr.Wrap("dockerx.ImagePrune", fmt.Errorf("%w: %s", err, out),
			"ensure you have sufficient permissions to manage Docker images")
	}
	return nil
}

// ContainerList lists running containers, optionally filtered by name substring.
func (c *CLIClient) ContainerList(ctx context.Context, nameFilter string) ([]ContainerState, error) {
	args := []string{"ps", "--format", `{"Name":"{{.Names}}","Status":"{{.Status}}","Image":"{{.Image}}","ImageID":"{{.ID}}"}`}
	if nameFilter != "" {
		args = append(args, "--filter", "name="+nameFilter)
	}
	out, err := runCmd(ctx, "docker", args...)
	if err != nil {
		return nil, cnerr.Wrap("dockerx.ContainerList", fmt.Errorf("%w: %s", err, out),
			"ensure the Docker daemon is running")
	}
	return parseDockerPs(out)
}

// ComposeLogs returns the last tail log lines for the given compose service.
// It is read-only and MUST NOT alter container state.
func (c *CLIClient) ComposeLogs(ctx context.Context, composeFile, service string, tail int) ([]byte, error) {
	tailStr := fmt.Sprintf("%d", tail)
	args := c.composeArgs(composeFile, "logs", "--no-color", "--tail", tailStr, service)
	out, err := c.runCompose(ctx, args...)
	if err != nil {
		return out, cnerr.Wrap("dockerx.ComposeLogs",
			fmt.Errorf("%w: %s", err, out),
			fmt.Sprintf("inspect the %q service manually: docker compose logs %s", service, service))
	}
	return out, nil
}

// ─── internal helpers ────────────────────────────────────────────────────────

// composeArgs builds the argument list for a compose command, inserting the
// compose file flag when composeFile is non-empty.
func (c *CLIClient) composeArgs(composeFile, subcommand string, extra ...string) []string {
	var args []string
	switch c.variant {
	case ComposeV2:
		args = append(args, "compose")
		if composeFile != "" {
			args = append(args, "-f", composeFile)
		}
	case ComposeV1:
		if composeFile != "" {
			args = append(args, "-f", composeFile)
		}
	}
	args = append(args, subcommand)
	args = append(args, extra...)
	return args
}

// composeCmd builds an exec.Cmd for a compose invocation.
func (c *CLIClient) composeCmd(ctx context.Context, args ...string) *exec.Cmd {
	switch c.variant {
	case ComposeV2:
		return exec.CommandContext(ctx, "docker", args...)
	default: // ComposeV1
		return exec.CommandContext(ctx, "docker-compose", args[1:]...) // strip the leading "compose" placeholder
	}
}

// runCompose executes a compose command and returns combined output.
func (c *CLIClient) runCompose(ctx context.Context, args ...string) ([]byte, error) {
	switch c.variant {
	case ComposeV2:
		return runCmd(ctx, "docker", args...)
	case ComposeV1:
		// For v1, the first token in args is "compose" (from composeArgs),
		// which must be dropped — v1 uses docker-compose as its own binary.
		if len(args) > 0 && args[0] == "compose" {
			args = args[1:]
		}
		return runCmd(ctx, "docker-compose", args...)
	default:
		return nil, cnerr.New("dockerx.runCompose",
			"no compose variant is available; install docker-compose-plugin")
	}
}

// runCmd runs name with args and returns combined stdout+stderr output.
func runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// ─── output parsers ──────────────────────────────────────────────────────────

// composePsRow is the JSON shape emitted by `docker compose ps --format json`.
type composePsRow struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	Status  string `json:"Status"`
	State   string `json:"State"`
	Image   string `json:"Image"`
}

func parseComposePs(data []byte) ([]ContainerState, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	// Compose ps may emit a JSON array or newline-delimited JSON objects.
	var rows []composePsRow
	if data[0] == '[' {
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, cnerr.Wrap("dockerx.parseComposePs", err, "unexpected compose ps output")
		}
	} else {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row composePsRow
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				continue // skip unparsable lines
			}
			rows = append(rows, row)
		}
	}
	states := make([]ContainerState, 0, len(rows))
	for _, r := range rows {
		running := strings.EqualFold(r.State, "running") || strings.HasPrefix(strings.ToLower(r.Status), "up")
		states = append(states, ContainerState{
			Name:    r.Name,
			Service: r.Service,
			Status:  r.Status,
			Running: running,
		})
	}
	return states, nil
}

// dockerPsRow is the JSON shape we request from `docker ps --format`.
type dockerPsRow struct {
	Name    string `json:"Name"`
	Status  string `json:"Status"`
	Image   string `json:"Image"`
	ImageID string `json:"ImageID"`
}

func parseDockerPs(data []byte) ([]ContainerState, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var states []ContainerState
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerPsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		running := strings.HasPrefix(strings.ToLower(row.Status), "up")
		states = append(states, ContainerState{
			Name:    row.Name,
			Status:  row.Status,
			Running: running,
			ImageID: row.ImageID,
		})
	}
	return states, nil
}
