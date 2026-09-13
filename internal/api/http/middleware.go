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

// codeInternalError is the wire code for a recovered panic.
const codeInternalError = "internal_error"

// recoverMiddleware turns a panic into a 500 with the same error envelope as
// business errors, instead of crashing the connection.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", "panic", rec, "method", r.Method, "path", r.URL.Path)
				writeJSON(logger, w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{
						"code":    codeInternalError,
						"message": "internal server error",
					},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
