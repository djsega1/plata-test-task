package http

import (
	"log/slog"
	"net/http"
)

func NewRouter(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler(logger))

	return recoverMiddleware(logger, loggingMiddleware(logger, mux))
}
