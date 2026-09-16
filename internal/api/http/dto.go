package http

import (
	"time"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// errorEnvelope is the single error shape for every non-2xx response:
// {"error":{"code":"...","message":"..."}}.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newErrorEnvelope(code, message string) errorEnvelope {
	return errorEnvelope{Error: errorBody{Code: code, Message: message}}
}

// createUpdateRequest is POST /quotes/updates' body.
type createUpdateRequest struct {
	Pair string `json:"pair"`
}

// createUpdateResponse is POST /quotes/updates' 202 (or 200 on an
// Idempotency-Key replay) body.
type createUpdateResponse struct {
	UpdateID  string    `json:"update_id"`
	Pair      string    `json:"pair"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func newCreateUpdateResponse(req domainquotes.CurrencyRateUpdateRequest) createUpdateResponse {
	return createUpdateResponse{
		UpdateID:  req.ID.String(),
		Pair:      req.Pair.String(),
		Status:    string(req.Status),
		CreatedAt: req.CreatedAt,
	}
}

// quoteRateDTO is the price/provenance fields shared by GET
// /quotes/updates/{id} (once succeeded) and GET /quotes/latest. Price is a
// fixed 10-decimal string so a JS client can't lose precision as a Number.
type quoteRateDTO struct {
	Price      string    `json:"price"`
	QuotedAt   time.Time `json:"quoted_at"`
	FetchedAt  time.Time `json:"fetched_at"`
	Provider   string    `json:"provider"`
	Quality    string    `json:"quality"`
	Derived    bool      `json:"derived"`
	Indicative bool      `json:"indicative"`
}

func newQuoteRateDTO(rate domainquotes.CurrencyRate) quoteRateDTO {
	return quoteRateDTO{
		Price:      rate.Value.StringFixed(10),
		QuotedAt:   rate.QuotedAt,
		FetchedAt:  rate.FetchedAt,
		Provider:   rate.Provider,
		Quality:    rate.Quality,
		Derived:    rate.Derived,
		Indicative: rate.Indicative,
	}
}

// updateStatusResponse is GET /quotes/updates/{id}'s body. *quoteRateDTO and
// Error are mutually exclusive and nil (so absent from the JSON) unless the
// request succeeded or failed respectively.
type updateStatusResponse struct {
	UpdateID string `json:"update_id"`
	Pair     string `json:"pair"`
	Status   string `json:"status"`
	*quoteRateDTO
	// Cached reports, only once Status is "succeeded", whether this update
	// reused an already-fresh quote instead of calling the provider — a
	// request that asked to refresh isn't otherwise distinguishable from
	// one that just handed back a price from up to stale_after ago.
	Cached   *bool      `json:"cached,omitempty"`
	Attempts int        `json:"attempts,omitempty"`
	Error    *errorBody `json:"error,omitempty"`
}

func newUpdateStatusResponse(req domainquotes.CurrencyRateUpdateRequest, rate *domainquotes.CurrencyRate) updateStatusResponse {
	resp := updateStatusResponse{
		UpdateID: req.ID.String(),
		Pair:     req.Pair.String(),
		Status:   string(req.Status),
	}
	if rate != nil {
		dto := newQuoteRateDTO(*rate)
		resp.quoteRateDTO = &dto
	}
	if req.Status == domainquotes.StatusSucceeded {
		resp.Cached = &req.Reused
	}
	if req.Status == domainquotes.StatusFailed {
		resp.Attempts = req.Attempts
		resp.Error = &errorBody{Code: req.ErrorCode, Message: safeErrorMessage(req.ErrorCode)}
	}
	return resp
}

// safeErrorMessages maps a stored error code to a message safe to hand a
// client. req.ErrorMessage carries raw detail (dial errors, upstream URLs)
// for logs and operators, not for the wire.
var safeErrorMessages = map[string]string{
	string(domainquotes.InvalidRequestError):       "the request was invalid",
	string(domainquotes.UnsupportedPairError):      "no rate available for this pair",
	string(domainquotes.AuthError):                 "the upstream provider rejected the request",
	string(domainquotes.QuotaExceededError):        "the upstream provider's quota is exhausted",
	string(domainquotes.RateLimitedError):          "rate limited; retry later",
	string(domainquotes.ProviderUnavailableError):  "the upstream provider is unavailable",
	string(domainquotes.UnclassifiedProviderError): "the upstream provider returned an error",
	string(domainquotes.MalformedResponseError):    "the upstream provider returned an unexpected response",
	string(domainquotes.InternalError):             "an internal error occurred",
	quotes.AttemptsExhaustedCode:                   "giving up after repeated failures",
}

// safeErrorMessage falls back to a generic message for any unrecognized
// code, rather than ever falling through to the raw stored text.
func safeErrorMessage(code string) string {
	if msg, ok := safeErrorMessages[code]; ok {
		return msg
	}
	return "the update failed"
}

// latestQuoteResponse is GET /quotes/latest's body: same rate fields as a
// succeeded updateStatusResponse, minus update_id and status.
type latestQuoteResponse struct {
	Pair string `json:"pair"`
	quoteRateDTO
}

func newLatestQuoteResponse(pair string, rate domainquotes.CurrencyRate) latestQuoteResponse {
	return latestQuoteResponse{Pair: pair, quoteRateDTO: newQuoteRateDTO(rate)}
}
