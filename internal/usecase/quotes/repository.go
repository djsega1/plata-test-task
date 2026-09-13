package quotes

import (
	"context"
	"uuid"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

type Repository interface {
	CreateUpdateRequest(ctx context.Context, req quotes.CurrencyRateUpdateRequest) error
	MarkUpdateSuccess(ctx context.Context, id uuid.UUID, rate quotes.CurrencyRate) error
	MarkUpdateFailed(ctx context.Context, id uuid.UUID, reason string) error
	GetQuoteByUpdateID(ctx context.Context, id uuid.UUID) (*quotes.CurrencyRate, error)
	GetLatestQuote(ctx context.Context, pair quotes.CurrencyPair) (*quotes.CurrencyRate, error)
}
