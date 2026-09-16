package quotes

import (
	"context"

	"uuid"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// GetByUpdateID parses rawID and looks up the update request, plus the
// quote it resolved to, if any (nil until the request succeeds). An
// unparseable rawID reports ErrNotFound rather than the raw uuid.Parse
// error, since malformed and well-formed-but-unknown are indistinguishable
// to the caller.
func GetByUpdateID(ctx context.Context, repo Repository, rawID string) (domainquotes.CurrencyRateUpdateRequest, *domainquotes.CurrencyRate, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return domainquotes.CurrencyRateUpdateRequest{}, nil, ErrNotFound
	}
	return repo.GetUpdateByID(ctx, id)
}
