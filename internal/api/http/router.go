package http

import (
	"context"
	"log/slog"
	"net/http"
)

func NewRouter(logger *slog.Logger, ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler(logger))
	mux.HandleFunc("GET /readyz", readyzHandler(logger, ready))

	return recoverMiddleware(logger, loggingMiddleware(logger, mux))
}
