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

// requestIDHeader is trusted from an upstream when present and echoed back
// either way.
const requestIDHeader = "X-Request-Id"

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
		if id == "" {
			id = uuid.NewV7().String()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
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
// Origin is reflected back rather than "*" so it still works if the API
// ever adds cookies/auth. Preflight OPTIONS is answered directly since no
// route registers that method.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
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
