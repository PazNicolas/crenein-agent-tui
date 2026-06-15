package cmd

import "strings"

// resolveInstallDir searches CWD, /root, and /home/*/ for a docker-compose.yml
// that references the CRENEIN agent image. Returns "" when not found.
//
// If override is non-empty it is returned directly (used by tests and the
// per-command --dir override). This is the single shared implementation used by
// the status, logs, and rollback commands; readFile/readDir are injected so the
// resolution is hermetic in tests.
func resolveInstallDir(
	readFile func(name string) ([]byte, error),
	readDir func(path string) ([]string, error),
	override string,
) string {
	if override != "" {
		return override
	}

	candidates := []string{".", "/root"}
	if entries, err := readDir("/home"); err == nil {
		for _, entry := range entries {
			candidates = append(candidates, "/home/"+entry)
		}
	}

	for _, dir := range candidates {
		data, err := readFile(dir + "/docker-compose.yml")
		if err != nil {
			continue
		}
		content := string(data)
		if strings.Contains(content, "crenein/c-network-agent-back") ||
			strings.Contains(content, "c-network-agent-back") {
			return dir
		}
	}
	return ""
}
