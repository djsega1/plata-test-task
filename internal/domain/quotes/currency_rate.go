package quotes

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type CurrencyRate struct {
	Value      decimal.Decimal
	Derived    bool
	Quality    string
	QuotedAt   time.Time
	Provider   string
	Indicative bool
	FetchedAt  time.Time
	StaleAfter time.Time
}

// NewCurrencyRate validates value > 0 before a rate is persisted to the
// quotes journal (see docs/design.md §5).
func NewCurrencyRate(
	value decimal.Decimal,
	derived bool,
	quality, provider string,
	indicative bool,
	quotedAt, fetchedAt, staleAfter time.Time,
) (CurrencyRate, error) {
	if value.Sign() <= 0 {
		return CurrencyRate{}, fmt.Errorf("rate must be positive, got %s", value)
	}
	return CurrencyRate{
		Value:      value,
		Derived:    derived,
		Quality:    quality,
		Provider:   provider,
		Indicative: indicative,
		QuotedAt:   quotedAt,
		FetchedAt:  fetchedAt,
		StaleAfter: staleAfter,
	}, nil
}
