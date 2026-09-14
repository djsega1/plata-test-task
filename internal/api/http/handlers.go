package http

import (
	"context"
	"encoding/json"
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

func writeJSON(logger *slog.Logger, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("write response body", "error", err)
	}
}
