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
	pool, err := postgres.NewPool(ctx, databaseURL, 0, 0, 0)
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

// TestClaimBatch_MergesPendingAndStuckByPriority exercises ClaimBatch's
// pending_candidates/stuck_candidates split together in one call, not each
// in isolation like the two tests above: a stuck row and a newer pending
// row both eligible at once, with limit=1 forcing ClaimBatch to pick one —
// it must be the stuck row, since its own next_attempt_at (unchanged by the
// earlier claim that made it in_progress) is older than the pending row's.
func TestClaimBatch_MergesPendingAndStuckByPriority(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	pair := testPair(t)

	older := domainquotes.NewCurrencyRateUpdateRequest(pair, testNow)
	require.NoError(t, repo.CreateUpdateRequest(ctx, older, ""))
	newer := domainquotes.NewCurrencyRateUpdateRequest(pair, testNow.Add(time.Minute))
	require.NoError(t, repo.CreateUpdateRequest(ctx, newer, ""))

	// Claim and strand "older" as stuck in_progress — its next_attempt_at
	// column is untouched by this claim, so it stays testNow.
	claimed, err := repo.ClaimBatch(ctx, 1, testNow, testNow.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, older.ID, claimed[0].ID)
	_, err = pool.Exec(ctx, "UPDATE quote_updates SET locked_at = $1 WHERE id = $2", testNow.Add(-time.Hour), older.ID)
	require.NoError(t, err)

	// Both older (stuck, next_attempt_at = testNow) and newer (pending,
	// next_attempt_at = testNow+1m) are now eligible. limit=1 must pick the
	// stuck row: its priority is older, even though it comes from the
	// stuck_candidates CTE, not pending_candidates.
	now := testNow.Add(2 * time.Minute)
	visibleSince := testNow.Add(time.Minute)
	first, err := repo.ClaimBatch(ctx, 1, now, visibleSince)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, older.ID, first[0].ID, "the stuck row's older next_attempt_at must win priority over the pending row")

	// A second call claims what's left — both rows end up claimed exactly
	// once between the two calls, proving the merge doesn't drop either side.
	second, err := repo.ClaimBatch(ctx, 10, now, visibleSince)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, newer.ID, second[0].ID)
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

	latest, latestID, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	assert.True(t, rate.Value.Equal(latest.Value))
	assert.NotZero(t, latestID)
}

// TestCompleteSuccess_ClearsPriorFailure covers a request that failed once
// (retryable), requeued, and then succeeded: it must not keep reporting its
// earlier attempt's error once read back.
func TestCompleteSuccess_ClearsPriorFailure(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.CompleteFailure(ctx, req.ID, "provider_unavailable", "upstream 503", true, now, now))

	_, err = repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.CompleteSuccess(ctx, req.ID, pair, testRate(t, now), now))

	gotReq, _, err := repo.GetUpdateByID(ctx, req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
	assert.Empty(t, gotReq.ErrorCode)
	assert.Empty(t, gotReq.ErrorMessage)
	assert.Equal(t, 2, gotReq.Attempts, "Attempts keeps accumulating even though the error is cleared")
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

// TestCompleteSuccessReuse covers Worker's cache-hit path: closing a
// request by pointing it at an already-existing quotes row, without
// inserting anything new. This is the write-amplification fix — without a
// dedicated method, every reuse (the common case once a pair has a live
// quote) would re-run the same INSERT ... ON CONFLICT DO UPDATE as a fresh
// fetch, on every single request instead of only the ones that actually
// called the provider.
func TestCompleteSuccessReuse(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	first := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, first, ""))
	second := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, second, ""))

	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	require.NoError(t, repo.CompleteSuccess(ctx, first.ID, pair, testRate(t, now), now))

	_, quoteID, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteSuccessReuse(ctx, second.ID, quoteID, now))

	var quoteCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM quotes").Scan(&quoteCount))
	assert.Equal(t, 1, quoteCount, "reuse must not insert a second journal row")

	gotReq, gotRate, err := repo.GetUpdateByID(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
	require.NotNil(t, gotRate)
	assert.True(t, testRate(t, now).Value.Equal(gotRate.Value))
}

func TestCompleteSuccessReuse_RequiresInProgress(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	// Not claimed — still pending.

	err := repo.CompleteSuccessReuse(ctx, req.ID, 1, now)
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
	assert.Equal(t, "provider_unavailable", got.ErrorCode)
	assert.Equal(t, "upstream 503", got.ErrorMessage)

	var storedNextAttempt time.Time
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT next_attempt_at FROM quote_updates WHERE id = $1", req.ID,
	).Scan(&storedNextAttempt))
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
	assert.Equal(t, "unsupported_pair", got.ErrorCode)
	assert.Equal(t, "no rate for pair", got.ErrorMessage)
}

func TestGetLatestQuote_NotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)

	_, _, err := repo.GetLatestQuote(t.Context(), testPair(t))
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

// "Latest" orders by quoted_at DESC, id DESC, not insertion order: the
// upstream can return a price older than one already stored. Exercises
// quotes_latest_idx against real Postgres, not just the memory stand-in.
func TestGetLatestQuote_OrdersByQuotedAtNotWriteOrder(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	older := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, older, ""))
	newer := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, newer, ""))
	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	olderRate := testRate(t, now.Add(-time.Hour))
	newerRate := testRate(t, now)
	// Complete the older quoted_at last: fetch/write order must not
	// matter, only quoted_at.
	require.NoError(t, repo.CompleteSuccess(ctx, newer.ID, pair, newerRate, now))
	require.NoError(t, repo.CompleteSuccess(ctx, older.ID, pair, olderRate, now))

	latest, _, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	assert.True(t, newerRate.Value.Equal(latest.Value))
}

func TestGetUpdateByID_NotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewRepository(pool)

	_, _, err := repo.GetUpdateByID(t.Context(), uuid.NewV7())
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}
