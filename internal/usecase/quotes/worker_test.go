package quotes_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

// claimOne creates one pending request and claims it, returning the
// now-in_progress row Worker.Process expects.
func claimOne(t *testing.T, repo *memory.Repository, pair domainquotes.CurrencyPair, now time.Time) domainquotes.CurrencyRateUpdateRequest {
	t.Helper()
	req := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	claimed, err := repo.ClaimBatch(t.Context(), 1, now, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return claimed[0]
}

func TestWorker_Process_PendingToSucceeded(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, gotRate, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
	require.NotNil(t, gotRate)
	assert.Equal(t, "test-provider", gotRate.Provider)
	assert.True(t, gotRate.Indicative)
	assert.Equal(t, 1, provider.callCount())
}

func TestWorker_Process_ReusesFreshQuoteWithoutCallingProvider(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

	first := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), first))
	require.Equal(t, 1, provider.callCount())

	fc.Advance(time.Second) // still inside the 1-minute StaleAfter window
	second := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), second))

	assert.Equal(t, 1, provider.callCount(), "second request must reuse the still-fresh quote")

	_, secondRate, err := repo.GetUpdateByID(t.Context(), second.ID)
	require.NoError(t, err)
	require.NotNil(t, secondRate)
	_, firstRate, err := repo.GetUpdateByID(t.Context(), first.ID)
	require.NoError(t, err)
	assert.True(t, firstRate.QuotedAt.Equal(secondRate.QuotedAt), "both point at the same journal row")
}

func TestWorker_Process_RetryableFailureDefersWithBackoff(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.ProviderUnavailableError, "upstream down", true, 30*time.Second, "",
	)}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), 10*time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, gotRate, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, gotReq.Status)
	assert.Nil(t, gotRate)

	// Not claimable yet: even the error's own 30s RetryAfter hasn't passed.
	notYet, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(20*time.Second), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, notYet)

	dueLater, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(time.Minute), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Len(t, dueLater, 1)
}

// TestWorker_Process_BackoffGrowsExponentiallyWithAttempts guards the
// backoff/max_attempts pair actually composing into a bounded-but-real
// retry window: a constant, attempts-blind delay (what this used to be)
// would put attempt 2's next_attempt_at in the same window as attempt 1's,
// which the second probe below would catch.
func TestWorker_Process_BackoffGrowsExponentiallyWithAttempts(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.ProviderUnavailableError, "upstream down", true, 0, "",
	)}
	const baseBackoff = 10 * time.Second
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), baseBackoff, time.Hour, 10)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	// Attempt 1 (exponent 0): jittered delay lands in [7.5s, 12.5s).
	claimed, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, worker.Process(t.Context(), claimed[0]))

	notYet1, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(7*time.Second), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, notYet1, "attempt 1's delay must be at least baseBackoff*0.75")

	// Reclaim for attempt 2, safely past attempt 1's window.
	claimed2, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(13*time.Second), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed2, 1)
	require.Equal(t, 2, claimed2[0].Attempts)
	require.NoError(t, worker.Process(t.Context(), claimed2[0]))

	// Attempt 2 (exponent 1): jittered delay lands in [15s, 25s) — the fake
	// clock never advances here, so both attempts' delays are anchored at
	// the same fc.Now(); a constant backoff would land attempt 2 back in
	// attempt 1's [7.5s, 12.5s) window, which this probe would catch.
	notYet2, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(13*time.Second), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, notYet2, "attempt 2's delay must have grown past attempt 1's own window")

	due2, err := repo.ClaimBatch(t.Context(), 10, fc.Now().Add(26*time.Second), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Len(t, due2, 1, "attempt 2's delay must still be bounded (roughly 2x attempt 1's)")
}

func TestWorker_Process_PermanentFailureFails(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.UnsupportedPairError, "no rate for pair", false, 0, "",
	)}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status)
}

// TestWorker_Process_UnclassifiedErrorLeavesRowInProgressForTheReaper covers
// Worker.fail's documented behaviour for an error that isn't a
// domainquotes.CurrencyRateError, while there's still retry budget left: it
// isn't this service's to classify, so it's returned as-is rather than
// recorded via CompleteFailure — the row stays in_progress and the reaper
// (ClaimBatch's own WHERE) reclaims it later, rather than silently landing
// in an unrecorded, un-backed-off state. This is exactly the gap a provider
// adapter that fails to classify a transport error (see exchangeratedev's
// ProviderUnavailableError wrapping) would otherwise fall into: without this
// path returning the error, a caller (Dispatcher) would have nothing to log
// and no way to tell a genuine bug in an adapter's error handling from a
// normal classified failure.
func TestWorker_Process_UnclassifiedErrorLeavesRowInProgressForTheReaper(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	unclassified := errors.New("boom: adapter forgot to classify this")
	provider := &countingProvider{err: unclassified}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	err := worker.Process(t.Context(), req)

	require.ErrorIs(t, err, unclassified, "an unclassified error must propagate, not be swallowed")

	gotReq, _, getErr := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domainquotes.StatusInProgress, gotReq.Status,
		"CompleteFailure must not be called for an error Worker can't classify — the reaper reclaims it instead")
}

// TestWorker_Process_UnclassifiedErrorFailsPermanentlyOnceAttemptsExhausted
// covers the other half of that same budget: without it, a bug that always
// fails before a row reaches CompleteFailure (e.g. a Repository outage)
// would cycle the row through the reaper forever — never recorded, never
// surfaced to a client polling GET /quotes/updates/{id}.
func TestWorker_Process_UnclassifiedErrorFailsPermanentlyOnceAttemptsExhausted(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	unclassified := errors.New("boom: adapter forgot to classify this")
	provider := &countingProvider{err: unclassified}
	const maxAttempts = 3
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, maxAttempts)

	req := claimOne(t, repo, pair, fc.Now())
	req.Attempts = maxAttempts // as if the reaper had already reclaimed it up to the budget
	err := worker.Process(t.Context(), req)
	require.NoError(t, err, "once budget is exhausted, fail must record CompleteFailure itself, not propagate")

	gotReq, _, getErr := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status)
	assert.Equal(t, "internal_error", gotReq.ErrorCode)
	assert.Contains(t, gotReq.ErrorMessage, unclassified.Error())
}

func TestWorker_Process_RateLimitedDefersWithoutCallingProvider(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	limiter := quotes.NewRateLimiter(1, 1)
	ok, _ := limiter.Allow(fc.Now()) // consumes the only slot
	require.True(t, ok)

	worker := quotes.NewWorker(repo, provider, fc, limiter, discardLogger(), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	assert.Equal(t, 0, provider.callCount(), "budget exhausted: provider must not be called")

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, gotReq.Status, "deferred, not failed")
}

// TestWorker_Process_RetryAfterCoolsDownLimiterForEveryPair covers the fix
// for a Retry-After that used to affect only the failing row's own backoff:
// every other pair kept spending a budget the upstream just said was empty.
// Now a 429's Retry-After pauses the shared RateLimiter itself, so a second,
// unrelated pair must not reach the provider either while the cooldown is
// still in effect.
func TestWorker_Process_RetryAfterCoolsDownLimiterForEveryPair(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair1 := testPair(t)
	pair2, err := domainquotes.NewCurrencyPair(domainquotes.CodeEUR, domainquotes.CodeMXN)
	require.NoError(t, err)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.RateLimitedError, "too many requests", true, 30*time.Second, "",
	)}
	limiter := quotes.NewRateLimiter(100, 1000) // plenty of room in both windows
	worker := quotes.NewWorker(repo, provider, fc, limiter, discardLogger(), time.Second, time.Minute, 5)

	req1 := claimOne(t, repo, pair1, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req1))
	require.Equal(t, 1, provider.callCount())

	req2 := claimOne(t, repo, pair2, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req2))
	assert.Equal(t, 1, provider.callCount(), "cooldown from pair1's Retry-After must block pair2 too")

	gotReq2, _, err := repo.GetUpdateByID(t.Context(), req2.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, gotReq2.Status, "deferred by the limiter, not failed")
}

func TestWorker_Process_LogsFetchedQuote(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	var buf bytes.Buffer
	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	line := findLogLine(t, decodeLogLines(t, &buf), "fetched quote from provider")
	assert.Equal(t, pair.String(), line["pair"])
	assert.Equal(t, "live", line["quality"])
}

func TestWorker_Process_LogsReusedQuoteAtDebug(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	first := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), first))

	var buf bytes.Buffer
	worker2 := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelDebug), time.Second, time.Minute, 5)
	fc.Advance(time.Second) // still inside the 1-minute StaleAfter window
	second := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker2.Process(t.Context(), second))

	line := findLogLine(t, decodeLogLines(t, &buf), "reusing cached quote")
	assert.Equal(t, pair.String(), line["pair"])
}

func TestWorker_Process_LogsRetryableFailureAtWarn(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	var buf bytes.Buffer
	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.ProviderUnavailableError, "upstream down", true, 30*time.Second, "",
	)}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), 10*time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	line := findLogLine(t, decodeLogLines(t, &buf), "update deferred for retry")
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, pair.String(), line["pair"])
}

func TestWorker_Process_LogsPermanentFailureAtError(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	var buf bytes.Buffer
	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.UnsupportedPairError, "no rate for pair", false, 0, "",
	)}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	line := findLogLine(t, decodeLogLines(t, &buf), "update failed permanently")
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, pair.String(), line["pair"])
}

// TestWorker_Process_MaxAttemptsExhaustedFailsPermanently covers the
// max-attempts termination added alongside classifying transport errors as
// retryable: without it, a permanently unavailable upstream would cycle a
// row through pending/in_progress forever, never telling the client it
// isn't coming back.
func TestWorker_Process_MaxAttemptsExhaustedFailsPermanently(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.ProviderUnavailableError, "upstream down", true, 0, "",
	)}
	const maxAttempts = 3
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Millisecond, time.Minute, maxAttempts)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	for range maxAttempts {
		claimed, err := repo.ClaimBatch(t.Context(), 10, fc.Now(), fc.Now().Add(-time.Hour))
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.NoError(t, worker.Process(t.Context(), claimed[0]))
		fc.Advance(time.Minute) // past the backoff, claimable again if still pending
	}

	gotReq, gotRate, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status, "must stop retrying once maxAttempts is reached")
	assert.Equal(t, "attempts_exhausted", gotReq.ErrorCode)
	assert.Equal(t, maxAttempts, gotReq.Attempts)
	assert.Nil(t, gotRate)
}

func TestWorker_Process_InvalidRateFromProviderFails(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.Zero, Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, gotRate, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status, "a non-positive rate fails NewCurrencyRate's own validation: not retryable")
	assert.Nil(t, gotRate)
}
