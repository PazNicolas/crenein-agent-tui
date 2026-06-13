package release

import (
	"context"
	"io"
	"net/http"
)

// buildRequest constructs an *http.Request. We wrap the stdlib type to keep
// the seam-based approach — callers never call http.NewRequest directly.
func buildRequest(ctx context.Context, method, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, nil)
}

// readAll reads all bytes from r and returns them.
func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
