package quotes

import (
	"context"
	"errors"
	"time"
	"uuid"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrIdempotencyKeyExists: replay (same pair) vs conflict (different
	// pair) is a use-case decision, not the repository's.
	ErrIdempotencyKeyExists = errors.New("idempotency key already exists")
	// ErrIdempotencyConflict: the key was already used for a different pair.
	ErrIdempotencyConflict = errors.New("idempotency key used with a different pair")
)

// Repository is the port over the quotes journal and the quote_updates
// queue.
type Repository interface {
	CreateUpdateRequest(ctx context.Context, req quotes.CurrencyRateUpdateRequest, idempotencyKey string) error
	GetByIdempotencyKey(ctx context.Context, key string) (quotes.CurrencyRateUpdateRequest, error)

	// ClaimBatch moves up to limit rows to in_progress: pending rows due by
	// now, plus in_progress rows locked before visibleSince — the reaper's
	// job, folded into the same claim.
	ClaimBatch(ctx context.Context, limit int, now, visibleSince time.Time) ([]quotes.CurrencyRateUpdateRequest, error)

	// CompleteSuccess closes id by inserting rate as a new quotes row (or
	// deduping onto an existing one with the same provider/pair/quoted_at)
	// and pointing id's quote_id at it. Only for a freshly fetched rate —
	// see CompleteSuccessReuse for the cache-hit path.
	CompleteSuccess(ctx context.Context, id uuid.UUID, pair quotes.CurrencyPair, rate quotes.CurrencyRate, now time.Time) error
	// CompleteSuccessReuse closes id by pointing its quote_id at an
	// already-existing quotes row (quoteID, from GetLatestQuote) without
	// inserting anything — the reuse path for a still-fresh quote.
	CompleteSuccessReuse(ctx context.Context, id uuid.UUID, quoteID int64, now time.Time) error
	CompleteFailure(ctx context.Context, id uuid.UUID, errCode, errMessage string, retryable bool, nextAttemptAt, now time.Time) error
	GetUpdateByID(ctx context.Context, id uuid.UUID) (quotes.CurrencyRateUpdateRequest, *quotes.CurrencyRate, error)

	// GetLatestQuote orders by quoted_at then id, and also returns that
	// row's own id, so a caller that decides to reuse it (still inside its
	// StaleAfter window) can record that reuse via CompleteSuccessReuse
	// without a second lookup.
	GetLatestQuote(ctx context.Context, pair quotes.CurrencyPair) (quotes.CurrencyRate, int64, error)
}
