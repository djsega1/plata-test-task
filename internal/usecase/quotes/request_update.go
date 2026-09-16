package quotes

import (
	"context"
	"errors"
	"log/slog"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// RequestUpdateResult reports whether the request created a new update or
// replayed an existing one via Idempotency-Key.
type RequestUpdateResult struct {
	Request  domainquotes.CurrencyRateUpdateRequest
	Replayed bool
}

// RequestUpdate parses rawPair, creates a pending update request, and
// resolves the Idempotency-Key contract: same key + same pair replays the
// existing request (Replayed = true), same key + a different pair returns
// ErrIdempotencyConflict.
func RequestUpdate(
	ctx context.Context, logger *slog.Logger, repo Repository, clock Clock, rawPair, idempotencyKey string,
) (RequestUpdateResult, error) {
	pair, err := domainquotes.ParseCurrencyPair(rawPair, "/")
	if err != nil {
		return RequestUpdateResult{}, err
	}

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, clock.Now())
	if err := repo.CreateUpdateRequest(ctx, req, idempotencyKey); err != nil {
		if !errors.Is(err, ErrIdempotencyKeyExists) {
			return RequestUpdateResult{}, err
		}

		existing, err := repo.GetByIdempotencyKey(ctx, idempotencyKey)
		if err != nil {
			return RequestUpdateResult{}, err
		}
		if existing.Pair != pair {
			logger.Warn("idempotency conflict", "key", idempotencyKey, "existing_pair", existing.Pair.String(), "requested_pair", pair.String())
			return RequestUpdateResult{}, ErrIdempotencyConflict
		}
		logger.Info("idempotency replay", "id", existing.ID, "pair", pair.String(), "key", idempotencyKey)
		return RequestUpdateResult{Request: existing, Replayed: true}, nil
	}

	logger.Info("update requested", "id", req.ID, "pair", pair.String())
	return RequestUpdateResult{Request: req}, nil
}
