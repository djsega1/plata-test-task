package quotes_test

import (
	"bytes"
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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), 10*time.Second)

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

func TestWorker_Process_PermanentFailureFails(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{err: domainquotes.NewCurrencyRateError(
		domainquotes.UnsupportedPairError, "no rate for pair", false, 0, "",
	)}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status)
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

	worker := quotes.NewWorker(repo, provider, fc, limiter, discardLogger(), time.Second)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	assert.Equal(t, 0, provider.callCount(), "budget exhausted: provider must not be called")

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, gotReq.Status, "deferred, not failed")
}

func TestWorker_Process_LogsFetchedQuote(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	var buf bytes.Buffer
	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), time.Second)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second)
	first := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), first))

	var buf bytes.Buffer
	worker2 := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelDebug), time.Second)
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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), 10*time.Second)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), bufferLogger(&buf, slog.LevelInfo), time.Second)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	line := findLogLine(t, decodeLogLines(t, &buf), "update failed permanently")
	assert.Equal(t, "ERROR", line["level"])
	assert.Equal(t, pair.String(), line["pair"])
}

func TestWorker_Process_InvalidRateFromProviderFails(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.Zero, Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second)

	req := claimOne(t, repo, pair, fc.Now())
	require.NoError(t, worker.Process(t.Context(), req))

	gotReq, gotRate, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusFailed, gotReq.Status, "a non-positive rate fails NewCurrencyRate's own validation: not retryable")
	assert.Nil(t, gotRate)
}
