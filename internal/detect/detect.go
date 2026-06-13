// Package detect provides system-state detection functions used by the install,
// update, and doctor engines. Every function receives its external dependencies
// (FS, CommandRunner, HTTPProber) as parameters so the package is testable
// without root access, a running Docker daemon, or real network calls.
package detect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"syscall"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── AVX detection ───────────────────────────────────────────────────────────

// AVX reports whether any CPU in /proc/cpuinfo lists the "avx" flag.
//
// Returns (true, nil) when AVX is present, (false, nil) when absent, and a
// *cnerr.Error when /proc/cpuinfo is unreadable. Callers MUST NOT silently
// assume AVX support on error.
func AVX(ctx context.Context, fs dockerx.FS) (bool, error) {
	_ = ctx
	data, err := fs.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false, cnerr.Wrap("detect.AVX", err,
			"use an explicit MongoDB image override (--mongo-image) if /proc/cpuinfo is unavailable")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		// Lines like: "flags : fpu vme de pse avx ..."
		if strings.HasPrefix(line, "flags") || strings.HasPrefix(line, "Features") {
			if idx := strings.Index(line, ":"); idx != -1 {
				flags := strings.Fields(line[idx+1:])
				for _, f := range flags {
					if f == "avx" {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

// MongoImage returns the recommended MongoDB image based on AVX support.
func MongoImage(hasAVX bool) string {
	if hasAVX {
		return "mongodb/mongodb-community-server:7.0-ubuntu2204"
	}
	return "mongo:4.4"
}

// ─── Distro detection ────────────────────────────────────────────────────────

// DistroInfo holds the result of Distro detection.
type DistroInfo struct {
	// ID is the lowercase distro identifier from /etc/os-release (e.g. "ubuntu").
	ID string
	// VersionID is the version string (e.g. "22.04").
	VersionID string
	// PrettyName is the human-readable name (e.g. "Ubuntu 22.04.3 LTS").
	PrettyName string
}

// SupportedDistros lists the distro IDs accepted by the install engine.
var SupportedDistros = []string{"ubuntu", "debian"}

// Distro parses /etc/os-release and returns a DistroInfo. Returns a *cnerr.Error
// when the file is missing or when the distro ID is not in SupportedDistros.
func Distro(ctx context.Context, fs dockerx.FS) (DistroInfo, error) {
	_ = ctx
	data, err := fs.ReadFile("/etc/os-release")
	if err != nil {
		return DistroInfo{}, cnerr.Wrap("detect.Distro", err,
			"ensure the host runs Ubuntu or Debian with /etc/os-release present")
	}
	info := parseOSRelease(data)
	for _, supported := range SupportedDistros {
		if info.ID == supported {
			return info, nil
		}
	}
	return DistroInfo{}, &cnerr.Error{
		Op:            "detect.Distro",
		Cause:         fmt.Errorf("unsupported distro %q", info.ID),
		FixSuggestion: "only Ubuntu and Debian are supported; migrate to a supported OS",
	}
}

func parseOSRelease(data []byte) DistroInfo {
	var info DistroInfo
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			info.ID = strings.ToLower(v)
		case "VERSION_ID":
			info.VersionID = v
		case "PRETTY_NAME":
			info.PrettyName = v
		}
	}
	return info
}

// ─── Docker detection ────────────────────────────────────────────────────────

// DockerInfo is the result of Docker detection.
type DockerInfo struct {
	// Installed is true when the docker binary is found in PATH.
	Installed bool
	// DaemonRunning is true when the daemon responded to a ping.
	DaemonRunning bool
	// SocketPermission is true when the socket is accessible to this process.
	SocketPermission bool
	// Version is populated when the daemon is running (e.g. "24.0.7").
	Version string
}

// Docker detects whether Docker is installed, whether the daemon is reachable,
// and whether the current user has socket access.
func Docker(ctx context.Context, runner dockerx.CommandRunner, client dockerx.Client) DockerInfo {
	info := DockerInfo{}

	// Check binary presence.
	_, err := runner.LookPath("docker")
	if err != nil {
		// Not installed — fix: the install engine can install it automatically.
		return info
	}
	info.Installed = true

	// Probe the daemon. We use the client.Ping so the seam is testable.
	if pingErr := client.Ping(ctx); pingErr != nil {
		// Distinguish permission denied vs not running.
		if isPermissionDenied(pingErr) {
			info.SocketPermission = false
			info.DaemonRunning = false
		}
		return info
	}
	info.DaemonRunning = true
	info.SocketPermission = true
	return info
}

// isPermissionDenied heuristically checks whether the error text indicates a
// socket permission problem. Real os.PathError / syscall.Errno handling works
// too, but the fake client returns plain errors, so we string-match as a
// portable fallback.
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "access denied")
}

// ─── Compose detection ───────────────────────────────────────────────────────

// ComposeInfo is the result of Compose detection.
type ComposeInfo struct {
	// Variant is the detected (and preferred) compose variant.
	Variant dockerx.ComposeVariant
	// V2Available is true when `docker compose version` succeeded.
	V2Available bool
	// V1Available is true when `docker-compose --version` succeeded.
	V1Available bool
	// Version is the version string of the selected variant.
	Version string
}

// Compose probes the host for docker compose v2 (`docker compose version`) and
// v1 (`docker-compose --version`), preferring v2 when both are present.
func Compose(ctx context.Context, runner dockerx.CommandRunner) (ComposeInfo, error) {
	info := ComposeInfo{}

	// Probe v2.
	if out, err := runner.Run(ctx, "docker", "compose", "version"); err == nil {
		info.V2Available = true
		info.Version = strings.TrimSpace(string(out))
	}

	// Probe v1.
	if out, err := runner.Run(ctx, "docker-compose", "--version"); err == nil {
		info.V1Available = true
		if !info.V2Available {
			info.Version = strings.TrimSpace(string(out))
		}
	}

	switch {
	case info.V2Available:
		info.Variant = dockerx.ComposeV2
		return info, nil
	case info.V1Available:
		info.Variant = dockerx.ComposeV1
		return info, nil
	default:
		return info, &cnerr.Error{
			Op:            "detect.Compose",
			Cause:         fmt.Errorf("no compose variant found"),
			FixSuggestion: "install docker-compose-plugin: apt-get install docker-compose-plugin",
		}
	}
}

// ─── Connectivity detection ──────────────────────────────────────────────────

// ConnectivityResult holds the per-endpoint reachability result.
type ConnectivityResult struct {
	// URL is the endpoint that was probed.
	URL string
	// Reachable is true when the probe succeeded within the timeout.
	Reachable bool
	// Err holds the error when the probe failed.
	Err error
}

// DefaultConnectivityURLs are the three endpoints the install pre-flight checks.
var DefaultConnectivityURLs = []string{
	"https://registry-1.docker.io/v2/",
	"https://hub.docker.com",
	"https://core.crenein.com",
}

// Connectivity probes each URL using the supplied prober. Each probe is
// independent: a failure on one URL does not stop the others. The prober
// is expected to enforce the per-request timeout itself (e.g. via
// http.Client.Timeout or a context with a deadline set per-request).
//
// Returns the first context error encountered when the parent ctx is cancelled;
// otherwise it always returns nil and per-result errors are embedded in
// ConnectivityResult.Err.
func Connectivity(ctx context.Context, prober dockerx.HTTPProber, urls []string) ([]ConnectivityResult, error) {
	results := make([]ConnectivityResult, 0, len(urls))
	for _, u := range urls {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			results = append(results, ConnectivityResult{
				URL: u,
				Err: cnerr.Wrap("detect.Connectivity", err,
					"check firewall/DNS/outbound HTTPS access"),
			})
			continue
		}
		resp, err := prober.Do(req)
		if err != nil {
			results = append(results, ConnectivityResult{
				URL: u,
				Err: cnerr.Wrap("detect.Connectivity",
					fmt.Errorf("probe %s: %w", u, err),
					"check firewall, DNS, and outbound HTTPS access"),
			})
			continue
		}
		resp.Body.Close()
		results = append(results, ConnectivityResult{URL: u, Reachable: true})
	}
	return results, nil
}

// ─── Disk space detection ────────────────────────────────────────────────────

// MinDiskMB is the required free disk space in megabytes (matches the bash scripts).
const MinDiskMB = 2048

// DiskSpace returns the free disk space in MB on the filesystem that contains
// path. Returns a *cnerr.Error when the free space is below MinDiskMB or when
// the measurement fails.
//
// The fs parameter is accepted for interface consistency and future testability,
// but disk-space measurement requires a real syscall. In tests, the FS seam is
// not used for this function; instead, inject a fake syscall via the optional
// StatvfsFunc field on FakeDiskProber.
func DiskSpace(_ context.Context, path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, cnerr.Wrap("detect.DiskSpace", err,
			fmt.Sprintf("ensure path %q exists and is accessible", path))
	}
	// Bavail is the number of free blocks available to unprivileged users.
	freeMB := (stat.Bavail * uint64(stat.Bsize)) / (1024 * 1024) //nolint:unconvert
	if freeMB < MinDiskMB {
		return freeMB, &cnerr.Error{
			Op:    "detect.DiskSpace",
			Cause: fmt.Errorf("only %d MB free, %d MB required", freeMB, MinDiskMB),
			FixSuggestion: fmt.Sprintf(
				"free at least %d MB of disk space (currently %d MB); run: docker image prune -f",
				MinDiskMB, freeMB),
		}
	}
	return freeMB, nil
}

// DiskSpaceProvider abstracts the syscall.Statfs call so DiskSpace can be
// tested without real filesystem access. Inject via DiskSpaceWithProvider.
type DiskSpaceProvider func(path string) (freeMB uint64, err error)

// DiskSpaceWithProvider is the testable variant of DiskSpace. Use this in unit
// tests by passing a fake provider.
func DiskSpaceWithProvider(_ context.Context, path string, provider DiskSpaceProvider) (uint64, error) {
	freeMB, err := provider(path)
	if err != nil {
		return 0, cnerr.Wrap("detect.DiskSpace", err,
			fmt.Sprintf("ensure path %q exists and is accessible", path))
	}
	if freeMB < MinDiskMB {
		return freeMB, &cnerr.Error{
			Op:    "detect.DiskSpace",
			Cause: fmt.Errorf("only %d MB free, %d MB required", freeMB, MinDiskMB),
			FixSuggestion: fmt.Sprintf(
				"free at least %d MB of disk space (currently %d MB); run: docker image prune -f",
				MinDiskMB, freeMB),
		}
	}
	return freeMB, nil
}

// ─── Permissions detection ───────────────────────────────────────────────────

// PermInfo is the result of Permissions detection.
type PermInfo struct {
	// IsRoot is true when the effective UID is 0.
	IsRoot bool
	// DockerSocketAccessible is true when /var/run/docker.sock is readable.
	DockerSocketAccessible bool
}

// Permissions checks whether the process has root privileges and whether the
// Docker socket is accessible to the current user.
//
// The runner is used to probe the socket (by running `docker info`). In tests,
// inject a FakeCommandRunner that returns canned errors.
func Permissions(ctx context.Context, runner dockerx.CommandRunner) (PermInfo, error) {
	info := PermInfo{}

	// Root check via effective UID. We use unsafe pointer arithmetic to access
	// the real syscall without importing os (which would make faking harder).
	// Actually os.Getuid() is the right call here — it's pure and testable.
	info.IsRoot = effectiveUID() == 0

	// Docker socket check: attempt `docker info`. A "permission denied" error
	// means the socket exists but is not accessible.
	out, err := runner.Run(ctx, "docker", "info")
	if err != nil {
		if isPermissionDenied(err) || isPermissionDenied(fmt.Errorf("%s", out)) {
			info.DockerSocketAccessible = false
		}
		// Any other error (daemon not running, not installed) is fine — we
		// report socket inaccessible but not as a permissions error per se.
		return info, nil
	}
	info.DockerSocketAccessible = true
	return info, nil
}

// effectiveUID returns the effective user ID of the calling process.
// We use a thin wrapper so tests can at least call the function without root.
func effectiveUID() int {
	return syscall.Getuid()
}
