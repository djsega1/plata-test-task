package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

var _ quotes.Repository = (*Repository)(nil)

const idempotencyKeyConstraint = "quote_updates_idem_idx"

// Repository implements usecase/quotes.Repository against a pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanUpdateRequest(row scanner) (domainquotes.CurrencyRateUpdateRequest, error) {
	var (
		id                   uuid.UUID
		base, quote, status  string
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&id, &base, &quote, &status, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domainquotes.CurrencyRateUpdateRequest{}, quotes.ErrNotFound
		}
		return domainquotes.CurrencyRateUpdateRequest{}, fmt.Errorf("postgres: scan update request: %w", err)
	}

	pair, err := domainquotes.NewCurrencyPair(domainquotes.CurrencyCode(base), domainquotes.CurrencyCode(quote))
	if err != nil {
		return domainquotes.CurrencyRateUpdateRequest{}, fmt.Errorf("postgres: invalid pair in row: %w", err)
	}
	return domainquotes.CurrencyRateUpdateRequest{
		ID:        id,
		Pair:      pair,
		Status:    domainquotes.CurrencyRateUpdateStatus(status),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func (r *Repository) CreateUpdateRequest(ctx context.Context, req domainquotes.CurrencyRateUpdateRequest, idempotencyKey string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO quote_updates (id, base, quote, status, idempotency_key, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $6, $6)
	`, req.ID, string(req.Pair.Base()), string(req.Pair.Quote()), string(req.Status), idempotencyKey, req.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == idempotencyKeyConstraint {
			return quotes.ErrIdempotencyKeyExists
		}
		return fmt.Errorf("postgres: create update request: %w", err)
	}
	return nil
}

func (r *Repository) GetByIdempotencyKey(ctx context.Context, key string) (domainquotes.CurrencyRateUpdateRequest, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, base, quote, status, created_at, updated_at
		FROM quote_updates
		WHERE idempotency_key = $1
	`, key)
	return scanUpdateRequest(row)
}

// ClaimBatch's WHERE covers both the pending queue and the reaper: pending
// rows due by now, plus in_progress rows locked before visibleSince.
func (r *Repository) ClaimBatch(ctx context.Context, limit int, now, visibleSince time.Time) ([]domainquotes.CurrencyRateUpdateRequest, error) {
	rows, err := r.pool.Query(ctx, `
		WITH claimed AS (
			SELECT id FROM quote_updates
			WHERE (status = 'pending' AND next_attempt_at <= $2)
			   OR (status = 'in_progress' AND locked_at < $3)
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE quote_updates AS qu
		SET status = 'in_progress', locked_at = $2, updated_at = $2, attempts = attempts + 1
		FROM claimed
		WHERE qu.id = claimed.id
		RETURNING qu.id, qu.base, qu.quote, qu.status, qu.created_at, qu.updated_at
	`, limit, now, visibleSince)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim batch: %w", err)
	}
	defer rows.Close()

	var out []domainquotes.CurrencyRateUpdateRequest
	for rows.Next() {
		req, err := scanUpdateRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: claim batch: %w", err)
	}
	return out, nil
}

func (r *Repository) CompleteSuccess(
	ctx context.Context, id uuid.UUID, pair domainquotes.CurrencyPair, rate domainquotes.CurrencyRate, now time.Time,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: complete success: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	var quoteID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO quotes (base, quote, rate, provider, quality, derived, indicative, quoted_at, fetched_at, stale_after)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (provider, base, quote, quoted_at) DO UPDATE SET id = quotes.id
		RETURNING id
	`, string(pair.Base()), string(pair.Quote()), rate.Value.String(), rate.Provider, rate.Quality,
		rate.Derived, rate.Indicative, rate.QuotedAt, rate.FetchedAt, rate.StaleAfter).Scan(&quoteID)
	if err != nil {
		return fmt.Errorf("postgres: complete success: upsert quote: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE quote_updates
		SET status = 'succeeded', quote_id = $2, updated_at = $3
		WHERE id = $1 AND status = 'in_progress'
	`, id, quoteID, now)
	if err != nil {
		return fmt.Errorf("postgres: complete success: update queue row: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: complete success: %s not in_progress", id)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: complete success: commit: %w", err)
	}
	return nil
}

// CompleteFailure folds both outcomes into one statement: retryable moves
// the row back to pending (for a later attempt), anything else moves it to
// its terminal failed state.
func (r *Repository) CompleteFailure(
	ctx context.Context, id uuid.UUID, errCode, errMessage string, retryable bool, nextAttemptAt, now time.Time,
) error {
	nextStatus := "failed"
	if retryable {
		nextStatus = "pending"
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE quote_updates
		SET status = $2,
		    next_attempt_at = CASE WHEN $2 = 'pending' THEN $3 ELSE next_attempt_at END,
		    error_code = $4, error_message = $5, updated_at = $6
		WHERE id = $1 AND status = 'in_progress'
	`, id, nextStatus, nextAttemptAt, errCode, errMessage, now)
	if err != nil {
		return fmt.Errorf("postgres: complete failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: complete failure: %s not in_progress", id)
	}
	return nil
}

func (r *Repository) GetUpdateByID(ctx context.Context, id uuid.UUID) (domainquotes.CurrencyRateUpdateRequest, *domainquotes.CurrencyRate, error) {
	var (
		reqID                           uuid.UUID
		base, quote, status             string
		createdAt, updatedAt            time.Time
		rateStr                         *string
		derived, indicative             *bool
		provider, quality               *string
		quotedAt, fetchedAt, staleAfter *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT qu.id, qu.base, qu.quote, qu.status, qu.created_at, qu.updated_at,
		       q.rate::text, q.derived, q.indicative, q.provider, q.quality, q.quoted_at, q.fetched_at, q.stale_after
		FROM quote_updates qu
		LEFT JOIN quotes q ON q.id = qu.quote_id
		WHERE qu.id = $1
	`, id).Scan(&reqID, &base, &quote, &status, &createdAt, &updatedAt,
		&rateStr, &derived, &indicative, &provider, &quality, &quotedAt, &fetchedAt, &staleAfter)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domainquotes.CurrencyRateUpdateRequest{}, nil, quotes.ErrNotFound
		}
		return domainquotes.CurrencyRateUpdateRequest{}, nil, fmt.Errorf("postgres: get update by id: %w", err)
	}

	pair, err := domainquotes.NewCurrencyPair(domainquotes.CurrencyCode(base), domainquotes.CurrencyCode(quote))
	if err != nil {
		return domainquotes.CurrencyRateUpdateRequest{}, nil, fmt.Errorf("postgres: invalid pair in row: %w", err)
	}
	req := domainquotes.CurrencyRateUpdateRequest{
		ID:        reqID,
		Pair:      pair,
		Status:    domainquotes.CurrencyRateUpdateStatus(status),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}

	if rateStr == nil {
		return req, nil, nil
	}
	value, err := decimal.NewFromString(*rateStr)
	if err != nil {
		return domainquotes.CurrencyRateUpdateRequest{}, nil, fmt.Errorf("postgres: parse rate: %w", err)
	}
	rate := domainquotes.CurrencyRate{
		Value:      value,
		Derived:    *derived,
		Quality:    *quality,
		Provider:   *provider,
		Indicative: *indicative,
		QuotedAt:   *quotedAt,
		FetchedAt:  *fetchedAt,
		StaleAfter: *staleAfter,
	}
	return req, &rate, nil
}

func (r *Repository) GetLatestQuote(ctx context.Context, pair domainquotes.CurrencyPair) (domainquotes.CurrencyRate, error) {
	var (
		rateStr                         string
		derived, indicative             bool
		provider, quality               string
		quotedAt, fetchedAt, staleAfter time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT rate::text, derived, indicative, provider, quality, quoted_at, fetched_at, stale_after
		FROM quotes
		WHERE base = $1 AND quote = $2
		ORDER BY quoted_at DESC, id DESC
		LIMIT 1
	`, string(pair.Base()), string(pair.Quote())).Scan(
		&rateStr, &derived, &indicative, &provider, &quality, &quotedAt, &fetchedAt, &staleAfter)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domainquotes.CurrencyRate{}, quotes.ErrNotFound
		}
		return domainquotes.CurrencyRate{}, fmt.Errorf("postgres: get latest quote: %w", err)
	}

	value, err := decimal.NewFromString(rateStr)
	if err != nil {
		return domainquotes.CurrencyRate{}, fmt.Errorf("postgres: parse rate: %w", err)
	}
	return domainquotes.CurrencyRate{
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
