package quotes_test

import (
	"context"
	"sync"
	"time"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// countingProvider is a hand-written RateProvider test double. It counts
// calls and can optionally block each call on a gate and/or announce each
// call on arrived, for concurrency tests.
type countingProvider struct {
	mu    sync.Mutex
	calls int

	rate quotes.ProviderQuote
	err  error
	// panicPair: GetCurrencyRate panics instead of returning, but only for
	// this pair — lets a test panic on one row while others in the same
	// batch still resolve normally, to prove the panic stays contained.
	panicPair *domainquotes.CurrencyPair

	gate    chan struct{} // nil: don't block
	arrived chan struct{} // nil: don't announce

	// lastCtx is the context the most recent call received — lets a test
	// assert on its cancellation state (e.g. still live after some outer
	// context was cancelled) while a call is parked on gate.
	lastCtx context.Context
}

func (p *countingProvider) GetCurrencyRate(ctx context.Context, pair domainquotes.CurrencyPair) (quotes.ProviderQuote, error) {
	p.mu.Lock()
	p.calls++
	p.lastCtx = ctx
	p.mu.Unlock()

	if p.arrived != nil {
		p.arrived <- struct{}{}
	}
	if p.gate != nil {
		<-p.gate
	}
	if p.panicPair != nil && pair == *p.panicPair {
		panic("simulated panic from GetCurrencyRate: " + pair.String())
	}
	if p.err != nil {
		return quotes.ProviderQuote{}, p.err
	}
	return p.rate, nil
}

func (p *countingProvider) Provider() string { return "test-provider" }
func (p *countingProvider) Indicative() bool { return true }
func (p *countingProvider) StaleAfter(_ string, quotedAt time.Time) time.Time {
	return quotedAt.Add(time.Minute)
}

func (p *countingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingProvider) lastCallCtx() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCtx
}
