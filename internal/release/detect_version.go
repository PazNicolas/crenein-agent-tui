package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// agentHealthResponse is the minimal shape of the /health endpoint.
type agentHealthResponse struct {
	Version string `json:"version"`
}

const (
	agentHealthHTTPS         = "https://localhost:8000/health"
	agentHealthHTTP          = "http://localhost:8000/health"
	agentContainerNameFilter = "agent"
)

// DetectAgentVersion implements Client.DetectAgentVersion.
//
// Strategy:
//  1. GET https://localhost:8000/health (insecure TLS) → read "version"
//  2. Fallback: GET http://localhost:8000/health → read "version"
//  3. Fallback: inspect Docker containers for crenein/c-network-agent-back image
//  4. All fail → "unknown"
func (mc *ManifestClient) DetectAgentVersion(ctx context.Context) string {
	// ── Step 1 & 2: probe /health ──────────────────────────────────────────
	for _, url := range []string{agentHealthHTTPS, agentHealthHTTP} {
		ver, ok := mc.probeHealth(ctx, url)
		if ok {
			return ver
		}
	}

	// ── Step 3: Docker image inspection ────────────────────────────────────
	if mc.Docker != nil {
		if ver := mc.detectFromDocker(ctx); ver != "" {
			return ver
		}
	}

	return unknownVersion
}

// probeHealth sends a GET to url and reads the "version" field.
// Returns (version, true) on success, ("", false) on any failure including
// non-200 status codes and missing/empty version fields.
func (mc *ManifestClient) probeHealth(ctx context.Context, url string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	resp, err := mc.HTTP.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	// 404 means pre-version-aware backend; any non-200 is also a failure.
	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false
	}

	var h agentHealthResponse
	if err := json.Unmarshal(body, &h); err != nil {
		return "", false
	}
	if h.Version == "" {
		return "", false
	}
	return h.Version, true
}

// detectFromDocker inspects running containers for the agent image and extracts
// the semver tag. Returns "" when nothing useful is found.
func (mc *ManifestClient) detectFromDocker(ctx context.Context) string {
	containers, err := mc.Docker.ContainerList(ctx, agentContainerNameFilter)
	if err != nil || len(containers) == 0 {
		return ""
	}

	const imagePrefix = "crenein/c-network-agent-back:"

	for _, c := range containers {
		// Try to inspect the image referenced by each container.
		// ContainerState.ImageID holds the sha256 digest when the tag is gone,
		// but we also try to inspect by the running container's image ref.
		// We use ImageInspect to get RepoTags.
		info, err := mc.Docker.ImageInspect(ctx, c.ImageID)
		if err != nil {
			continue
		}
		for _, tag := range info.RepoTags {
			if strings.HasPrefix(tag, imagePrefix) {
				suffix := strings.TrimPrefix(tag, imagePrefix)
				if suffix == "latest" {
					// Report digest; semantic version is unknown.
					return fmt.Sprintf("unknown (digest %s)", info.ID)
				}
				if validSemver(suffix) {
					return suffix
				}
			}
		}
	}
	return ""
}
