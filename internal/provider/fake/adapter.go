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

	mu        sync.Mutex
	callTimes []time.Time
}

// NewFakeRateProvider builds a fake provider. minDelay == maxDelay == 0 disables the
// simulated delay; rpmQuota <= 0 disables the rate limit.
func NewFakeRateProvider(clock quotes.Clock, minDelay, maxDelay time.Duration, rpmQuota int) *FakeRateProvider {
	return &FakeRateProvider{
		clock:    clock,
		minDelay: minDelay,
		maxDelay: maxDelay,
		rpmQuota: rpmQuota,
	}
}

func (f *FakeRateProvider) GetCurrencyRate(ctx context.Context, pair domainquotes.CurrencyPair) (domainquotes.CurrencyRate, error) {
	if err := f.checkQuota(); err != nil {
		return domainquotes.CurrencyRate{}, err
	}

	if err := f.simulateDelay(ctx); err != nil {
		return domainquotes.CurrencyRate{}, err
	}

	base, ok := baseRates[pair.Slug("-")]
	if !ok {
		return domainquotes.CurrencyRate{}, domainquotes.NewCurrencyRateError(
			domainquotes.UnsupportedPairError,
			fmt.Sprintf("fake provider has no rate for %s", pair),
			false,
			0,
			"unsupported_pair",
		)
	}

	jitter := decimal.NewFromFloat(1 + (rand.Float64()-0.5)*0.01) // +-0.5%

	return domainquotes.CurrencyRate{
		Value:         base.Mul(jitter),
		Derived:       false,
		MarketSession: "open",
		Quality:       "live",
		QuotedAt:      f.clock.Now(),
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
