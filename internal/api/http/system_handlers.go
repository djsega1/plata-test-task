package http

import (
	"context"
	"log/slog"
	"net/http"
)

func healthzHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func readyzHandler(logger *slog.Logger, ready func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r.Context()); err != nil {
			logger.Warn("not ready", "error", err)
			writeJSON(logger, w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
