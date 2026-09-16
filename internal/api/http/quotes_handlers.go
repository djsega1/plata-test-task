package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// Wire error codes, returned in errorEnvelope.Error.Code. Messages built
// from the underlying error stay inline at the call site; only the fixed,
// repeated codes are named here.
const (
	codeInvalidRequest      = "invalid_request"
	codeUnsupportedPair     = "unsupported_pair"
	codeIdempotencyConflict = "idempotency_conflict"
	codeNotFound            = "not_found"
	codePayloadTooLarge     = "payload_too_large"
)

// maxCreateUpdateBodyBytes bounds POST /quotes/updates' request body — it's
// one field (a pair string), so this is generous headroom, not a tuned limit.
const maxCreateUpdateBodyBytes = 4 << 10 // 4 KiB

// maxIdempotencyKeyLen bounds the Idempotency-Key header — unlike the body,
// http.MaxBytesReader doesn't cover headers, so an unbounded key would grow
// the quote_updates_idem_idx unique index by however much a client sends.
const maxIdempotencyKeyLen = 255

const (
	msgMalformedJSON         = "malformed JSON body"
	msgPairRequired          = "pair is required"
	msgIdempotencyKeyTooLong = "Idempotency-Key exceeds 255 characters"
	msgPairQueryRequired     = "pair query parameter is required"
	msgNoSuchUpdate          = "no such update"
	msgNoQuoteYet            = "no quote yet for this pair"
	msgInternalError         = "internal server error"
	msgPayloadTooLarge       = "request body exceeds the 4 KiB limit"
)

// postQuotesUpdatesHandler enqueues a quote refresh. nudge may be nil (see
// nudgeDispatcher) — tests that don't wire a dispatcher can pass one.
func postQuotesUpdatesHandler(logger *slog.Logger, repo quotes.Repository, clk quotes.Clock, nudge chan<- struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Caps how much a client can make this handler read.
		r.Body = http.MaxBytesReader(w, r.Body, maxCreateUpdateBodyBytes)

		var body createUpdateRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields() // an unrecognized field is more likely a client bug than forward-compat growth
		if err := dec.Decode(&body); err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				writeError(logger, w, http.StatusRequestEntityTooLarge, codePayloadTooLarge, msgPayloadTooLarge)
				return
			}
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgMalformedJSON)
			return
		}
		if body.Pair == "" {
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgPairRequired)
			return
		}

		idempotencyKey := r.Header.Get("Idempotency-Key")
		if len(idempotencyKey) > maxIdempotencyKeyLen {
			writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, msgIdempotencyKeyTooLong)
			return
		}

		result, err := quotes.RequestUpdate(r.Context(), logger, repo, clk, body.Pair, idempotencyKey)
		if err != nil {
			switch {
			case errors.Is(err, domainquotes.ErrMalformedPair):
				writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			case errors.Is(err, domainquotes.ErrPairNotAllowed):
				writeError(logger, w, http.StatusUnprocessableEntity, codeUnsupportedPair, err.Error())
			case errors.Is(err, quotes.ErrIdempotencyConflict):
				writeError(logger, w, http.StatusConflict, codeIdempotencyConflict, err.Error())
			default:
				logger.Error("request update", "error", err, "request_id", requestIDFromContext(r.Context()))
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

		// The one place request_id and update_id are logged together
		// (see requestIDMiddleware) so the two are traceable to each other.
		logger.Info("update requested",
			"request_id", requestIDFromContext(r.Context()),
			"update_id", result.Request.ID, "pair", body.Pair, "replayed", result.Replayed,
		)
		writeJSON(logger, w, status, newCreateUpdateResponse(result.Request))
	}
}

// getQuotesUpdateHandler reports the state of one update request. An
// unparseable id is treated as an unknown one (404); any other error must
// not be reported as "no such update", since that would wrongly tell a
// polling client to stop for good.
func getQuotesUpdateHandler(logger *slog.Logger, repo quotes.Repository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, rate, err := quotes.GetByUpdateID(r.Context(), repo, r.PathValue("id"))
		if err != nil {
			if errors.Is(err, quotes.ErrNotFound) {
				writeError(logger, w, http.StatusNotFound, codeNotFound, msgNoSuchUpdate)
				return
			}
			logger.Error("get update by id", "error", err, "request_id", requestIDFromContext(r.Context()))
			writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
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

		pair, rate, err := quotes.GetLatest(r.Context(), repo, rawPair)
		if err != nil {
			switch {
			case errors.Is(err, domainquotes.ErrMalformedPair):
				writeError(logger, w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			case errors.Is(err, domainquotes.ErrPairNotAllowed):
				writeError(logger, w, http.StatusUnprocessableEntity, codeUnsupportedPair, err.Error())
			case errors.Is(err, quotes.ErrNotFound):
				writeError(logger, w, http.StatusNotFound, codeNotFound, msgNoQuoteYet)
			default:
				logger.Error("get latest", "error", err, "request_id", requestIDFromContext(r.Context()))
				writeError(logger, w, http.StatusInternalServerError, codeInternalError, msgInternalError)
			}
			return
		}
		writeJSON(logger, w, http.StatusOK, newLatestQuoteResponse(pair.String(), rate))
	}
}

// nudgeDispatcher wakes Dispatcher.Run if it's idle and waiting on nudge;
// the select's default makes it non-blocking otherwise, so the HTTP
// response never waits on it. If the nudge is dropped, the periodic tick
// picks up the new row later.
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
