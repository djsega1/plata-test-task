package quotes

import (
	"context"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

type RateProvider interface {
	GetCurrencyRate(ctx context.Context, pair quotes.CurrencyPair) (quotes.CurrencyRate, error)
}
