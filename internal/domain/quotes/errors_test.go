package quotes_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// CurrencyRateError must implement error.
var _ error = quotes.CurrencyRateError{}

func TestCurrencyRateErrorCode_Valid(t *testing.T) {
	tests := []struct {
		code quotes.CurrencyRateErrorCode
		want bool
	}{
		{quotes.InvalidRequestError, true},
		{quotes.UnsupportedPairError, true},
		{quotes.AuthError, true},
		{quotes.RateLimitedError, true},
		{quotes.ProviderUnavailableError, true},
		{quotes.UnclassifiedProviderError, true},
		{quotes.MalformedResponseError, true},
		{quotes.InternalError, true},
		{quotes.CurrencyRateErrorCode("bogus"), false},
		{quotes.CurrencyRateErrorCode(""), false},
	}
	for _, tt := range tests {
		if got := tt.code.Valid(); got != tt.want {
			t.Errorf("CurrencyRateErrorCode(%q).Valid() = %v, want %v", tt.code, got, tt.want)
		}
	}
}

// NewCurrencyRateError panics rather than returning an error for an invalid
// code (see its doc comment): code is always one of the constants above,
// supplied by our own code, never parsed from external input.
func TestNewCurrencyRateError_PanicsOnInvalidCode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewCurrencyRateError did not panic on an invalid code")
		}
	}()
	_ = quotes.NewCurrencyRateError(quotes.CurrencyRateErrorCode("bogus"), "message", false, 0, "")
}

func TestCurrencyRateError_Accessors(t *testing.T) {
	err := quotes.NewCurrencyRateError(quotes.RateLimitedError, "too many requests", true, 30*time.Second, "rate_limited")

	if err.Code() != quotes.RateLimitedError {
		t.Errorf("Code() = %v, want %v", err.Code(), quotes.RateLimitedError)
	}
	if !err.Retryable() {
		t.Error("Retryable() = false, want true")
	}
	if err.RetryAfter() != 30*time.Second {
		t.Errorf("RetryAfter() = %v, want 30s", err.RetryAfter())
	}
	if err.ProviderCode() != "rate_limited" {
		t.Errorf("ProviderCode() = %q, want %q", err.ProviderCode(), "rate_limited")
	}
}

func TestCurrencyRateError_ErrorMessage(t *testing.T) {
	t.Run("includes the upstream code when present", func(t *testing.T) {
		err := quotes.NewCurrencyRateError(quotes.ProviderUnavailableError, "upstream down", true, 0, "service_unavailable")
		msg := err.Error()
		if !strings.Contains(msg, "service_unavailable") {
			t.Errorf("Error() = %q, want it to mention the raw upstream code — several upstream codes fold into the same domain code", msg)
		}
	})

	t.Run("omits the upstream-code note when there isn't one", func(t *testing.T) {
		err := quotes.NewCurrencyRateError(quotes.MalformedResponseError, "bad json", false, 0, "")
		msg := err.Error()
		if strings.Contains(msg, "upstream code") {
			t.Errorf("Error() = %q, want no upstream-code mention when providerCode is empty", msg)
		}
	})
}

func TestCurrencyRateError_UnwrapsWithErrorsAs(t *testing.T) {
	wrapped := fmt.Errorf("calling provider: %w", quotes.NewCurrencyRateError(quotes.AuthError, "bad key", false, 0, "invalid_api_key"))

	var rateErr quotes.CurrencyRateError
	if !errors.As(wrapped, &rateErr) {
		t.Fatal("errors.As failed to find a CurrencyRateError in the chain")
	}
	if rateErr.Code() != quotes.AuthError {
		t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.AuthError)
	}
}
