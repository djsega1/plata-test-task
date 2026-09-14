package memory

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// TestCompleteSuccess_DedupsSameQuotedAt is an internal (package memory, not
// memory_test) test: it needs the unexported quotes map to prove dedup, the
// same way storage/postgres/migrate_test.go reaches goose internals — the
// Repository port itself has no read that would expose how many journal
// rows exist.
func TestCompleteSuccess_DedupsSameQuotedAt(t *testing.T) {
	repo := NewRepository()
	ctx := t.Context()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	pair, err := domainquotes.NewCurrencyPair(domainquotes.CodeEUR, domainquotes.CodeUSD)
	require.NoError(t, err)
	rate, err := domainquotes.NewCurrencyRate(
		decimal.RequireFromString("18.4321"), false, "open", "live", "exchangerate.dev",
		true, now, now, now.Add(time.Minute),
	)
	require.NoError(t, err)

	req1 := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req1, ""))
	req2 := domainquotes.NewCurrencyRateUpdateRequest(pair, now)
	require.NoError(t, repo.CreateUpdateRequest(ctx, req2, ""))

	_, err = repo.ClaimBatch(ctx, 10, now, now.Add(-time.Hour))
	require.NoError(t, err)

	require.NoError(t, repo.CompleteSuccess(ctx, req1.ID, pair, rate, now))
	require.NoError(t, repo.CompleteSuccess(ctx, req2.ID, pair, rate, now))

	assert.Len(t, repo.quotes, 1, "same provider/pair/quoted_at must dedup to one journal row")
	assert.Equal(t, repo.records[req1.ID].quoteID, repo.records[req2.ID].quoteID)
}
