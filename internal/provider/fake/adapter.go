package fake

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/shopspring/decimal"
)

// baseRates holds a starting rate for each allowed pair, keyed by its dash slug (e.g. "EUR-MXN").
var baseRates = map[string]decimal.Decimal{
	"USD-EUR": decimal.NewFromFloat(0.92),
	"USD-MXN": decimal.NewFromFloat(17.50),
	"EUR-USD": decimal.NewFromFloat(1.09),
	"EUR-MXN": decimal.NewFromFloat(19.00),
	"MXN-USD": decimal.NewFromFloat(0.057),
	"MXN-EUR": decimal.NewFromFloat(0.053),
}

var _ quotes.RateProvider = (*FakeRateProvider)(nil)

// FakeRateProvider is an offline RateProvider: fixed base rates with light jitter, plus
// optional simulated delay and a requests-per-minute limit.
type FakeRateProvider struct {
	clock    quotes.Clock
	minDelay time.Duration
	maxDelay time.Duration
	rpmQuota int // requests per minute; 0 means no limit
	quoteTTL time.Duration

	mu        sync.Mutex
	callTimes []time.Time
}

// NewFakeRateProvider builds a fake provider. minDelay == maxDelay == 0 disables the
// simulated delay; rpmQuota <= 0 disables the rate limit. quoteTTL is the StaleAfter
// window for any quality other than "live".
func NewFakeRateProvider(clock quotes.Clock, minDelay, maxDelay time.Duration, rpmQuota int, quoteTTL time.Duration) *FakeRateProvider {
	return &FakeRateProvider{
		clock:    clock,
		minDelay: minDelay,
		maxDelay: maxDelay,
		rpmQuota: rpmQuota,
		quoteTTL: quoteTTL,
	}
}

func (f *FakeRateProvider) Provider() string { return "fake" }

func (f *FakeRateProvider) Indicative() bool { return true }

// StaleAfter treats "live" the same way exchangeratedev does (~60s); anything else
// falls back to quoteTTL.
func (f *FakeRateProvider) StaleAfter(quality string, quotedAt time.Time) time.Time {
	if quality == "live" {
		return quotedAt.Add(60 * time.Second)
	}
	return quotedAt.Add(f.quoteTTL)
}

func (f *FakeRateProvider) GetCurrencyRate(ctx context.Context, pair domainquotes.CurrencyPair) (quotes.ProviderQuote, error) {
	if err := f.checkQuota(); err != nil {
		return quotes.ProviderQuote{}, err
	}

	if err := f.simulateDelay(ctx); err != nil {
		return quotes.ProviderQuote{}, err
	}

	base, ok := baseRates[pair.Slug("-")]
	if !ok {
		return quotes.ProviderQuote{}, domainquotes.NewCurrencyRateError(
			domainquotes.UnsupportedPairError,
			fmt.Sprintf("fake provider has no rate for %s", pair),
			false,
			0,
			"unsupported_pair",
		)
	}

	jitter := decimal.NewFromFloat(1 + (rand.Float64()-0.5)*0.01) // +-0.5%

	return quotes.ProviderQuote{
		Value:    base.Mul(jitter),
		Derived:  false,
		Quality:  "live",
		QuotedAt: f.clock.Now(),
	}, nil
}

// checkQuota keeps a rolling one-minute window of call times.
func (f *FakeRateProvider) checkQuota() error {
	if f.rpmQuota <= 0 {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	now := f.clock.Now()
	cutoff := now.Add(-time.Minute)

	kept := f.callTimes[:0]
	for _, t := range f.callTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	f.callTimes = kept

	if len(f.callTimes) >= f.rpmQuota {
		return domainquotes.NewCurrencyRateError(
			domainquotes.RateLimitedError,
			fmt.Sprintf("fake provider: exceeded %d requests/minute", f.rpmQuota),
			true,
			time.Minute,
			"rate_limited",
		)
	}

	f.callTimes = append(f.callTimes, now)
	return nil
}

// simulateDelay waits for a random duration in [minDelay, maxDelay), but returns early if
// ctx is done.
func (f *FakeRateProvider) simulateDelay(ctx context.Context) error {
	delay := f.randomDelay()
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (f *FakeRateProvider) randomDelay() time.Duration {
	if f.maxDelay <= f.minDelay {
		return f.minDelay
	}
	return f.minDelay + rand.N(f.maxDelay-f.minDelay)
}
