package fake_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/provider/fake"
	"github.com/djsega1/plata-test-task/pkg/clock"
	"github.com/shopspring/decimal"
)

func mustPair(t *testing.T, base, quote quotes.CurrencyCode) quotes.CurrencyPair {
	t.Helper()
	pair, err := quotes.NewCurrencyPair(base, quote)
	if err != nil {
		t.Fatalf("NewCurrencyPair(%s, %s): %v", base, quote, err)
	}
	return pair
}

func TestFakeRateProvider_GetCurrencyRate(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	provider := fake.NewFakeRateProvider(fakeClock, 0, 0, 0, 5*time.Minute)
	pair := mustPair(t, quotes.CodeEUR, quotes.CodeMXN)

	rate, err := provider.GetCurrencyRate(context.Background(), pair)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !rate.QuotedAt.Equal(fakeClock.Now()) {
		t.Errorf("QuotedAt = %v, want %v (from the injected clock)", rate.QuotedAt, fakeClock.Now())
	}
	if rate.Quality != "live" {
		t.Errorf("Quality = %q, want %q", rate.Quality, "live")
	}
	if rate.Derived {
		t.Error("Derived = true, want false")
	}

	// base rate for EUR-MXN, with room for jitter
	base := decimal.RequireFromString("19.00")
	tolerance := base.Mul(decimal.RequireFromString("0.01"))
	if diff := rate.Value.Sub(base).Abs(); diff.GreaterThan(tolerance) {
		t.Errorf("Value = %s, want within %s of base %s", rate.Value, tolerance, base)
	}
}

func TestFakeRateProvider_ProviderAndIndicative(t *testing.T) {
	provider := fake.NewFakeRateProvider(clock.NewFakeClock(time.Now()), 0, 0, 0, 5*time.Minute)

	if got := provider.Provider(); got != "fake" {
		t.Errorf("Provider() = %q, want %q", got, "fake")
	}
	if !provider.Indicative() {
		t.Error("Indicative() = false, want true")
	}
}

func TestFakeRateProvider_StaleAfter(t *testing.T) {
	provider := fake.NewFakeRateProvider(clock.NewFakeClock(time.Now()), 0, 0, 0, 5*time.Minute)
	quotedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if got, want := provider.StaleAfter("live", quotedAt), quotedAt.Add(60*time.Second); !got.Equal(want) {
		t.Errorf(`StaleAfter("live", ...) = %v, want %v`, got, want)
	}
	if got, want := provider.StaleAfter("unknown", quotedAt), quotedAt.Add(5*time.Minute); !got.Equal(want) {
		t.Errorf(`StaleAfter("unknown", ...) = %v, want %v (quoteTTL)`, got, want)
	}
}

func TestFakeRateProvider_DifferentPairsDifferentRates(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Now())
	provider := fake.NewFakeRateProvider(fakeClock, 0, 0, 0, 5*time.Minute)

	eurMxn, err := provider.GetCurrencyRate(context.Background(), mustPair(t, quotes.CodeEUR, quotes.CodeMXN))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	usdEur, err := provider.GetCurrencyRate(context.Background(), mustPair(t, quotes.CodeUSD, quotes.CodeEUR))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if eurMxn.Value.Equal(usdEur.Value) {
		t.Errorf("EUR/MXN and USD/EUR both returned %s — they should come from different base rates", eurMxn.Value)
	}
}

func TestFakeRateProvider_ContextCancelledDuringSimulatedDelay(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Now())
	// short delay; ctx is already cancelled, so this returns fast
	provider := fake.NewFakeRateProvider(fakeClock, 50*time.Millisecond, 50*time.Millisecond, 0, 5*time.Minute)
	pair := mustPair(t, quotes.CodeEUR, quotes.CodeMXN)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := provider.GetCurrencyRate(ctx, pair)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("GetCurrencyRate took %v with an already-cancelled context, want a near-instant return", elapsed)
	}
}

func TestFakeRateProvider_Quota(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	provider := fake.NewFakeRateProvider(fakeClock, 0, 0, 2, 5*time.Minute) // 2 requests/minute
	pair := mustPair(t, quotes.CodeEUR, quotes.CodeMXN)

	for i := range 2 {
		if _, err := provider.GetCurrencyRate(context.Background(), pair); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}

	_, err := provider.GetCurrencyRate(context.Background(), pair)
	var rateErr quotes.CurrencyRateError
	if !errors.As(err, &rateErr) {
		t.Fatalf("call 3: got %T, want quotes.CurrencyRateError: %v", err, err)
	}
	if rateErr.Code() != quotes.RateLimitedError {
		t.Errorf("Code() = %v, want %v", rateErr.Code(), quotes.RateLimitedError)
	}
	if !rateErr.Retryable() {
		t.Error("Retryable() = false, want true")
	}

	// after one minute, the quota resets
	fakeClock.Advance(61 * time.Second)
	if _, err := provider.GetCurrencyRate(context.Background(), pair); err != nil {
		t.Fatalf("after advancing past the window: unexpected error: %v", err)
	}
}

func TestFakeRateProvider_QuotaUnderConcurrency(t *testing.T) {
	fakeClock := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	const quota = 5
	const callers = 20
	provider := fake.NewFakeRateProvider(fakeClock, 0, 0, quota, 5*time.Minute)
	pair := mustPair(t, quotes.CodeEUR, quotes.CodeMXN)

	var wg sync.WaitGroup
	var succeeded, limited int64

	for range callers {
		wg.Go(func() {
			if _, err := provider.GetCurrencyRate(context.Background(), pair); err != nil {
				atomic.AddInt64(&limited, 1)
			} else {
				atomic.AddInt64(&succeeded, 1)
			}
		})
	}
	wg.Wait()

	if succeeded != quota {
		t.Errorf("succeeded = %d, want exactly %d (the quota) — run with -race to check the shared callTimes slice too", succeeded, quota)
	}
	if limited != callers-quota {
		t.Errorf("limited = %d, want %d", limited, callers-quota)
	}
}
