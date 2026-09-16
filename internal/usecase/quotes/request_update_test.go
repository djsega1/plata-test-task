package quotes_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

// testNow is a fixed instant, not time.Now(); every test's relative times
// are derived from it (see storage/postgres/repository_test.go).
var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testPair(t *testing.T) domainquotes.CurrencyPair {
	t.Helper()
	pair, err := domainquotes.NewCurrencyPair(domainquotes.CodeEUR, domainquotes.CodeUSD)
	require.NoError(t, err)
	return pair
}

func TestRequestUpdate_CreatesPending(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	result, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "")
	require.NoError(t, err)
	assert.False(t, result.Replayed)
	assert.Equal(t, domainquotes.StatusPending, result.Request.Status)
	assert.Equal(t, testPair(t), result.Request.Pair)
}

func TestRequestUpdate_ReplaysSameKeySamePair(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	first, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)
	require.False(t, first.Replayed)

	fc.Advance(time.Minute)
	second, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)
	assert.True(t, second.Replayed)
	assert.Equal(t, first.Request.ID, second.Request.ID)
}

func TestRequestUpdate_ConflictsSameKeyDifferentPair(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	_, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)

	_, err = quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/MXN", "key-1")
	assert.ErrorIs(t, err, quotes.ErrIdempotencyConflict)
}

func TestRequestUpdate_EmptyKeyDoesNotCollide(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	first, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "")
	require.NoError(t, err)
	second, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "")
	require.NoError(t, err)
	assert.NotEqual(t, first.Request.ID, second.Request.ID)
}

func TestRequestUpdate_RejectsPairOutsideAllowList(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	_, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "XXX/USD", "")
	assert.Error(t, err)
}

func TestRequestUpdate_LogsCreated(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	var buf bytes.Buffer
	result, err := quotes.RequestUpdate(t.Context(), bufferLogger(&buf, slog.LevelInfo), repo, fc, "EUR/USD", "")
	require.NoError(t, err)

	line := findLogLine(t, decodeLogLines(t, &buf), "update requested")
	assert.Equal(t, "EUR/USD", line["pair"])
	assert.Equal(t, result.Request.ID.String(), line["id"])
}

func TestRequestUpdate_LogsIdempotencyReplay(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	_, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)

	var buf bytes.Buffer
	fc.Advance(time.Minute)
	_, err = quotes.RequestUpdate(t.Context(), bufferLogger(&buf, slog.LevelInfo), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)

	line := findLogLine(t, decodeLogLines(t, &buf), "idempotency replay")
	assert.Equal(t, "EUR/USD", line["pair"])
	assert.Equal(t, "key-1", line["key"])
}

func TestRequestUpdate_LogsIdempotencyConflictAtWarn(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	_, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "key-1")
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = quotes.RequestUpdate(t.Context(), bufferLogger(&buf, slog.LevelInfo), repo, fc, "EUR/MXN", "key-1")
	assert.ErrorIs(t, err, quotes.ErrIdempotencyConflict)

	line := findLogLine(t, decodeLogLines(t, &buf), "idempotency conflict")
	assert.Equal(t, "WARN", line["level"])
	assert.Equal(t, "EUR/USD", line["existing_pair"])
	assert.Equal(t, "EUR/MXN", line["requested_pair"])
}
