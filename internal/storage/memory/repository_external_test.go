package memory_test

import (
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

// testNow is a fixed instant, not time.Now() (see storage/postgres/repository_test.go).
var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

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
	repo := memory.NewRepository()
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
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
	require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
}

func TestGetByIdempotencyKey_NotFound(t *testing.T) {
	repo := memory.NewRepository()

	_, err := repo.GetByIdempotencyKey(t.Context(), "missing")
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

func TestClaimBatch_ConcurrentDoesNotDoubleIssue(t *testing.T) {
	repo := memory.NewRepository()
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

func TestClaimBatch_RespectsLimit(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	for range 5 {
		require.NoError(t, repo.CreateUpdateRequest(ctx, domainquotes.NewCurrencyRateUpdateRequest(pair, now), ""))
	}

	claimed, err := repo.ClaimBatch(ctx, 3, now, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Len(t, claimed, 3)
}

func TestClaimBatch_IgnoresNotYetDuePending(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now.Add(time.Minute))
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))

	claimed, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, claimed, "next_attempt_at in the future must not be claimed yet")
}

func TestClaimBatch_ReclaimsStuckInProgress(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))

	claimed, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	// No CompleteSuccess/CompleteFailure follows — simulates a worker crash
	// while the row sits locked at `now`.

	later := now.Add(time.Minute)

	notYetStuck, err := repo.ClaimBatch(ctx, 10, later, later.Add(-30*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, notYetStuck, "visibleSince before locked_at must not reclaim it")

	reclaimed, err := repo.ClaimBatch(ctx, 10, later, later.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	assert.Equal(t, req.ID, reclaimed[0].ID)
}

func TestCompleteSuccess(t *testing.T) {
	repo := memory.NewRepository()
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
	assert.True(t, rate.QuotedAt.Equal(gotRate.QuotedAt))
	assert.True(t, rate.FetchedAt.Equal(gotRate.FetchedAt))
	assert.True(t, rate.StaleAfter.Equal(gotRate.StaleAfter))

	latest, latestID, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	assert.True(t, rate.Value.Equal(latest.Value))
	assert.NotZero(t, latestID)
}

// TestCompleteSuccess_ClearsPriorFailure covers a request that failed once
// (retryable), requeued, and then succeeded: it must not keep reporting its
// earlier attempt's error once read back.
func TestCompleteSuccess_ClearsPriorFailure(t *testing.T) {
	repo := memory.NewRepository()
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

func TestCompleteSuccess_RequiresInProgress(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	// Not claimed — still pending.

	err := repo.CompleteSuccess(ctx, req.ID, pair, testRate(t, now), now)
	assert.Error(t, err)
}

func TestCompleteSuccessReuse(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	first := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, first, ""))
	second := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, second, ""))

	_, err := repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	rate := testRate(t, now)
	require.NoError(t, repo.CompleteSuccess(ctx, first.ID, pair, rate, now))

	_, quoteID, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteSuccessReuse(ctx, second.ID, quoteID, now))

	gotReq, gotRate, err := repo.GetUpdateByID(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
	require.NotNil(t, gotRate)
	assert.True(t, rate.Value.Equal(gotRate.Value))
}

func TestCompleteSuccessReuse_RequiresInProgress(t *testing.T) {
	repo := memory.NewRepository()
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
	repo := memory.NewRepository()
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

	tooEarly, err := repo.ClaimBatch(ctx, 10, nextAttempt.Add(-time.Second), now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, tooEarly, "must not be claimable before its own next_attempt_at")

	dueNow, err := repo.ClaimBatch(ctx, 10, nextAttempt, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, dueNow, 1)
	assert.Equal(t, req.ID, dueNow[0].ID)
}

func TestCompleteFailure_NotRetryable(t *testing.T) {
	repo := memory.NewRepository()
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
}

func TestCompleteFailure_RequiresInProgress(t *testing.T) {
	repo := memory.NewRepository()
	ctx := t.Context()
	now := testNow
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req, ""))
	// Not claimed — still pending.

	err := repo.CompleteFailure(ctx, req.ID, "provider_unavailable", "upstream 503", true, now.Add(time.Second), now)
	assert.Error(t, err)
}

func TestGetLatestQuote_NotFound(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := repo.GetLatestQuote(t.Context(), testPair(t))
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

func TestGetLatestQuote_OrdersByQuotedAtNotWriteOrder(t *testing.T) {
	repo := memory.NewRepository()
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
	// Complete the older quoted_at last: fetch/write order must not matter,
	// only quoted_at.
	require.NoError(t, repo.CompleteSuccess(ctx, newer.ID, pair, newerRate, now))
	require.NoError(t, repo.CompleteSuccess(ctx, older.ID, pair, olderRate, now))

	latest, _, err := repo.GetLatestQuote(ctx, pair)
	require.NoError(t, err)
	assert.True(t, newerRate.Value.Equal(latest.Value))
}

func TestGetUpdateByID_NotFound(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := repo.GetUpdateByID(t.Context(), uuid.NewV7())
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}
