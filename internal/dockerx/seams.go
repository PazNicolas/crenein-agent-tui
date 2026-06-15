// Package dockerx wraps the Docker and docker-compose CLIs behind narrow
// interfaces so that every caller (detect, engine) can be tested without a
// running Docker daemon, root access, or real network calls.
//
// Seam interfaces defined here are shared by internal/detect and internal/engine.
// The real CLI-backed implementations live in client.go; recording fakes for
// tests live in fake.go.
package dockerx

import (
	"context"
	"io"
	"net/http"
)

// ComposeVariant indicates which compose binary variant is available on the
// host. The detector (detect.Compose) fills this; the Client uses it for
// dispatch.
type ComposeVariant int

const (
	// ComposeNone means no compose binary was found.
	ComposeNone ComposeVariant = iota
	// ComposeV1 means the legacy standalone docker-compose binary is available.
	ComposeV1
	// ComposeV2 means the compose v2 plugin (docker compose) is available.
	ComposeV2
)

// String returns the canonical binary prefix for the variant.
func (v ComposeVariant) String() string {
	switch v {
	case ComposeV1:
		return "docker-compose"
	case ComposeV2:
		return "docker compose"
	default:
		return "none"
	}
}

// ComposeUpOptions carries flags for the compose-up call.
type ComposeUpOptions struct {
	// NoDeps skips recreating dependency services (--no-deps).
	NoDeps bool
	// ForceRecreate forces a container replacement even when nothing changed
	// (--force-recreate).
	ForceRecreate bool
	// Services restricts the up command to specific services; empty means all.
	Services []string
	// Detach runs the stack in the background (-d).
	Detach bool
}

// ContainerState holds the minimal information returned by compose ps / docker
// ps for a single container.
type ContainerState struct {
	// Name is the compose-assigned container name (e.g. "srv-agent-1").
	Name string
	// Service is the compose service name (e.g. "agent").
	Service string
	// Status is the raw status string from docker ps (e.g. "Up 2 minutes").
	Status string
	// Running is true when the container is in the "running" state.
	Running bool
	// ImageID is the sha256 digest of the image in use.
	ImageID string
}

// ImageInfo holds the result of an image inspect / docker images query.
type ImageInfo struct {
	// ID is the full sha256 image ID.
	ID string
	// RepoTags lists all repository:tag pairs for this image.
	RepoTags []string
	// Size is the uncompressed size in bytes.
	Size int64
}

// ExecOptions carries configuration for docker compose exec.
type ExecOptions struct {
	// Service is the compose service to exec into (e.g. "influxdb").
	Service string
	// Cmd is the command and its arguments to run inside the container.
	Cmd []string
	// Stdin provides optional stdin to the exec call.
	Stdin io.Reader
}

// Client is the narrow interface that engine and detect use for all Docker and
// docker-compose operations. Every method takes a context so callers can impose
// deadlines or cancellations.
//
// The real implementation shells out to the Docker CLI. Fakes record every
// call so tests can assert the exact command sequence.
type Client interface {
	// Ping checks whether the Docker daemon is reachable.
	Ping(ctx context.Context) error

	// ComposeUp starts (or restarts) one or more compose services.
	ComposeUp(ctx context.Context, composeFile string, opts ComposeUpOptions) error

	// ComposePs lists containers managed by the given compose file, optionally
	// filtered to the named services.
	ComposePs(ctx context.Context, composeFile string, services []string) ([]ContainerState, error)

	// ComposePull pulls images for one or more compose services.
	ComposePull(ctx context.Context, composeFile string, services []string) error

	// ComposeExec runs a command in a running compose service container.
	ComposeExec(ctx context.Context, composeFile string, opts ExecOptions) ([]byte, error)

	// ImageInspect returns image metadata for the given reference (name:tag or ID).
	ImageInspect(ctx context.Context, ref string) (ImageInfo, error)

	// ImageTag assigns a new tag to an existing image (used for rollback
	// re-tagging).
	ImageTag(ctx context.Context, source, target string) error

	// ImagePrune removes dangling images (equivalent to docker image prune -f).
	ImagePrune(ctx context.Context) error

	// ImagePull pulls a single image by its full reference (e.g. "repo/image:tag").
	// Unlike ComposePull, this accepts an image ref, not a compose service name.
	ImagePull(ctx context.Context, ref string) error

	// ContainerList returns the running containers, optionally filtered by
	// name substring.
	ContainerList(ctx context.Context, nameFilter string) ([]ContainerState, error)

	// ComposeLogs returns the last n log lines for the given service.
	// It is read-only and MUST NOT alter container state.
	ComposeLogs(ctx context.Context, composeFile, service string, tail int) ([]byte, error)

	// ComposeLogsStream streams log lines for the given service to stdout.
	// When follow is true the child process runs until ctx is cancelled; a
	// cancellation is not treated as an error (returns nil). When noColor is
	// true --no-color is passed to compose. tail ≤ 0 disables the --tail flag.
	// It is read-only and MUST NOT alter container state.
	ComposeLogsStream(ctx context.Context, composeFile, service string, tail int, follow, noColor bool, stdout io.Writer) error
}

// CommandRunner runs arbitrary system commands (apt-get, systemctl, openssl,
// useradd, chown, chmod, etc.) and returns combined stdout+stderr output.
//
// The engine MUST NOT call exec.Command directly; all external commands other
// than docker/compose go through this seam. This is the only way to test
// root-only flows on a developer laptop.
type CommandRunner interface {
	// Run executes name with args, waiting for it to complete. Returns the
	// combined output and any error (including non-zero exit codes).
	Run(ctx context.Context, name string, args ...string) ([]byte, error)

	// LookPath reports whether name is available on PATH (mirrors exec.LookPath
	// semantics without executing the binary).
	LookPath(name string) (string, error)
}

// FS is the filesystem seam for reads and writes that the engine and detect
// functions perform on VM paths (/proc/cpuinfo, /etc/os-release, /data, .env,
// etc.). Using this seam instead of os.ReadFile / os.WriteFile lets tests
// supply in-memory filesystems.
type FS interface {
	// ReadFile returns the full contents of the named file.
	ReadFile(name string) ([]byte, error)

	// WriteFile writes data to the named file with the given permission bits,
	// creating it if necessary (equivalent to os.WriteFile).
	WriteFile(name string, data []byte, perm uint32) error

	// MkdirAll creates the directory path along with any necessary parents
	// (equivalent to os.MkdirAll).
	MkdirAll(path string, perm uint32) error

	// Stat returns a minimal file-info value for the named path.
	Stat(name string) (FileInfo, error)

	// Chmod changes the mode of the named file.
	Chmod(name string, perm uint32) error

	// Chown changes the numeric user and group of the named file.
	Chown(name string, uid, gid int) error

	// ReadDir returns the directory entries (names only) for path.
	ReadDir(path string) ([]string, error)

	// RemoveAll removes path and any children it contains (equivalent to
	// os.RemoveAll). Removing a non-existent path is not an error.
	RemoveAll(path string) error

	// AppendFile appends data to the named file, creating it if necessary.
	// Semantics: O_APPEND|O_CREATE|O_WRONLY; perm is used only on creation.
	AppendFile(name string, data []byte, perm uint32) error
}

// FileInfo is a minimal stat result returned by FS.Stat.
type FileInfo struct {
	// Name is the base name of the file.
	Name string
	// Mode is the file mode bits (including type bits).
	Mode uint32
	// Size is the file size in bytes.
	Size int64
	// IsDir is true when the path is a directory.
	IsDir bool
}

// HTTPProber performs HTTP requests. Callers inject a real *http.Client or a
// fake that records calls and returns canned responses.
type HTTPProber interface {
	// Do executes the given HTTP request and returns the response. The caller
	// is responsible for closing the response body.
	Do(req *http.Request) (*http.Response, error)
}

// HTTPProberFunc adapts a plain function to the HTTPProber interface.
type HTTPProberFunc func(req *http.Request) (*http.Response, error)

// Do calls f(req).
func (f HTTPProberFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

// NewHTTPProber wraps a standard http.Client as an HTTPProber.
func NewHTTPProber(c *http.Client) HTTPProber {
	return HTTPProberFunc(c.Do)
}
