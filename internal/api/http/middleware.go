package http

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
	"uuid"
)

// statusRecorder captures the status code so loggingMiddleware can log it.
// Flush and Hijack forward to the underlying ResponseWriter's optional
// interfaces, which embedding alone would not promote — without them a
// handler using SSE or a protocol upgrade would fail its type assertion
// silently.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// requestIDContextKey is unexported and of a named type (not string) so a
// key set here can't collide with one some other package might stash in
// the same context under a plain string key.
type requestIDContextKey struct{}

// requestIDHeader is trusted from an upstream when present and well-formed,
// and echoed back either way.
const requestIDHeader = "X-Request-Id"

// maxRequestIDLen bounds an inbound X-Request-Id: it's stashed verbatim
// into every downstream log line for this request, so an unbounded value
// would let a client bloat every one of them.
const maxRequestIDLen = 100

// requestIDMiddleware assigns every request a trace id (from the inbound
// X-Request-Id header, or a fresh uuid.NewV7()), stashed in the context for
// downstream logging.
//
// request_id identifies one HTTP call; update_id identifies a quote_updates
// row that outlives it. postQuotesUpdatesHandler logs both together once,
// which is the pivot point for tracing a request_id into the worker's
// update_id-keyed logs.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewV7().String()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}

// validRequestID accepts an inbound X-Request-Id only if it's short,
// printable ASCII — this is trusted as an already-assigned trace id from an
// upstream proxy, not treated as arbitrary client input, so a client behind
// no such proxy shouldn't be able to plant a control character or an
// oversized value into every log line this request produces.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// requestIDFromContext returns the id requestIDMiddleware stashed in ctx,
// or "" if the middleware isn't wired (e.g. a handler test built without
// going through NewRouter).
func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestIDFromContext(r.Context()),
		)
	})
}

// corsMiddleware lets a browser call the API from a different origin.
// allowed, if non-empty, is the only set of Origins reflected back; empty
// reflects any Origin — safe only as long as this API never sets
// Access-Control-Allow-Credentials. Preflight OPTIONS is answered directly
// since no route registers that method.
func corsMiddleware(allowed []string, next http.Handler) http.Handler {
	allowedSet := make(map[string]bool, len(allowed))
	for _, origin := range allowed {
		allowedSet[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && (len(allowedSet) == 0 || allowedSet[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// codeInternalError is the wire code for a recovered panic.
const codeInternalError = "internal_error"

// recoverMiddleware turns a panic into a 500 with the same error envelope as
// business errors, instead of crashing the connection.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", "panic", rec, "method", r.Method, "path", r.URL.Path,
					"request_id", requestIDFromContext(r.Context()))
				writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
