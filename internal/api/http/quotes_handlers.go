package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// Wire error codes, returned in errorEnvelope.Error.Code (codeInternalError
// is defined in middleware.go, next to the panic recovery that's its only
// other caller). A message built from the underlying error (an unsupported
// pair, an idempotency conflict) stays inline at the call site — only the
// fixed, repeated ones are worth naming here.
const (
	codeInvalidRequest      = "invalid_request"
	codeUnsupportedPair     = "unsupported_pair"
	codeIdempotencyConflict = "idempotency_conflict"
	codeNotFound            = "not_found"
)

const (
	msgMalformedJSON     = "malformed JSON body"
	msgPairRequired      = "pair is required"
	msgPairQueryRequired = "pair query parameter is required"
	msgNoSuchUpdate      = "no such update"
	msgNoQuoteYet        = "no quote yet for this pair"
	msgInternalError     = "internal server error"
)

// postQuotesUpdatesHandler enqueues a quote refresh (docs/design.md §4).
// nudge may be nil (see nudgeDispatcher) — tests that don't wire a
// dispatcher can pass one.
func postQuotesUpdatesHandler(logger *slog.Logger, repo quotes.Repository, clk quotes.Clock, nudge chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body createUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgMalformedJSON)
			return
		}
		if body.Pair == "" {
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgPairRequired)
			return
		}

		result, err := quotes.RequestUpdate(r.Context(), repo, clk, body.Pair, r.Header.Get("Idempotency-Key"))
		if err != nil {
			switch {
			case errors.Is(err, domainquotes.ErrMalformedPair):
				writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			case errors.Is(err, domainquotes.ErrPairNotAllowed):
				writeError(logger, w, http.StatusUnprocessableEntity, codeUnsupportedPair, err.Error())
			case errors.Is(err, quotes.ErrIdempotencyConflict):
				writeError(logger, w, http.StatusConflict, codeIdempotencyConflict, err.Error())
			default:
				logger.Error("request update", "error", err)
				writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
			}
			return
		}

		status := http.StatusAccepted
		if result.Replayed {
			status = http.StatusOK
			w.Header().Set("Idempotency-Replayed", "true")
		} else {
			nudgeDispatcher(nudge)
		}
		writeJSON(logger, w, status, newCreateUpdateResponse(result.Request))
	}
}

// getQuotesUpdateHandler reports the state of one update request. An
// unparseable id is treated the same as an unknown one (404): both mean
// "this service has no such update", which is all a client-supplied opaque
// id can ever tell it.
func getQuotesUpdateHandler(logger *slog.Logger, repo quotes.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, rate, err := quotes.GetByUpdateID(r.Context(), repo, r.PathValue("id"))
		if err != nil {
			// Both an unknown id (ErrNotFound) and an unparseable one
			// (uuid.Parse's error, which isn't ErrNotFound) land here and
			// get the same 404 — see the doc comment above.
			writeError(logger, w, http.StatusNotFound, codeNotFound, msgNoSuchUpdate)
			return
		}

		if req.Status == domainquotes.StatusPending || req.Status == domainquotes.StatusInProgress {
			w.Header().Set("Retry-After", "1")
		}
		writeJSON(logger, w, http.StatusOK, newUpdateStatusResponse(req, rate))
	}
}

// getQuotesLatestHandler returns the most recent quote for a pair.
func getQuotesLatestHandler(logger *slog.Logger, repo quotes.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawPair := r.URL.Query().Get("pair")
		if rawPair == "" {
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgPairQueryRequired)
			return
		}

		rate, err := quotes.GetLatest(r.Context(), repo, rawPair)
		if err != nil {
			switch {
			case errors.Is(err, domainquotes.ErrMalformedPair):
				writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			case errors.Is(err, domainquotes.ErrPairNotAllowed):
				writeError(logger, w, http.StatusUnprocessableEntity, codeUnsupportedPair, err.Error())
			case errors.Is(err, quotes.ErrNotFound):
				writeError(logger, w, http.StatusNotFound, codeNotFound, msgNoQuoteYet)
			default:
				logger.Error("get latest", "error", err)
				writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
			}
			return
		}
		writeJSON(logger, w, http.StatusOK, newLatestQuoteResponse(rawPair, rate))
	}
}

// nudgeDispatcher wakes Dispatcher.Run (see usecase/quotes/dispatcher.go) if
// it happens to be idle and waiting on nudge right now. The send only
// succeeds in that exact case; the select's default case makes it
// non-blocking otherwise, so this never makes the HTTP response wait:
//   - nudge is nil (no dispatcher wired, e.g. in tests): send blocks
//     forever, so default always wins.
//   - the dispatcher is mid claim-and-dispatch, not yet back at its select:
//     nothing is listening yet, so default wins.
//
// Either way nothing is lost — the periodic tick will pick up the new row
// on its own next pass, just a little later.
func nudgeDispatcher(nudge chan<- struct{}) {
	select {
	case nudge <- struct{}{}:
	default:
	}
}

func writeJSON(logger *slog.Logger, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("write response body", "error", err)
	}
}

func writeError(logger *slog.Logger, w http.ResponseWriter, status int, code, message string) {
	writeJSON(logger, w, status, newErrorEnvelope(code, message))
}
