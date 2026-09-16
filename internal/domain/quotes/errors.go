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
	QuotaExceededError       CurrencyRateErrorCode = "quota_exceeded"       // plan quota spent, not retryable
	RateLimitedError         CurrencyRateErrorCode = "rate_limited"         // too many requests, retryable
	ProviderUnavailableError CurrencyRateErrorCode = "provider_unavailable" // upstream down, retryable

	// code from upstream not in the classification table; kept separate from
	// ProviderUnavailableError so a stale table stays visible.
	UnclassifiedProviderError CurrencyRateErrorCode = "unclassified_provider"

	MalformedResponseError CurrencyRateErrorCode = "malformed_response" // could not parse response, not retryable

	// InternalError marks a failure that isn't the upstream's fault at all —
	// this service's own code produced it (e.g. Repository.CompleteSuccess
	// failing). Worker.fail uses it only once DispatchMaxAttempts is
	// reached on an error it otherwise can't classify, so a bug that keeps
	// throwing before a row ever reaches CompleteFailure still terminates
	// instead of cycling through the reaper forever.
	InternalError CurrencyRateErrorCode = "internal_error"
)

func (c CurrencyRateErrorCode) Valid() bool {
	return slices.Contains([]CurrencyRateErrorCode{
		InvalidRequestError, UnsupportedPairError, AuthError, QuotaExceededError, RateLimitedError,
		ProviderUnavailableError, UnclassifiedProviderError, MalformedResponseError, InternalError,
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

// NewCurrencyRateError panics on an invalid code instead of returning an
// error: code is never derived from external input (the raw upstream
// string goes in providerCode instead), so an invalid code can only be a
// programming mistake.
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
