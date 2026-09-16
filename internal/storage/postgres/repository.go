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
		attempts             int
		errCode, errMessage  *string
		createdAt, updatedAt time.Time
	)
	if err := row.Scan(&id, &base, &quote, &status, &attempts, &errCode, &errMessage, &createdAt, &updatedAt); err != nil {
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
		ID:           id,
		Pair:         pair,
		Status:       domainquotes.CurrencyRateUpdateStatus(status),
		Attempts:     attempts,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		ErrorCode:    derefOrEmpty(errCode),
		ErrorMessage: derefOrEmpty(errMessage),
	}, nil
}

// derefOrEmpty maps a nullable TEXT column to "" (a request that never failed).
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// diagnoseTransitionFailure runs only after a caller's own
// UPDATE ... WHERE status = <expected> affected zero rows, to turn that
// into a specific error instead of a bare "0 rows": id not found, or found
// but not in the status next expects (CanTransitionTo says which). The
// WHERE clause on that UPDATE is what makes the transition itself
// race-safe — this SELECT runs only to explain a failure that already
// happened, so the common (successful) case stays a single round trip
// instead of paying for a read-then-validate-then-write on every call.
func (r *Repository) diagnoseTransitionFailure(ctx context.Context, tx pgx.Tx, id uuid.UUID, next domainquotes.CurrencyRateUpdateStatus) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM quote_updates WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return quotes.ErrNotFound
		}
		return fmt.Errorf("read status after failed update: %w", err)
	}
	if !domainquotes.CurrencyRateUpdateStatus(status).CanTransitionTo(next) {
		return fmt.Errorf("invalid status transition: %s -> %s", status, next)
	}
	// status matches and next is a legal move from it, yet the UPDATE still
	// matched zero rows: something changed the row between that UPDATE and
	// this SELECT.
	return fmt.Errorf("%s changed status concurrently", id)
}

// completeSuccessTx closes id by pointing its quote_id at quoteID. Shared by
// CompleteSuccess (after its journal insert, reused=false) and
// CompleteSuccessReuse (no insert to run first, reused=true) — reused feeds
// the DTO's "cached" field so a client can tell a real provider call from a
// still-fresh cache hit.
func (r *Repository) completeSuccessTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, quoteID int64, reused bool, now time.Time) error {
	// Clears error_code/error_message: a retried request that now succeeds
	// shouldn't keep reporting its earlier failure.
	tag, err := tx.Exec(ctx, `
		UPDATE quote_updates
		SET status = $2, quote_id = $3, reused = $6, updated_at = $4, error_code = NULL, error_message = NULL
		WHERE id = $1 AND status = $5
	`, id, string(domainquotes.StatusSucceeded), quoteID, now, string(domainquotes.StatusInProgress), reused)
	if err != nil {
		return fmt.Errorf("update queue row: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return r.diagnoseTransitionFailure(ctx, tx, id, domainquotes.StatusSucceeded)
	}
	return nil
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
		SELECT id, base, quote, status, attempts, error_code, error_message, created_at, updated_at
		FROM quote_updates
		WHERE idempotency_key = $1
	`, key)
	return scanUpdateRequest(row)
}

// ClaimBatch's WHERE covers both the pending queue and the reaper: pending
// rows due by now, plus in_progress rows locked before visibleSince. It
// doesn't go through a status-transition check at all: a reaper reclaim
// (in_progress -> in_progress, refreshing locked_at) isn't a transition the
// domain state machine names, and a batch claim can't afford a
// read-then-validate-then-write per candidate row anyway.
//
// Only a pending_candidates claim increments attempts; a stuck_candidates
// reclaim doesn't. attempts gates DispatchMaxAttempts, which is meant to
// bound genuine retries against the upstream — a worker crashing (or a pass
// simply not getting to a row before another replica's reaper reclaims it,
// see DispatchVisibilityTimeout) shouldn't burn that budget before the
// upstream has failed it even once: ten crash-reclaims followed by one real
// retryable failure would otherwise read as "attempts exhausted" on the
// first actual failure.
//
// pending_candidates and stuck_candidates are claimed as two separate,
// independently-limited CTEs rather than one combined WHERE (status = ...
// OR status = ...) ORDER BY ... LIMIT $1. Measured with EXPLAIN ANALYZE: the
// combined form forces FOR UPDATE SKIP LOCKED to sit between Sort and Limit,
// blocking the top-N-heapsort optimization and forcing a full Sort of the
// whole backlog — ~2ms at 5k pending rows but 117ms at ~305k. Splitting
// keeps each half's Sort bounded by its own index (queue_idx on
// next_attempt_at, stuck_idx on locked_at) and its own LIMIT $1, so at most
// 2*limit rows reach the outer sort that decides final claim order,
// independent of backlog size. stuck_candidates orders by locked_at (its
// index) since in_progress rows are bounded by worker capacity, not queue
// depth, so which $1 stuck rows get picked barely matters; final priority
// is still next_attempt_at.
func (r *Repository) ClaimBatch(ctx context.Context, limit int, now, visibleSince time.Time) ([]domainquotes.CurrencyRateUpdateRequest, error) {
	rows, err := r.pool.Query(ctx, `
		WITH pending_candidates AS (
			SELECT id, next_attempt_at FROM quote_updates
			WHERE status = $4 AND next_attempt_at <= $2
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		),
		stuck_candidates AS (
			SELECT id, next_attempt_at FROM quote_updates
			WHERE status = $5 AND locked_at < $3
			ORDER BY locked_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		),
		claimed AS (
			SELECT id, is_pending FROM (
				SELECT id, next_attempt_at, TRUE AS is_pending FROM pending_candidates
				UNION ALL
				SELECT id, next_attempt_at, FALSE AS is_pending FROM stuck_candidates
			) candidates
			ORDER BY next_attempt_at
			LIMIT $1
		)
		UPDATE quote_updates AS qu
		SET status = $5, locked_at = $2, updated_at = $2,
		    attempts = attempts + CASE WHEN claimed.is_pending THEN 1 ELSE 0 END
		FROM claimed
		WHERE qu.id = claimed.id
		RETURNING qu.id, qu.base, qu.quote, qu.status, qu.attempts, qu.error_code, qu.error_message, qu.created_at, qu.updated_at
	`, limit, now, visibleSince, string(domainquotes.StatusPending), string(domainquotes.StatusInProgress))
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

	quoteID, err := upsertQuote(ctx, tx, pair, rate)
	if err != nil {
		return fmt.Errorf("postgres: complete success: upsert quote: %w", err)
	}

	if err := r.completeSuccessTx(ctx, tx, id, quoteID, false, now); err != nil {
		return fmt.Errorf("postgres: complete success: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: complete success: commit: %w", err)
	}
	return nil
}

// upsertQuote inserts rate as a new quotes row, or — on a race with another
// insert of the same (provider, base, quote, quoted_at) — hands back the
// winner's id instead of a redundant DO UPDATE SET id = id write.
//
// The fallback SELECT runs as its own statement, not a UNION in the same
// query: a UNION would read under the query's original snapshot, which can
// predate the winner's commit. A separate statement is safe because
// Postgres's conflict check makes the INSERT wait for the concurrent
// inserter to finish before reporting DO NOTHING's empty result.
func upsertQuote(ctx context.Context, tx pgx.Tx, pair domainquotes.CurrencyPair, rate domainquotes.CurrencyRate) (int64, error) {
	var quoteID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO quotes (base, quote, rate, provider, quality, derived, indicative, quoted_at, fetched_at, stale_after)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (provider, base, quote, quoted_at) DO NOTHING
		RETURNING id
	`, string(pair.Base()), string(pair.Quote()), rate.Value.String(), rate.Provider, rate.Quality,
		rate.Derived, rate.Indicative, rate.QuotedAt, rate.FetchedAt, rate.StaleAfter).Scan(&quoteID)
	if err == nil {
		return quoteID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	err = tx.QueryRow(ctx, `
		SELECT id FROM quotes WHERE provider = $1 AND base = $2 AND quote = $3 AND quoted_at = $4
	`, rate.Provider, string(pair.Base()), string(pair.Quote()), rate.QuotedAt).Scan(&quoteID)
	if err != nil {
		return 0, fmt.Errorf("read row that won the insert race: %w", err)
	}
	return quoteID, nil
}

// CompleteFailure folds both outcomes into one statement: retryable moves
// the row back to pending (for a later attempt), anything else moves it to
// its terminal failed state.
func (r *Repository) CompleteFailure(
	ctx context.Context, id uuid.UUID, errCode, errMessage string, retryable bool, nextAttemptAt, now time.Time,
) error {
	nextStatus := domainquotes.StatusFailed
	if retryable {
		nextStatus = domainquotes.StatusPending
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: complete failure: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, `
		UPDATE quote_updates
		SET status = $2,
		    next_attempt_at = CASE WHEN $2 = $7 THEN $3 ELSE next_attempt_at END,
		    error_code = $4, error_message = $5, updated_at = $6
		WHERE id = $1 AND status = $8
	`, id, string(nextStatus), nextAttemptAt, errCode, errMessage, now,
		string(domainquotes.StatusPending), string(domainquotes.StatusInProgress))
	if err != nil {
		return fmt.Errorf("postgres: complete failure: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: complete failure: %w", r.diagnoseTransitionFailure(ctx, tx, id, nextStatus))
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: complete failure: commit: %w", err)
	}
	return nil
}

func (r *Repository) GetUpdateByID(ctx context.Context, id uuid.UUID) (domainquotes.CurrencyRateUpdateRequest, *domainquotes.CurrencyRate, error) {
	var (
		reqID                           uuid.UUID
		base, quote, status             string
		attempts                        int
		errCode, errMessage             *string
		createdAt, updatedAt            time.Time
		reused                          *bool
		rateStr                         *string
		derived, indicative             *bool
		provider, quality               *string
		quotedAt, fetchedAt, staleAfter *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT qu.id, qu.base, qu.quote, qu.status, qu.attempts, qu.error_code, qu.error_message, qu.created_at, qu.updated_at,
		       qu.reused, q.rate::text, q.derived, q.indicative, q.provider, q.quality, q.quoted_at, q.fetched_at, q.stale_after
		FROM quote_updates qu
		LEFT JOIN quotes q ON q.id = qu.quote_id
		WHERE qu.id = $1
	`, id).Scan(&reqID, &base, &quote, &status, &attempts, &errCode, &errMessage, &createdAt, &updatedAt,
		&reused, &rateStr, &derived, &indicative, &provider, &quality, &quotedAt, &fetchedAt, &staleAfter)
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
		ID:           reqID,
		Pair:         pair,
		Status:       domainquotes.CurrencyRateUpdateStatus(status),
		Attempts:     attempts,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		ErrorCode:    derefOrEmpty(errCode),
		ErrorMessage: derefOrEmpty(errMessage),
		Reused:       reused != nil && *reused,
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

func (r *Repository) GetLatestQuote(ctx context.Context, pair domainquotes.CurrencyPair) (domainquotes.CurrencyRate, int64, error) {
	var (
		id                              int64
		rateStr                         string
		derived, indicative             bool
		provider, quality               string
		quotedAt, fetchedAt, staleAfter time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, rate::text, derived, indicative, provider, quality, quoted_at, fetched_at, stale_after
		FROM quotes
		WHERE base = $1 AND quote = $2
		ORDER BY quoted_at DESC, id DESC
		LIMIT 1
	`, string(pair.Base()), string(pair.Quote())).Scan(
		&id, &rateStr, &derived, &indicative, &provider, &quality, &quotedAt, &fetchedAt, &staleAfter)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domainquotes.CurrencyRate{}, 0, quotes.ErrNotFound
		}
		return domainquotes.CurrencyRate{}, 0, fmt.Errorf("postgres: get latest quote: %w", err)
	}

	value, err := decimal.NewFromString(rateStr)
	if err != nil {
		return domainquotes.CurrencyRate{}, 0, fmt.Errorf("postgres: parse rate: %w", err)
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
	}, id, nil
}

// CompleteSuccessReuse closes id by pointing its quote_id at an existing
// quotes row (quoteID) — the reuse counterpart to CompleteSuccess. Unlike
// CompleteSuccess there's no journal insert to run first, so this is just
// completeSuccessTx wrapped in its own transaction.
func (r *Repository) CompleteSuccessReuse(ctx context.Context, id uuid.UUID, quoteID int64, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: complete success reuse: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := r.completeSuccessTx(ctx, tx, id, quoteID, true, now); err != nil {
		return fmt.Errorf("postgres: complete success reuse: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: complete success reuse: commit: %w", err)
	}
	return nil
}
