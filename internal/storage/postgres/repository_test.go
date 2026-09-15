//go:build integration

package postgres_test

import (
	"os"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/postgres"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// testNow is a fixed instant, not time.Now(): every test's own relative
// times (Add(-time.Hour), etc.) are derived from it, so nothing here
// depends on when the test actually runs.
var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newTestPool connects to TEST_DATABASE_URL, migrates it, and truncates the
// tables this package owns so each test starts from an empty slate.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	require.NoError(t, postgres.Migrate(t.Context(), databaseURL))

	ctx := t.Context()
	pool, err := postgres.NewPool(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, "TRUNCATE quote_updates, quotes RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	return pool
}

func testPair(t *testing.T) domainquotes.CurrencyPair {
	t.Helper()
	pair, err := domainquotes.NewCurrencyPair(domainquotes.CodeEUR, domainquotes.CodeUSD)
	require.NoError(t, err)
	return pair
}

func testRate(t *testing.T, quotedAt time.Time) domainquotes.CurrencyRate {
	t.Helper()
	rate, err := domainquotes.NewCurrencyRate(
		decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
		true, quotedAt, quotedAt, quotedAt.Add(time.Minute),
	)
	require.NoError(t, err)
	return rate
}

func TestCreateUpdateRequest_IdempotencyKeyUniqueness(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	first := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, first, "key-1"))

	second := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	err := repo.CreateUpdateRequest(ctx, second, "key-1")
	require.ErrorIs(t, err, quotes.ErrIdempotencyKeyExists)

	got, err := repo.GetByIdempotencyKey(ctx, "key-1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)
}

func TestCreateUpdateRequest_EmptyKeyDoesNotCollide(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
	require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
}

func TestClaimBatch_ConcurrentDoesNotDoubleIssue(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	const numRequests = 20
	for range numRequests {
		require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
	}

	var (
		mu     sync.Mutex
		seen   = make(map[uuid.UUID]bool)
		wg     sync.WaitGroup
		claims int
	)
	const numWorkers = 8
	for range numWorkers {
		wg.Go(func() {
			claimed, err := repo.ClaimBatch(ctx, 5, now, now.Add(-time.Hour))
			assert.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()
			for _, req := range claimed {
				assert.False(t, seen[req.ID], "id %s claimed twice", req.ID)
				seen[req.ID] = true
				claims++
			}
		})
	}
	wg.Wait()

	assert.Equal(t, numRequests, claims)
	assert.Len(t, seen, numRequests)
}

func TestClaimBatch_ReclaimsStuckInProgress(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))

	claimed, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Simulate a worker crash: push locked_at into the past directly.
	_, err = pool.Exec(ctx, "UPDATE quote_updates SET locked_at = $1 WHERE id = $2", now.Add(-time.Hour), req.ID)
	require.NoError(t, err)

	later := now.Add(time.Minute)

	notYetStuck, err := repo.ClaimBatch(ctx, 10, later, later.Add(-30*time.Minute-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, notYetStuck, "visibleSince before locked_at must not reclaim it")

	reclaimed, err := repo.ClaimBatch(ctx, 10, later, later.Add(-30*time.Minute))
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, req.ID, reclaimed[0].ID)

	var attempts int
	require.NoError(t, pool.QueryRow(ctx, "SELECT attempts FROM quote_updates WHERE id = $1", req.ID).Scan(&attempts))
	assert.Equal(t, 2, attempts, "one attempt per claim: initial + reclaim")
}

func TestCompleteSuccess(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	claimed, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	rate := testRate(t, now)
	require.NoError(t, repo.CompleteSuccess(ctx, req.ID, pair, rate, now))

	gotReq, gotRate, err := repo.GetUpdateByID(ctx, req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
	require.NotNil(t, gotRate)
	assert.True(t, rate.Value.Equal(gotRate.Value))
	assert.Equal(t, rate.Provider, gotRate.Provider)
	assert.Equal(t, rate.Quality, gotRate.Quality)
	assert.Equal(t, rate.Derived, gotRate.Derived)
	assert.Equal(t, rate.Indicative, gotRate.Indicative)
	assert.WithinDuration(t, rate.QuotedAt, gotRate.QuotedAt, time.Millisecond)
	assert.WithinDuration(t, rate.FetchedAt, gotRate.FetchedAt, time.Millisecond)
	assert.WithinDuration(t, rate.StaleAfter, gotRate.StaleAfter, time.Millisecond)

	latest, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	assert.True(t, rate.Value.Equal(latest.Value))
}

func TestCompleteSuccess_DedupsSameQuotedAt(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)
	rate := testRate(t, now)

	req1 := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req1, ""))
	req2 := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req2, ""))

	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	require.NoError(t, repo.CompleteSuccess(ctx, req1.ID, pair, rate, now))
	require.NoError(t, repo.CompleteSuccess(ctx, req2.ID, pair, rate, now))

	var quoteCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM quotes").Scan(&quoteCount))
	assert.Equal(t, 1, quoteCount, "same provider/pair/quoted_at must dedup to one journal row")

	var q1, q2 *int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT quote_id FROM quote_updates WHERE id = $1", req1.ID).Scan(&q1))
	require.NoError(t, pool.QueryRow(ctx, "SELECT quote_id FROM quote_updates WHERE id = $1", req2.ID).Scan(&q2))
	require.NotNil(t, q1)
	require.NotNil(t, q2)
	assert.Equal(t, *q1, *q2)
}

func TestCompleteSuccess_RequiresInProgress(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	// Not claimed — still pending.

	err := repo.CompleteSuccess(ctx, req.ID, pair, testRate(t, now), now)
	assert.Error(t, err)
}

func TestCompleteFailure_Retryable(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	nextAttempt := now.Add(30 * time.Second)
	require.NoError(t, repo.CompleteFailure(ctx, req.ID, "provider_unavailable", "upstream 503", true, nextAttempt, now))

	got, _, err := repo.GetUpdateByID(ctx, req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, got.Status)

	var errCode, errMessage string
	var storedNextAttempt time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT error_code, error_message, next_attempt_at FROM quote_updates WHERE id = $1", req.ID,
	).Scan(&errCode, &errMessage, &storedNextAttempt))
	assert.Equal(t, "provider_unavailable", errCode)
	assert.Equal(t, "upstream 503", errMessage)
	// Postgres TIMESTAMPTZ is microsecond precision, time.Now() is
	// nanosecond — exact .Equal would flake on the round trip.
	assert.WithinDuration(t, nextAttempt, storedNextAttempt, time.Millisecond)
}

func TestCompleteFailure_NotRetryable(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	require.NoError(t, repo.CompleteFailure(ctx, req.ID, "unsupported_pair", "no rate for pair", false, time.Time{}, now))

	got, _, err := repo.GetUpdateByID(ctx, req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, got.Status)

	var errCode, errMessage string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT error_code, error_message FROM quote_updates WHERE id = $1", req.ID,
	).Scan(&errCode, &errMessage))
	assert.Equal(t, "unsupported_pair", errCode)
	assert.Equal(t, "no rate for pair", errMessage)
}

func TestGetLatestQuote_NotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)

	_, err := repo.GetLatestQuote(t.Context(), testPair(t))
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

func TestGetUpdateByID_NotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)

	_, _, err := repo.GetUpdateByID(t.Context(), uuid.NewV7())
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}
