package quotes

import (
	"context"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// GetLatest parses rawPair and returns the most recent quote for it. The
// parsed pair is returned alongside it so a caller (the HTTP handler) can
// echo the canonical "BASE/QUOTE" form back — rawPair itself may be
// lowercase or loosely spaced, and a client shouldn't see that reflected
// verbatim when the equivalent POST endpoint always normalizes it.
func GetLatest(ctx context.Context, repo Repository, rawPair string) (domainquotes.CurrencyPair, domainquotes.CurrencyRate, error) {
	pair, err := domainquotes.ParseCurrencyPair(rawPair, "/")
	if err != nil {
		return domainquotes.CurrencyPair{}, domainquotes.CurrencyRate{}, err
	}
	rate, _, err := repo.GetLatestQuote(ctx, pair)
	return pair, rate, err
}
