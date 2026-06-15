package release

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/PazNicolas/crenein-agent-tui/internal/cnerr"
	"github.com/PazNicolas/crenein-agent-tui/internal/dockerx"
	"github.com/PazNicolas/crenein-agent-tui/internal/selfupdate"
)

// GitHubReleaseSource resolves release assets from the GitHub Releases API.
// It implements selfupdate.ReleaseSource.
//
// Asset naming follows the goreleaser convention defined in .goreleaser.yaml:
//
//	crenein-agent_<version>_<os>_<arch>.tar.gz
//
// where <version> is the semver tag without a leading "v"
// (e.g. "crenein-agent_0.2.0_linux_amd64.tar.gz").
type GitHubReleaseSource struct {
	HTTP   dockerx.HTTPProber
	GOOS   string
	GOARCH string
}

// NewGitHubReleaseSource constructs a GitHubReleaseSource wired to the real
// runtime OS/arch and the provided HTTP prober.
func NewGitHubReleaseSource(http dockerx.HTTPProber) *GitHubReleaseSource {
	return &GitHubReleaseSource{
		HTTP:   http,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}
}

// ResolveAsset implements selfupdate.ReleaseSource.
//
// When version is empty it fetches the latest release; otherwise it fetches the
// release tagged "v<version>" (adding a "v" prefix when absent).
func (s *GitHubReleaseSource) ResolveAsset(ctx context.Context, version string) (selfupdate.ReleaseAsset, error) {
	url := s.releaseURL(version)

	body, err := s.get(ctx, url)
	if err != nil {
		return selfupdate.ReleaseAsset{}, cnerr.Wrap(
			"release.GitHubReleaseSource.ResolveAsset: fetch release",
			err,
			"check network connectivity",
		)
	}

	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return selfupdate.ReleaseAsset{}, cnerr.Wrap(
			"release.GitHubReleaseSource.ResolveAsset: parse release JSON",
			err,
			"the GitHub API returned unexpected JSON; try again later",
		)
	}

	// Resolve the version string from the tag (strip leading "v").
	resolvedVersion := strings.TrimPrefix(rel.TagName, "v")

	assetName := s.AssetName(resolvedVersion)
	var downloadURL, checksumsURL string

	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			downloadURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		}
	}

	if downloadURL == "" {
		return selfupdate.ReleaseAsset{}, cnerr.New(
			fmt.Sprintf(
				"release.GitHubReleaseSource.ResolveAsset: no asset %q found in release %s",
				assetName, rel.TagName,
			),
			fmt.Sprintf("ensure the release contains an asset for %s/%s", s.GOOS, s.GOARCH),
		)
	}
	if checksumsURL == "" {
		return selfupdate.ReleaseAsset{}, cnerr.New(
			"release.GitHubReleaseSource.ResolveAsset: checksums.txt not found in release",
			"ensure the release workflow uploads checksums.txt",
		)
	}

	return selfupdate.ReleaseAsset{
		Name:         assetName,
		DownloadURL:  downloadURL,
		ChecksumsURL: checksumsURL,
	}, nil
}

// releaseURL returns the GitHub Releases API URL for the given version.
// Empty version → latest release; non-empty → release by tag (adding "v" prefix
// when absent).
func (s *GitHubReleaseSource) releaseURL(version string) string {
	if version == "" {
		return githubAPIBase + "/repos/" + githubRepo + "/releases/latest"
	}
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return githubAPIBase + "/repos/" + githubRepo + "/releases/tags/" + tag
}

// AssetName builds the expected binary asset filename following the goreleaser
// convention from .goreleaser.yaml:
//
//	crenein-agent_<version>_<os>_<arch>.tar.gz
//
// (project_name=crenein-agent, version without "v" prefix)
func (s *GitHubReleaseSource) AssetName(version string) string {
	return fmt.Sprintf("crenein-agent_%s_%s_%s.tar.gz", version, s.GOOS, s.GOARCH)
}

// get performs a GET request via the HTTPProber and returns the response body.
func (s *GitHubReleaseSource) get(ctx context.Context, url string) ([]byte, error) {
	req, err := buildRequest(ctx, "GET", url)
	if err != nil {
		return nil, err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return readAll(resp.Body)
}
