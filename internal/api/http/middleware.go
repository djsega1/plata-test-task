package http

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder captures the status code so loggingMiddleware can log it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
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
		)
	})
}

// corsMiddleware lets a browser call the API from a different origin, e.g.
// a docs UI served on its own port. Origin is reflected back rather than a
// blanket "*": the two behave identically for an API with no cookies or
// auth to leak, but reflecting also works if that ever changes — "*" is
// rejected by browsers alongside credentialed requests. A preflight OPTIONS
// is answered directly, since no route registers that method — without
// this it would fall through to a 404/405 instead of the response the
// browser is asking for.
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
				logger.Error("panic recovered", "panic", rec, "method", r.Method, "path", r.URL.Path)
				writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
