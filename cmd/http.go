package cmd

import (
	"crypto/tls"
	"net/http"
	"time"
)

// newInsecureHTTPClient returns an *http.Client that skips TLS certificate
// verification. This is intentional: the CRENEIN agent stack uses self-signed
// certificates on localhost, so TLS verification would always fail.
//
// Use this client ONLY for local health probing (e.g. https://localhost:8000/health).
// Download/release HTTP calls must use the regular httpClient (no InsecureSkipVerify).
func newInsecureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}
