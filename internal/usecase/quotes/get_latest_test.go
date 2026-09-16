package quotes_test

import (
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

func TestGetLatest_NotFound(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := quotes.GetLatest(t.Context(), repo, "EUR/USD")
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

func TestGetLatest_RejectsPairOutsideAllowList(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := quotes.GetLatest(t.Context(), repo, "XXX/USD")
	assert.Error(t, err)
}

func TestGetLatest_ReturnsSucceededQuote(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)
	pair := testPair(t)

	created, err := quotes.RequestUpdate(t.Context(), discardLogger(), repo, fc, "EUR/USD", "")
	require.NoError(t, err)

	_, err = repo.ClaimBatch(t.Context(), 10, testNow, testNow.Add(-time.Hour))
	require.NoError(t, err)

	rate, err := domainquotes.NewCurrencyRate(
		decimal.RequireFromString("1.0824"), false, "live", "exchangerate.dev",
		true, testNow, testNow, testNow.Add(time.Minute),
	)
	require.NoError(t, err)
	require.NoError(t, repo.CompleteSuccess(t.Context(), created.Request.ID, pair, rate, testNow))

	_, got, err := quotes.GetLatest(t.Context(), repo, "EUR/USD")
	require.NoError(t, err)
	assert.True(t, rate.Value.Equal(got.Value))
}
