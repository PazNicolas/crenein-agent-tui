package styles

import (
	"strings"
	"testing"
)

// TestLogoLines_CompactPureASCII verifies the wordmark is compact (≤ 3 rows) and
// pure ASCII for both profiles, so it fits smaller terminals and renders cleanly
// on dumb / NO_COLOR terminals.
func TestLogoLines_CompactPureASCII(t *testing.T) {
	for _, p := range []Profile{ProfileColor, ProfileMono} {
		lines := p.LogoLines()
		if len(lines) == 0 {
			t.Fatalf("profile %v: LogoLines returned no lines", p)
		}
		if len(lines) > 3 {
			t.Errorf("profile %v: logo should be compact (≤3 rows), got %d", p, len(lines))
		}
		joined := strings.Join(lines, "\n")
		for _, r := range joined {
			if r != '\n' && r > 127 {
				t.Errorf("profile %v: logo must be pure ASCII; found non-ASCII rune %q", p, r)
			}
		}
	}
}

// TestLogoTagline pins the English brand tagline.
func TestLogoTagline(t *testing.T) {
	const want = "your network, your business, our mission"
	if got := ProfileColor.LogoTagline(); got != want {
		t.Errorf("LogoTagline = %q, want %q", got, want)
	}
}
