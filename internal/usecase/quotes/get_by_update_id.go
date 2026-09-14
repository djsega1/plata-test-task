package quotes

import (
	"context"

	"uuid"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// GetByUpdateID parses rawID and looks up the update request, plus the
// quote it resolved to, if any (nil until the request succeeds).
func GetByUpdateID(ctx context.Context, repo Repository, rawID string) (domainquotes.CurrencyRateUpdateRequest, *domainquotes.CurrencyRate, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return domainquotes.CurrencyRateUpdateRequest{}, nil, err
	}
	return repo.GetUpdateByID(ctx, id)
}
