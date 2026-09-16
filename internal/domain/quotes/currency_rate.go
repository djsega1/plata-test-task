package quotes

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// maxQuotedAtSkew bounds how far ahead of fetchedAt a quotedAt may be
// before NewCurrencyRate rejects it — allows for normal clock drift
// between this service and the upstream.
const maxQuotedAtSkew = 5 * time.Minute

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

// NewCurrencyRate validates value > 0 and quotedAt. A zero quotedAt (from
// a failed upstream decode) would make StaleAfter always in the past;
// a quotedAt too far in the future would win "latest" ordering and
// shadow real quotes indefinitely. Both are rejected here instead of
// silently persisted.
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
	if quotedAt.IsZero() {
		return CurrencyRate{}, fmt.Errorf("quoted_at must not be zero")
	}
	if quotedAt.After(fetchedAt.Add(maxQuotedAtSkew)) {
		return CurrencyRate{}, fmt.Errorf("quoted_at %s is more than %s ahead of fetched_at %s", quotedAt, maxQuotedAtSkew, fetchedAt)
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
