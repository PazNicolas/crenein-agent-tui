// Package release provides the version manifest client and version-resolution
// helpers for the crenein-agent CLI. All I/O goes through injected seams
// (dockerx.HTTPProber, dockerx.FS, dockerx.Client) so every function is
// testable without network access, real files, or a running Docker daemon.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
)

// ─── Schema types ─────────────────────────────────────────────────────────────

// AgentRelease holds the metadata for one agent backend release.
type AgentRelease struct {
	// Date is the release date in ISO 8601 format (YYYY-MM-DD).
	Date string `json:"date"`
	// Image is the fully qualified Docker image tag for this release.
	// Consumers MUST use this value and never synthesize tags by concatenation.
	Image string `json:"image"`
	// Mongo maps the major MongoDB family ("7" for AVX hosts, "4" for non-AVX)
	// to the exact image tag the install/update engine should pull.
	Mongo map[string]string `json:"mongo"`
	// Notes is a human-readable summary of this release, shown in update previews.
	Notes string `json:"notes"`
}

// CLIRelease holds the metadata for one CLI release.
type CLIRelease struct {
	// Date is the release date in ISO 8601 format (YYYY-MM-DD).
	Date string `json:"date"`
	// Notes is a human-readable summary of this release.
	Notes string `json:"notes"`
}

// AgentSection is the "agent" top-level section of the manifest.
type AgentSection struct {
	// Latest is the recommended semver version string (must be a key in Releases).
	Latest string `json:"latest"`
	// Releases maps each known semver version to its release metadata.
	Releases map[string]AgentRelease `json:"releases"`
}

// CLISection is the "cli" top-level section of the manifest.
type CLISection struct {
	// Latest is the recommended semver version string (must be a key in Releases).
	Latest string `json:"latest"`
	// Releases maps each known semver version to its release metadata.
	Releases map[string]CLIRelease `json:"releases"`
}

// Manifest is the root structure of the versions.json document published on
// every GitHub Release of PazNicolas/crenein-agent-tui.
type Manifest struct {
	Agent AgentSection `json:"agent"`
	CLI   CLISection   `json:"cli"`
	// FetchedAt is populated by FetchManifest and is never serialized to JSON.
	// It holds the time when the manifest was last fetched or read from cache.
	FetchedAt time.Time `json:"-"`
}

// ─── Validation ───────────────────────────────────────────────────────────────

// Validate checks that m is internally consistent according to the spec:
//   - all release keys must be valid semver (X.Y.Z)
//   - agent releases must have non-empty image
//   - agent releases must have mongo["7"] and mongo["4"]
//   - agent.latest must exist in agent.releases
//   - cli.latest must exist in cli.releases
//
// Returns a *cnerr.Error identifying the first violated field; never returns a
// partially usable manifest.
func (m *Manifest) Validate() *cnerr.Error {
	// Validate agent.releases entries.
	for ver, rel := range m.Agent.Releases {
		if !validSemver(ver) {
			return cnerr.New(
				fmt.Sprintf("release.Manifest.Validate: agent.releases key %q is not valid semver (X.Y.Z)", ver),
				"ensure all agent release keys follow the X.Y.Z format",
			)
		}
		if rel.Image == "" {
			return cnerr.New(
				fmt.Sprintf("release.Manifest.Validate: agent.releases[%q].image is empty", ver),
				"provide a fully qualified Docker image tag for every agent release",
			)
		}
		if rel.Mongo == nil {
			return cnerr.New(
				fmt.Sprintf("release.Manifest.Validate: agent.releases[%q].mongo is nil", ver),
				`provide a mongo map with keys "7" and "4" for every agent release`,
			)
		}
		if _, ok := rel.Mongo["7"]; !ok {
			return cnerr.New(
				fmt.Sprintf(`release.Manifest.Validate: agent.releases[%q].mongo missing key "7"`, ver),
				`add a "7" entry in the mongo map for AVX-capable hosts`,
			)
		}
		if _, ok := rel.Mongo["4"]; !ok {
			return cnerr.New(
				fmt.Sprintf(`release.Manifest.Validate: agent.releases[%q].mongo missing key "4"`, ver),
				`add a "4" entry in the mongo map for non-AVX hosts`,
			)
		}
	}

	// Validate agent.latest exists in agent.releases.
	if _, ok := m.Agent.Releases[m.Agent.Latest]; !ok {
		return cnerr.New(
			fmt.Sprintf("release.Manifest.Validate: agent.latest %q not found in agent.releases", m.Agent.Latest),
			"ensure agent.latest references a version present in agent.releases",
		)
	}

	// Validate cli.releases entries.
	for ver := range m.CLI.Releases {
		if !validSemver(ver) {
			return cnerr.New(
				fmt.Sprintf("release.Manifest.Validate: cli.releases key %q is not valid semver (X.Y.Z)", ver),
				"ensure all CLI release keys follow the X.Y.Z format",
			)
		}
	}

	// Validate cli.latest exists in cli.releases.
	if _, ok := m.CLI.Releases[m.CLI.Latest]; !ok {
		return cnerr.New(
			fmt.Sprintf("release.Manifest.Validate: cli.latest %q not found in cli.releases", m.CLI.Latest),
			"ensure cli.latest references a version present in cli.releases",
		)
	}

	return nil
}

// ParseManifest decodes and validates a versions.json document. Returns a
// *cnerr.Error on any parse or validation failure; never returns a partial result.
func ParseManifest(data []byte) (*Manifest, *cnerr.Error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, cnerr.Wrap("release.ParseManifest", err,
			"ensure versions.json is valid JSON matching the expected schema")
	}
	if verr := m.Validate(); verr != nil {
		return nil, verr
	}
	return &m, nil
}

// ─── Semver helpers ───────────────────────────────────────────────────────────

// validSemver returns true iff s is a non-negative X.Y.Z triplet.
func validSemver(s string) bool {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 {
			return false
		}
		// Leading zeros only allowed for "0" itself.
		if len(p) > 1 && p[0] == '0' {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// semverParts parses "X.Y.Z" into three ints. Returns (0,0,0) on bad input.
func semverParts(s string) (int, int, int) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0
	}
	parse := func(p string) int {
		v := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return -1
			}
			v = v*10 + int(c-'0')
		}
		return v
	}
	ma, mi, pa := parse(parts[0]), parse(parts[1]), parse(parts[2])
	if ma < 0 || mi < 0 || pa < 0 {
		return 0, 0, 0
	}
	return ma, mi, pa
}

// CompareSemver compares two semver strings a and b.
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
// Invalid inputs are treated as 0.0.0.
func CompareSemver(a, b string) int {
	aMaj, aMin, aPat := semverParts(a)
	bMaj, bMin, bPat := semverParts(b)
	switch {
	case aMaj != bMaj:
		if aMaj > bMaj {
			return 1
		}
		return -1
	case aMin != bMin:
		if aMin > bMin {
			return 1
		}
		return -1
	case aPat != bPat:
		if aPat > bPat {
			return 1
		}
		return -1
	default:
		return 0
	}
}

// ─── Resolution helpers ───────────────────────────────────────────────────────

// ResolveAgentVersion returns the agent version to target:
// pinVersion (if non-empty and present in releases) or m.Agent.Latest.
// Returns a *cnerr.Error when pinVersion is specified but absent.
func ResolveAgentVersion(m *Manifest, pinVersion string) (string, *cnerr.Error) {
	if pinVersion == "" {
		return m.Agent.Latest, nil
	}
	if _, ok := m.Agent.Releases[pinVersion]; !ok {
		return "", cnerr.New(
			fmt.Sprintf("release.ResolveAgentVersion: version %q not found in agent.releases", pinVersion),
			"run `crenein-agent self-update --check` to see available versions",
		)
	}
	return pinVersion, nil
}

// ResolveAgentImage returns the fully-qualified Docker image for the given agent version.
// Returns a *cnerr.Error when the version is absent from the manifest.
func ResolveAgentImage(m *Manifest, version string) (string, *cnerr.Error) {
	rel, ok := m.Agent.Releases[version]
	if !ok {
		return "", cnerr.New(
			fmt.Sprintf("release.ResolveAgentImage: version %q not found in agent.releases", version),
			"check the manifest for available agent versions",
		)
	}
	return rel.Image, nil
}

// ResolveMongoImage returns the exact Mongo image for the given agent version
// and AVX capability. hasAVX==true picks key "7"; false picks key "4".
// Returns a *cnerr.Error when the version or mongo key is absent.
func ResolveMongoImage(m *Manifest, version string, hasAVX bool) (string, *cnerr.Error) {
	rel, ok := m.Agent.Releases[version]
	if !ok {
		return "", cnerr.New(
			fmt.Sprintf("release.ResolveMongoImage: version %q not found in agent.releases", version),
			"check the manifest for available agent versions",
		)
	}
	key := "4"
	if hasAVX {
		key = "7"
	}
	img, ok := rel.Mongo[key]
	if !ok {
		return "", cnerr.New(
			fmt.Sprintf(`release.ResolveMongoImage: mongo key %q missing for version %q`, key, version),
			`ensure the manifest's mongo map contains both "7" and "4" entries`,
		)
	}
	return img, nil
}

// ResolveReleaseNotes returns the release notes for the given agent version.
// Returns "" when the version is absent (non-fatal: callers may omit notes).
func ResolveReleaseNotes(m *Manifest, version string) string {
	rel, ok := m.Agent.Releases[version]
	if !ok {
		return ""
	}
	return rel.Notes
}

// ─── Update-available computation ─────────────────────────────────────────────

// UpdateStatus describes whether an update is available for one component.
type UpdateStatus int

const (
	// UpdateUnknown means the local version is "unknown"; cannot determine status.
	UpdateUnknown UpdateStatus = iota
	// UpdateAvailable means the manifest has a newer version.
	UpdateAvailable
	// UpdateUpToDate means the local version is current.
	UpdateUpToDate
)

// String returns a human-readable representation.
func (u UpdateStatus) String() string {
	switch u {
	case UpdateAvailable:
		return "available"
	case UpdateUpToDate:
		return "up to date"
	default:
		return "undetermined"
	}
}

// UpdateInfo is the result of an update-availability check.
type UpdateInfo struct {
	// CLIVersion is the local CLI version being compared.
	CLIVersion string
	// CLILatest is the manifest's cli.latest.
	CLILatest string
	// CLIStatus is the update status for the CLI.
	CLIStatus UpdateStatus

	// AgentVersion is the detected running agent version (may be "unknown").
	AgentVersion string
	// AgentLatest is the manifest's agent.latest.
	AgentLatest string
	// AgentStatus is the update status for the agent.
	AgentStatus UpdateStatus
}

const unknownVersion = "unknown"

// ComputeUpdateInfo compares localCLI and detectedAgent against the manifest
// and returns an UpdateInfo. When a version is "unknown" the corresponding
// status is UpdateUnknown (never UpdateAvailable or UpdateUpToDate).
func ComputeUpdateInfo(m *Manifest, localCLI, detectedAgent string) UpdateInfo {
	info := UpdateInfo{
		CLIVersion:   localCLI,
		CLILatest:    m.CLI.Latest,
		AgentVersion: detectedAgent,
		AgentLatest:  m.Agent.Latest,
	}

	// CLI comparison.
	if localCLI == unknownVersion || localCLI == "" {
		info.CLIStatus = UpdateUnknown
	} else {
		cmp := CompareSemver(localCLI, m.CLI.Latest)
		if cmp < 0 {
			info.CLIStatus = UpdateAvailable
		} else {
			info.CLIStatus = UpdateUpToDate
		}
	}

	// Agent comparison.
	if detectedAgent == unknownVersion || detectedAgent == "" || strings.HasPrefix(detectedAgent, "unknown") {
		info.AgentStatus = UpdateUnknown
	} else {
		cmp := CompareSemver(detectedAgent, m.Agent.Latest)
		if cmp < 0 {
			info.AgentStatus = UpdateAvailable
		} else {
			info.AgentStatus = UpdateUpToDate
		}
	}

	return info
}

// ─── Local version cache ───────────────────────────────────────────────────────

// versionCache is the on-disk structure written to ~/.crenein/version-cache.json.
type versionCache struct {
	FetchedAt    time.Time `json:"fetched_at"`
	ManifestJSON []byte    `json:"manifest_json"`
}

const (
	cacheTTL      = 24 * time.Hour
	cacheFile     = "version-cache.json"
	cacheDir      = ".crenein"
	cacheDirPerm  = 0o700
	cacheFilePerm = 0o600
)

// cacheClient handles reading and writing the local version cache.
// homeDir and now are injected for testability.
type cacheClient struct {
	fs      dockerx.FS
	homeDir string
	now     func() time.Time
}

func (c *cacheClient) cachePath() string {
	return c.homeDir + "/" + cacheDir + "/" + cacheFile
}

// readCacheWithTime reads the cache file and returns (manifestJSON, fetchedAt)
// when fresh. Returns (nil, zero) when absent, corrupt, or expired.
func (c *cacheClient) readCacheWithTime() ([]byte, time.Time) {
	data, err := c.fs.ReadFile(c.cachePath())
	if err != nil {
		return nil, time.Time{} // absent
	}
	var entry versionCache
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, time.Time{} // corrupt → treat as absent
	}
	if c.now().Sub(entry.FetchedAt) > cacheTTL {
		return nil, time.Time{} // expired
	}
	return entry.ManifestJSON, entry.FetchedAt
}

// writeCache persists manifestJSON to disk (creates dir+file as needed).
func (c *cacheClient) writeCache(manifestJSON []byte) error {
	dir := c.homeDir + "/" + cacheDir
	if err := c.fs.MkdirAll(dir, cacheDirPerm); err != nil {
		return fmt.Errorf("release.writeCache: mkdir %s: %w", dir, err)
	}
	entry := versionCache{
		FetchedAt:    c.now(),
		ManifestJSON: manifestJSON,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("release.writeCache: marshal: %w", err)
	}
	return c.fs.WriteFile(c.cachePath(), data, cacheFilePerm)
}

// ─── GitHub Releases API client ───────────────────────────────────────────────

const (
	githubRepo    = "PazNicolas/crenein-agent-tui"
	githubAPIBase = "https://api.github.com"
	versionsAsset = "versions.json"
)

// githubRelease is the minimal subset of a GitHub Releases API response we need.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Client is the public interface for fetching the version manifest.
// Implemented by ManifestClient; callers can inject a test double.
type Client interface {
	// FetchManifest returns the validated Manifest. When bypassCache is false
	// and the local cache is fresh, it is served from disk. When bypassCache is
	// true or the cache is stale/absent, a live GitHub API call is made.
	FetchManifest(ctx context.Context, bypassCache bool) (*Manifest, *cnerr.Error)

	// DetectAgentVersion detects the running agent version using /health then
	// Docker inspection, returning "unknown" when neither source is available.
	DetectAgentVersion(ctx context.Context) string
}

// ManifestClient implements Client with injectable seams.
type ManifestClient struct {
	HTTP   dockerx.HTTPProber
	Docker dockerx.Client
	cache  *cacheClient
}

// NewManifestClient constructs a ManifestClient. homeDir and now are injected
// to make cache path resolution and TTL checks testable.
func NewManifestClient(
	http dockerx.HTTPProber,
	docker dockerx.Client,
	fs dockerx.FS,
	homeDir string,
	now func() time.Time,
) *ManifestClient {
	return &ManifestClient{
		HTTP:   http,
		Docker: docker,
		cache: &cacheClient{
			fs:      fs,
			homeDir: homeDir,
			now:     now,
		},
	}
}

// FetchManifest implements Client.FetchManifest.
func (mc *ManifestClient) FetchManifest(ctx context.Context, bypassCache bool) (*Manifest, *cnerr.Error) {
	if !bypassCache {
		if cachedRaw, cachedAt := mc.cache.readCacheWithTime(); cachedRaw != nil {
			m, verr := ParseManifest(cachedRaw)
			if verr == nil {
				m.FetchedAt = cachedAt
				return m, nil
			}
			// Cached manifest is invalid — fall through to live fetch.
		}
	}

	// Fetch latest release metadata from GitHub API.
	releaseJSON, err := mc.fetchURL(ctx, githubAPIBase+"/repos/"+githubRepo+"/releases/latest")
	if err != nil {
		return nil, cnerr.Wrap("release.FetchManifest: fetch latest release", err,
			"check network connectivity or use --force-check to retry")
	}

	var rel githubRelease
	if jsonErr := json.Unmarshal(releaseJSON, &rel); jsonErr != nil {
		return nil, cnerr.Wrap("release.FetchManifest: parse release JSON", jsonErr,
			"the GitHub API returned unexpected JSON; try again later")
	}

	// Find the versions.json asset URL.
	assetURL := ""
	for _, a := range rel.Assets {
		if a.Name == versionsAsset {
			assetURL = a.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return nil, cnerr.New(
			"release.FetchManifest: versions.json asset not found in latest release",
			"ensure the release workflow has uploaded the versions.json asset",
		)
	}

	// Download the manifest asset.
	manifestJSON, err := mc.fetchURL(ctx, assetURL)
	if err != nil {
		return nil, cnerr.Wrap("release.FetchManifest: download versions.json", err,
			"check network connectivity or use --force-check to retry")
	}

	// Validate before caching.
	m, verr := ParseManifest(manifestJSON)
	if verr != nil {
		return nil, verr
	}

	now := mc.cache.now()
	// Persist to cache (best-effort; failure does not abort the operation).
	_ = mc.cache.writeCache(manifestJSON)
	m.FetchedAt = now

	return m, nil
}

// fetchURL executes a GET request via the HTTPProber and returns the body bytes.
func (mc *ManifestClient) fetchURL(ctx context.Context, url string) ([]byte, error) {
	req, err := buildRequest(ctx, "GET", url)
	if err != nil {
		return nil, err
	}
	resp, err := mc.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return readAll(resp.Body)
}
