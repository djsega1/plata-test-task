package quotes

import (
	"context"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// GetLatest parses rawPair and returns the most recent quote for it.
func GetLatest(ctx context.Context, repo Repository, rawPair string) (domainquotes.CurrencyRate, error) {
	pair, err := domainquotes.ParseCurrencyPair(rawPair, "/")
	if err != nil {
		return domainquotes.CurrencyRate{}, err
	}
	rate, _, err := repo.GetLatestQuote(ctx, pair)
	return rate, err
}
