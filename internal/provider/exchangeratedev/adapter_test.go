package exchangeratedev_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/provider/exchangeratedev"
	"github.com/shopspring/decimal"
)

func testPair(t *testing.T, base, quote quotes.CurrencyCode) quotes.CurrencyPair {
	t.Helper()
	pair, err := quotes.NewCurrencyPair(base, quote)
	if err != nil {
		t.Fatalf("NewCurrencyPair(%s, %s): %v", base, quote, err)
	}
	return pair
}

// mustRateError fails the test if err is not a quotes.CurrencyRateError.
func mustRateError(t *testing.T, err error) quotes.CurrencyRateError {
	t.Helper()
	var rateErr quotes.CurrencyRateError
	if !errors.As(err, &rateErr) {
		t.Fatalf("got error of type %T, want quotes.CurrencyRateError: %v", err, err)
	}
	return rateErr
}

func TestGetCurrencyRate_Success(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"result": "success",
			"pair": "eur-usd",
			"base": "EUR",
			"quote": "USD",
			"rate": 1.0824,
			"source": "live",
			"sources": {"USD": "live"},
			"market_session": "open",
			"timestamp": "2026-06-16T14:02:11Z",
			"data_updated_at": "2026-06-16T14:01:41Z",
			"derived": false,
			"derivation_bps_max": null,
			"notice": "Indicative rates, not for settlement."
		}`))
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeEUR, quotes.CodeUSD)

	rate, err := provider.GetCurrencyRate(context.Background(), pair)
	if err != nil {
		t.Fatalf("GetCurrencyRate: unexpected error: %v", err)
	}

	if gotPath != "/v1/rate/EUR-USD" {
		t.Errorf("request path = %q, want %q (dash slug, not slash)", gotPath, "/v1/rate/EUR-USD")
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty for anonymous access", gotAuth)
	}

	if !rate.Value.Equal(decimal.RequireFromString("1.0824")) {
		t.Errorf("Value = %s, want 1.0824", rate.Value)
	}
	if rate.Derived {
		t.Errorf("Derived = true, want false")
	}
	if rate.Quality != "live" {
		t.Errorf("Quality = %q, want %q (mapped from source=live)", rate.Quality, "live")
	}

	wantQuotedAt := time.Date(2026, 6, 16, 14, 1, 41, 0, time.UTC)
	if !rate.QuotedAt.Equal(wantQuotedAt) {
		// must come from data_updated_at, not timestamp
		t.Errorf("QuotedAt = %v, want %v (data_updated_at, not timestamp)", rate.QuotedAt, wantQuotedAt)
	}
}

func TestExchangerateDevProvider_ProviderAndIndicative(t *testing.T) {
	provider := exchangeratedev.NewExchangerateDevProvider(exchangeratedev.DefaultBaseURL, "", &http.Client{}, 5*time.Minute)

	if got := provider.Provider(); got != "exchangerate.dev" {
		t.Errorf("Provider() = %q, want %q", got, "exchangerate.dev")
	}
	if !provider.Indicative() {
		t.Error("Indicative() = false, want true")
	}
}

func TestExchangerateDevProvider_StaleAfter(t *testing.T) {
	provider := exchangeratedev.NewExchangerateDevProvider(exchangeratedev.DefaultBaseURL, "", &http.Client{}, 5*time.Minute)
	quotedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		quality string
		want    time.Time
	}{
		{"live", quotedAt.Add(60 * time.Second)},
		{"daily", quotedAt.Add(24 * time.Hour)},
		{"unknown", quotedAt.Add(5 * time.Minute)},
	}
	for _, tt := range tests {
		if got := provider.StaleAfter(tt.quality, quotedAt); !got.Equal(tt.want) {
			t.Errorf("StaleAfter(%q, ...) = %v, want %v", tt.quality, got, tt.want)
		}
	}
}

func TestGetCurrencyRate_SendsBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"result":"success","rate":1}`))
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "secret-key", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeUSD, quotes.CodeEUR)

	if _, err := provider.GetCurrencyRate(context.Background(), pair); err != nil {
		t.Fatalf("GetCurrencyRate: unexpected error: %v", err)
	}

	if want := "Bearer secret-key"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

func TestGetCurrencyRate_RateLimited(t *testing.T) {
	tests := []struct {
		name          string
		retryAfterHdr func() string
		wantMinRetry  time.Duration
		wantMaxRetry  time.Duration
	}{
		{
			name:          "seconds",
			retryAfterHdr: func() string { return "30" },
			wantMinRetry:  30 * time.Second,
			wantMaxRetry:  30 * time.Second,
		},
		{
			name: "http-date",
			retryAfterHdr: func() string {
				return time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
			},
			wantMinRetry: 90 * time.Second, // tolerance around "2 minutes from now"
			wantMaxRetry: 130 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tt.retryAfterHdr())
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"result":"error","code":"rate_limited","message":"too many requests"}`))
			}))
			defer srv.Close()

			provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
			pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

			_, err := provider.GetCurrencyRate(context.Background(), pair)
			rateErr := mustRateError(t, err)

			if rateErr.Code() != quotes.RateLimitedError {
				t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.RateLimitedError)
			}
			if !rateErr.Retryable() {
				t.Errorf("Retryable() = false, want true")
			}
			if got := rateErr.RetryAfter(); got < tt.wantMinRetry || got > tt.wantMaxRetry {
				t.Errorf("RetryAfter() = %v, want within [%v, %v]", got, tt.wantMinRetry, tt.wantMaxRetry)
			}
		})
	}
}

func TestGetCurrencyRate_ClassifiedProviderErrors(t *testing.T) {
	tests := []struct {
		providerCode  string
		status        int
		wantCode      quotes.CurrencyRateErrorCode
		wantRetryable bool
	}{
		{"invalid_pair", http.StatusUnprocessableEntity, quotes.UnsupportedPairError, false},
		{"unsupported_base", http.StatusUnprocessableEntity, quotes.UnsupportedPairError, false},
		{"missing_parameter", http.StatusBadRequest, quotes.InvalidRequestError, false},
		{"invalid_api_key", http.StatusUnauthorized, quotes.AuthError, false},
		{"service_unavailable", http.StatusServiceUnavailable, quotes.ProviderUnavailableError, true},
		{"auth_unavailable", http.StatusServiceUnavailable, quotes.ProviderUnavailableError, true},
	}

	for _, tt := range tests {
		t.Run(tt.providerCode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"result":"error","code":"` + tt.providerCode + `","message":"upstream said so"}`))
			}))
			defer srv.Close()

			provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
			pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

			_, err := provider.GetCurrencyRate(context.Background(), pair)
			rateErr := mustRateError(t, err)

			if rateErr.Code() != tt.wantCode {
				t.Errorf("Code() = %v, want %v", rateErr.Code(), tt.wantCode)
			}
			if rateErr.Retryable() != tt.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", rateErr.Retryable(), tt.wantRetryable)
			}
			if rateErr.ProviderCode() != tt.providerCode {
				t.Errorf("ProviderCode() = %q, want %q", rateErr.ProviderCode(), tt.providerCode)
			}
		})
	}
}

func TestGetCurrencyRate_UnclassifiedProviderCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"result":"error","code":"some_new_code_from_the_future","message":"?"}`))
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

	_, err := provider.GetCurrencyRate(context.Background(), pair)
	rateErr := mustRateError(t, err)

	// must stay distinct from ProviderUnavailableError
	if rateErr.Code() != quotes.UnclassifiedProviderError {
		t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.UnclassifiedProviderError)
	}
	if !rateErr.Retryable() {
		t.Errorf("Retryable() = false, want true (unknown failure mode, give it a chance)")
	}
}

func TestGetCurrencyRate_MalformedSuccessBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result": "succ`)) // truncated
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

	_, err := provider.GetCurrencyRate(context.Background(), pair)
	rateErr := mustRateError(t, err)

	if rateErr.Code() != quotes.MalformedResponseError {
		t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.MalformedResponseError)
	}
	if rateErr.Retryable() {
		t.Errorf("Retryable() = true, want false")
	}
}

func TestGetCurrencyRate_MalformedErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<html>503 Service Unavailable</html>`)) // not JSON at all
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

	_, err := provider.GetCurrencyRate(context.Background(), pair)
	rateErr := mustRateError(t, err)

	if rateErr.Code() != quotes.MalformedResponseError {
		t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.MalformedResponseError)
	}
	// 5xx with a bad body is still retryable, based on the status code
	if !rateErr.Retryable() {
		t.Errorf("Retryable() = false, want true (status >= 500 fallback)")
	}
}

func TestGetCurrencyRate_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":"success","rate":1}`))
	}))
	defer srv.Close()

	provider := exchangeratedev.NewExchangerateDevProvider(srv.URL, "", srv.Client(), 5*time.Minute)
	pair := testPair(t, quotes.CodeEUR, quotes.CodeMXN)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the call

	_, err := provider.GetCurrencyRate(ctx, pair)
	if err == nil {
		t.Fatal("GetCurrencyRate: got nil error, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}

	if rateErr, ok := errors.AsType[quotes.CurrencyRateError](err); ok {
		t.Errorf("got a CurrencyRateError (%v) for context cancellation - it must pass through unnormalised", rateErr)
	}
}
