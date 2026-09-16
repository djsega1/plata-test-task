// Package httpclient builds an http.Client with an explicit Transport
// instead of the zero-value client's implicit fallback to
// http.DefaultTransport. Deliberately dumb, like pkg/clock: it takes
// numbers and returns a client, owning no defaults or config parsing of
// its own — a caller (cmd/server) decides the numbers.
package httpclient

import (
	"net/http"
	"time"
)

// New builds a client whose Transport reuses idle connections to the same
// host instead of a fresh TCP+TLS handshake per call: the zero-value
// Transport's MaxIdleConnsPerHost defaults to 2, too low for a caller that
// fetches several resources from one host concurrently.
func New(timeout time.Duration, maxIdleConnsPerHost int, idleConnTimeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: maxIdleConnsPerHost,
			IdleConnTimeout:     idleConnTimeout,
		},
	}
}
