// Package httpclient builds an http.Client with an explicit Transport.
// Takes numbers, returns a client — no defaults or config parsing of its
// own; the caller decides the numbers.
package httpclient

import (
	"net/http"
	"time"
)

// New builds a client whose Transport reuses idle connections per host
// (the zero-value Transport's MaxIdleConnsPerHost default of 2 is too low
// for concurrent calls to one host).
func New(timeout time.Duration, maxIdleConnsPerHost int, idleConnTimeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: maxIdleConnsPerHost,
			IdleConnTimeout:     idleConnTimeout,
		},
	}
}
