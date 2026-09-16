package exchangeratedev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/shopspring/decimal"
)

// DefaultBaseURL is the production exchangerate.dev endpoint.
const DefaultBaseURL = "https://api.exchangerate.dev"

// maxResponseBodyBytes bounds how much of a /v1/rate response this adapter
// will read. A legitimate response is a few hundred bytes; this is
// generous headroom, not a tuned limit.
const maxResponseBodyBytes = 1 << 20 // 1 MiB

var _ quotes.RateProvider = (*ExchangerateDevProvider)(nil)

type ExchangerateDevProvider struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	quoteTTL   time.Duration
}

// NewExchangerateDevProvider builds an adapter. httpClient is required —
// wiring (cmd/server) owns building and tuning it, e.g. via pkg/httpclient;
// this package has no opinion on transport settings or their defaults.
// apiKey may be empty. quoteTTL is the StaleAfter window for any quality
// other than "live"/"daily".
func NewExchangerateDevProvider(baseURL, apiKey string, httpClient *http.Client, quoteTTL time.Duration) *ExchangerateDevProvider {
	return &ExchangerateDevProvider{baseURL: baseURL, apiKey: apiKey, httpClient: httpClient, quoteTTL: quoteTTL}
}

func (e *ExchangerateDevProvider) Provider() string { return "exchangerate.dev" }

func (e *ExchangerateDevProvider) Indicative() bool { return true }

// StaleAfter: ~60s for a live quote, next publication (~24h) for a daily fixing, quoteTTL
// for anything mapQuality didn't recognize.
func (e *ExchangerateDevProvider) StaleAfter(quality string, quotedAt time.Time) time.Time {
	switch quality {
	case "live":
		return quotedAt.Add(60 * time.Second)
	case "daily":
		return quotedAt.Add(24 * time.Hour)
	default:
		return quotedAt.Add(e.quoteTTL)
	}
}

// rateResponse holds only the fields this service uses.
type rateResponse struct {
	Result        string      `json:"result"`
	Rate          json.Number `json:"rate"`
	Source        string      `json:"source"`
	DataUpdatedAt time.Time   `json:"data_updated_at"`
	Derived       bool        `json:"derived"`
}

type errorResponse struct {
	Result  string `json:"result"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ExchangerateDevProvider) GetCurrencyRate(ctx context.Context, pair domainquotes.CurrencyPair) (quotes.ProviderQuote, error) {
	url := e.baseURL + "/v1/rate/" + pair.Slug("-")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return quotes.ProviderQuote{}, fmt.Errorf("exchangeratedev: build request: %w", err)
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// Caller's own context ended this, not the upstream's fault —
			// pass through unnormalised rather than record a provider failure.
			return quotes.ProviderQuote{}, err
		}
		// Network/DNS/TLS failure: upstream unreachable, not rejecting the
		// request. Classified retryable so Worker.fail can act on it instead
		// of leaving the row in_progress until the reaper reclaims it.
		return quotes.ProviderQuote{}, domainquotes.NewCurrencyRateError(
			domainquotes.ProviderUnavailableError, "request failed: "+err.Error(), true, 0, "",
		)
	}
	defer resp.Body.Close()

	// Bounds a misbehaving or compromised upstream that streams an
	// unbounded body: the client-side timeout limits how long that can run,
	// not how much memory it can consume in the meantime.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return quotes.ProviderQuote{}, domainquotes.NewCurrencyRateError(
			domainquotes.MalformedResponseError, "reading response body: "+err.Error(), false, 0, "",
		)
	}

	if resp.StatusCode != http.StatusOK {
		return quotes.ProviderQuote{}, e.errorFromBody(resp.StatusCode, resp.Header, body)
	}

	var success rateResponse
	if err := json.Unmarshal(body, &success); err != nil {
		return quotes.ProviderQuote{}, domainquotes.NewCurrencyRateError(
			domainquotes.MalformedResponseError, "decoding response: "+err.Error(), false, 0, "",
		)
	}

	if success.Result != "success" {
		// status 200 but not a success body
		return quotes.ProviderQuote{}, e.errorFromBody(resp.StatusCode, resp.Header, body)
	}

	rateValue, err := decimal.NewFromString(success.Rate.String())
	if err != nil {
		return quotes.ProviderQuote{}, domainquotes.NewCurrencyRateError(
			domainquotes.MalformedResponseError, "parsing rate: "+err.Error(), false, 0, "",
		)
	}

	return quotes.ProviderQuote{
		Value:    rateValue,
		Derived:  success.Derived,
		Quality:  mapQuality(success.Source),
		QuotedAt: success.DataUpdatedAt,
	}, nil
}

func (e *ExchangerateDevProvider) errorFromBody(status int, header http.Header, body []byte) error {
	var errResp errorResponse
	if err := json.Unmarshal(body, &errResp); err != nil || errResp.Code == "" {
		return domainquotes.NewCurrencyRateError(
			domainquotes.MalformedResponseError,
			fmt.Sprintf("upstream returned status %d with an unparseable body", status),
			status >= http.StatusInternalServerError,
			0,
			"",
		)
	}

	return classifyError(errResp.Code, errResp.Message, parseRetryAfter(header))
}

func mapQuality(source string) string {
	switch source {
	case "live":
		return "live"
	case "ecb_daily", "fred_daily":
		return "daily"
	default:
		return "unknown"
	}
}

// parseRetryAfter reads Retry-After as seconds or an HTTP date. Returns 0 if missing or
// invalid; 0 does not mean "retry immediately".
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

type errorClass struct {
	code      domainquotes.CurrencyRateErrorCode
	retryable bool
}

// errorClassification maps exchangerate.dev error codes to this service's own error codes
// and a retry decision. Kept local to the adapter, not the domain: a different upstream
// would need its own table with its own codes.
var errorClassification = map[string]errorClass{
	// bad request or bad data: not retryable
	"invalid_pair":      {domainquotes.UnsupportedPairError, false},
	"unsupported_base":  {domainquotes.UnsupportedPairError, false},
	"bad_date":          {domainquotes.InvalidRequestError, false},
	"invalid_amount":    {domainquotes.InvalidRequestError, false},
	"invalid_request":   {domainquotes.InvalidRequestError, false},
	"missing_parameter": {domainquotes.InvalidRequestError, false},
	"no_data_for_date":  {domainquotes.InvalidRequestError, false},

	// data temporarily unavailable: retryable
	"data_unavailable":    {domainquotes.ProviderUnavailableError, true},
	"live_unavailable":    {domainquotes.ProviderUnavailableError, true},
	"source_unavailable":  {domainquotes.ProviderUnavailableError, true},
	"service_unavailable": {domainquotes.ProviderUnavailableError, true},

	// auth and limits
	"auth_unavailable":  {domainquotes.ProviderUnavailableError, true},
	"invalid_api_key":   {domainquotes.AuthError, false},
	"forbidden":         {domainquotes.AuthError, false},
	"rate_limited":      {domainquotes.RateLimitedError, true},
	"ip_rate_limited":   {domainquotes.RateLimitedError, true},
	"quota_exceeded":    {domainquotes.QuotaExceededError, false}, // monthly quota, won't reset soon — not an auth problem
	"quota_unavailable": {domainquotes.ProviderUnavailableError, true},
}

func classifyError(providerCode, message string, retryAfter time.Duration) error {
	class, ok := errorClassification[providerCode]
	if !ok {
		// unknown code: retryable by default, but kept as its own code so it is not
		// confused with a classified ProviderUnavailableError.
		class = errorClass{code: domainquotes.UnclassifiedProviderError, retryable: true}
	}
	return domainquotes.NewCurrencyRateError(class.code, message, class.retryable, retryAfter, providerCode)
}
