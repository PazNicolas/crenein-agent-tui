## ADDED Requirements

### Requirement: Self-update applies newer releases with verification and atomic replacement
`crenein-agent self-update` SHALL query the GitHub Releases API of `PazNicolas/crenein-agent-tui`, compare the latest release version against the running binary version using semver, and when a newer version exists, download the release asset matching the current OS/arch, verify its SHA256 against the release `checksums.txt`, and replace the current binary atomically by writing a temporary file in the same directory as the target binary and renaming it over the target. The replaced binary MUST have permissions `0755`.

#### Scenario: Newer version is installed
- **GIVEN** the running binary reports version `0.1.0`
- **AND** the latest GitHub Release of `PazNicolas/crenein-agent-tui` is `0.2.0` with an asset for the current OS/arch and a `checksums.txt` asset
- **WHEN** the user runs `crenein-agent self-update --yes`
- **THEN** the CLI MUST download the asset for the current OS/arch into a temporary file located in the same directory as the resolved binary path (e.g. `/usr/local/bin`)
- **AND** the computed SHA256 of the download MUST match the `checksums.txt` entry for that exact asset name before any replacement occurs
- **AND** the binary MUST be replaced via rename of the temporary file over the target path
- **AND** the new binary MUST have permissions `0755`
- **AND** the CLI MUST report `0.1.0 → 0.2.0` and exit with code `0`

#### Scenario: Already up to date
- **GIVEN** the running binary version equals the latest release version
- **WHEN** the user runs `crenein-agent self-update`
- **THEN** the CLI MUST report that it is already up to date, including the current version
- **AND** it MUST NOT download any asset
- **AND** it MUST exit with code `0`

#### Scenario: Interactive confirmation before replacing
- **GIVEN** a newer version exists
- **AND** the `--yes` flag is NOT provided
- **WHEN** the user runs `crenein-agent self-update` in an interactive terminal
- **THEN** the CLI MUST show the `current → target` versions and ask for confirmation before downloading and replacing
- **AND** declining MUST leave the binary untouched and exit with code `0`

### Requirement: Check-only mode reports update availability via exit code
`crenein-agent self-update --check` SHALL report whether an update is available without modifying anything, using distinct exit codes: `0` when up to date, `10` when an update is available, and `1` on any error.

#### Scenario: Check finds an update available
- **GIVEN** the running binary is older than the latest release
- **WHEN** the user runs `crenein-agent self-update --check`
- **THEN** the CLI MUST print the current and latest versions
- **AND** it MUST NOT download, write, or replace any file other than refreshing `~/.crenein/version-cache.json`
- **AND** it MUST exit with code `10`

#### Scenario: Check finds nothing to do
- **GIVEN** the running binary version equals the latest release version
- **WHEN** the user runs `crenein-agent self-update --check`
- **THEN** the CLI MUST exit with code `0`

#### Scenario: Check fails on network or API error
- **GIVEN** the GitHub Releases API is unreachable and no valid cache entry exists
- **WHEN** the user runs `crenein-agent self-update --check`
- **THEN** the CLI MUST report the error
- **AND** it MUST exit with code `1`

### Requirement: Explicit version pin allows targeted installs and downgrades
`crenein-agent self-update --version X.Y.Z` SHALL install exactly version `X.Y.Z` from the corresponding GitHub Release, including when `X.Y.Z` is older than the running version (explicit downgrade). Verification and atomic replacement rules apply identically.

#### Scenario: Pin to an older version
- **GIVEN** the running binary reports version `0.2.0`
- **AND** release `0.1.0` exists with assets and checksums
- **WHEN** the user runs `crenein-agent self-update --version 0.1.0 --yes`
- **THEN** the CLI MUST download, verify, and atomically install the `0.1.0` binary
- **AND** it MUST report `0.2.0 → 0.1.0` and exit with code `0`

#### Scenario: Pinned version does not exist
- **GIVEN** no release tagged `9.9.9` exists
- **WHEN** the user runs `crenein-agent self-update --version 9.9.9`
- **THEN** the CLI MUST report that the version was not found
- **AND** the current binary MUST remain untouched
- **AND** it MUST exit with code `1`

### Requirement: Failures never corrupt or remove the current binary
Self-update SHALL fail safely: a checksum mismatch MUST abort before any replacement, a missing write permission MUST produce an actionable error suggesting `sudo`, and an interrupted download MUST NOT leave a partial binary at the target path or leak temporary files.

#### Scenario: Checksum mismatch aborts
- **GIVEN** the downloaded asset's SHA256 does not match the `checksums.txt` entry (or no entry exists for the asset name)
- **WHEN** self-update reaches the verification step
- **THEN** the CLI MUST abort without renaming anything over the target binary
- **AND** the binary at the target path MUST remain byte-identical to before the run
- **AND** the temporary download file MUST be removed
- **AND** the CLI MUST exit with code `1` reporting the checksum failure

#### Scenario: No write permission on the binary location
- **GIVEN** the resolved binary path is `/usr/local/bin/crenein-agent` and the current user cannot write to it
- **WHEN** the user runs `crenein-agent self-update`
- **THEN** the CLI MUST detect the missing permission before downloading the release asset
- **AND** it MUST print an error that includes the suggestion to re-run with `sudo crenein-agent self-update`
- **AND** it MUST exit with code `1`

#### Scenario: Interrupted download leaves no corrupt state
- **GIVEN** the download is interrupted mid-transfer (connection reset or process kill)
- **WHEN** self-update terminates or its context is cancelled
- **THEN** the target binary MUST remain the previous working version
- **AND** no partially written file MUST remain at the target path
- **AND** a subsequent self-update run MUST clean up any stale temporary file before starting

### Requirement: Release lookups are cached to respect GitHub API rate limits
Self-update and update-availability checks SHALL cache GitHub release lookups in `~/.crenein/version-cache.json` with a 24-hour TTL. The `--force-check` flag SHALL bypass the cache and refresh it from the live API.

#### Scenario: Fresh cache short-circuits the API
- **GIVEN** `~/.crenein/version-cache.json` exists with `fetched_at` less than 24 hours old
- **WHEN** the user runs `crenein-agent self-update --check`
- **THEN** the CLI MUST answer from the cached data
- **AND** it MUST NOT call the GitHub API

#### Scenario: Force-check bypasses the cache
- **GIVEN** a fresh cache exists
- **WHEN** the user runs `crenein-agent self-update --check --force-check`
- **THEN** the CLI MUST query the GitHub Releases API
- **AND** it MUST rewrite `~/.crenein/version-cache.json` with the new result and a new `fetched_at`

#### Scenario: Corrupt cache is not fatal
- **GIVEN** `~/.crenein/version-cache.json` contains invalid JSON
- **WHEN** any command needs release data
- **THEN** the CLI MUST treat the cache as absent and query the API
- **AND** it MUST overwrite the corrupt file with a valid cache on success
