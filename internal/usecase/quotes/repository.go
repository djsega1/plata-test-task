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
)

// Repository is the port over the quotes journal and the quote_updates
// queue (docs/design.md §5).
type Repository interface {
	CreateUpdateRequest(ctx context.Context, req quotes.CurrencyRateUpdateRequest, idempotencyKey string) error
	GetByIdempotencyKey(ctx context.Context, key string) (quotes.CurrencyRateUpdateRequest, error)

	// ClaimBatch moves up to limit rows to in_progress: pending rows due by
	// now, plus in_progress rows locked before visibleSince — the reaper's
	// job, folded into the same claim (docs/design.md §3).
	ClaimBatch(ctx context.Context, limit int, now, visibleSince time.Time) ([]quotes.CurrencyRateUpdateRequest, error)

	CompleteSuccess(ctx context.Context, id uuid.UUID, pair quotes.CurrencyPair, rate quotes.CurrencyRate, now time.Time) error
	CompleteFailure(ctx context.Context, id uuid.UUID, errCode, errMessage string, retryable bool, nextAttemptAt, now time.Time) error
	GetUpdateByID(ctx context.Context, id uuid.UUID) (quotes.CurrencyRateUpdateRequest, *quotes.CurrencyRate, error)

	// GetLatestQuote orders by quoted_at then id (docs/design.md §5).
	GetLatestQuote(ctx context.Context, pair quotes.CurrencyPair) (quotes.CurrencyRate, error)
}
