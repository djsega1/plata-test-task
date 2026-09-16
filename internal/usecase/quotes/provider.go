package quotes

import (
	"context"
	"time"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/shopspring/decimal"
)

// ProviderQuote is what an adapter's fetch returns — not yet a validated
// domain CurrencyRate, since a raw upstream value isn't safe to persist
// yet. Worker builds the real CurrencyRate from this plus
// Provider/Indicative/StaleAfter and its own clock for FetchedAt.
type ProviderQuote struct {
	Value    decimal.Decimal
	Derived  bool
	Quality  string
	QuotedAt time.Time
}

// RateProvider is implemented once per upstream. Freshness cadence and
// naming are upstream-specific, so each adapter owns them alongside the
// fetch itself, rather than the caller guessing from a shared quality
// vocabulary.
type RateProvider interface {
	GetCurrencyRate(ctx context.Context, pair quotes.CurrencyPair) (ProviderQuote, error)

	// Provider names the upstream this adapter talks to, for
	// CurrencyRate.Provider.
	Provider() string

	// Indicative reports whether this upstream's rates are indicative
	// (not for settlement), for CurrencyRate.Indicative.
	Indicative() bool

	// StaleAfter returns when a rate of this quality, quoted at quotedAt,
	// stops being safe to reuse instead of calling GetCurrencyRate again.
	StaleAfter(quality string, quotedAt time.Time) time.Time
}
