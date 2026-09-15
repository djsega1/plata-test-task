package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// apiV1Prefix is docs/design.md §4's base path for the business endpoints;
// /healthz and /readyz below are deliberately outside it.
const apiV1Prefix = "/api/v1"

// NewRouter wires every HTTP route. nudge is passed straight through to the
// POST handler (see postQuotesUpdatesHandler) — a nil channel is fine for
// callers (tests, mainly) that don't run a dispatcher.
func NewRouter(
	logger *slog.Logger, ready func(context.Context) error,
	repo quotes.Repository, clk quotes.Clock, nudge chan<- struct{},
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodGet+" /healthz", healthzHandler(logger))
	mux.HandleFunc(http.MethodGet+" /readyz", readyzHandler(logger, ready))

	mux.HandleFunc(http.MethodPost+" "+apiV1Prefix+"/quotes/updates", postQuotesUpdatesHandler(logger, repo, clk, nudge))
	mux.HandleFunc(http.MethodGet+" "+apiV1Prefix+"/quotes/updates/{id}", getQuotesUpdateHandler(logger, repo))
	mux.HandleFunc(http.MethodGet+" "+apiV1Prefix+"/quotes/latest", getQuotesLatestHandler(logger, repo))

	return recoverMiddleware(logger, loggingMiddleware(logger, mux))
}
