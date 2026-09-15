package quotes

import (
	"fmt"
	"slices"
	"time"
)

type CurrencyRateErrorCode string

const (
	InvalidRequestError      CurrencyRateErrorCode = "invalid_request"      // bad request, not retryable
	UnsupportedPairError     CurrencyRateErrorCode = "unsupported_pair"     // pair not supported, not retryable
	AuthError                CurrencyRateErrorCode = "auth_error"           // bad key or forbidden, not retryable
	RateLimitedError         CurrencyRateErrorCode = "rate_limited"         // too many requests, retryable
	ProviderUnavailableError CurrencyRateErrorCode = "provider_unavailable" // upstream down, retryable

	// code from upstream not in the classification table; kept separate from
	// ProviderUnavailableError so a stale table stays visible.
	UnclassifiedProviderError CurrencyRateErrorCode = "unclassified_provider"

	MalformedResponseError CurrencyRateErrorCode = "malformed_response" // could not parse response, not retryable
)

func (c CurrencyRateErrorCode) Valid() bool {
	return slices.Contains([]CurrencyRateErrorCode{
		InvalidRequestError, UnsupportedPairError, AuthError, RateLimitedError,
		ProviderUnavailableError, UnclassifiedProviderError, MalformedResponseError,
	}, c)
}

type CurrencyRateError struct {
	code         CurrencyRateErrorCode
	message      string
	retryable    bool
	retryAfter   time.Duration // zero if not given
	providerCode string        // raw upstream code, for logs
}

func (c CurrencyRateError) Error() string {
	if c.providerCode != "" {
		return fmt.Sprintf("%s error: %s (upstream code: %s)", c.code, c.message, c.providerCode)
	}
	return fmt.Sprintf("%s error: %s", c.code, c.message)
}

func (c CurrencyRateError) Code() CurrencyRateErrorCode { return c.code }
func (c CurrencyRateError) Retryable() bool             { return c.retryable }
func (c CurrencyRateError) RetryAfter() time.Duration   { return c.retryAfter }
func (c CurrencyRateError) ProviderCode() string        { return c.providerCode }

// NewCurrencyRateError panics on an invalid code rather than returning an
// error: unlike CurrencyCode (parsed from a client-supplied pair string,
// see NewCurrencyCode) or CurrencyPair, code here is never derived from
// external input — every caller passes one of the constants above, and the
// raw upstream string goes in providerCode instead. An invalid code can
// only be a programming mistake, so there's nothing a caller could do with
// a returned error except panic anyway; failing loudly here, at the
// mistake's origin, beats forcing every call site to carry unreachable
// error handling for it.
func NewCurrencyRateError(
	code CurrencyRateErrorCode,
	message string,
	retryable bool,
	retryAfter time.Duration,
	providerCode string,
) CurrencyRateError {
	if !code.Valid() {
		panic(fmt.Sprintf("domainquotes: invalid CurrencyRateErrorCode %q", code))
	}
	return CurrencyRateError{
		code:         code,
		message:      message,
		retryable:    retryable,
		retryAfter:   retryAfter,
		providerCode: providerCode,
	}
}
