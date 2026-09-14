package quotes_test

import (
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

func TestGetByUpdateID_NotFound(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := quotes.GetByUpdateID(t.Context(), repo, uuid.NewV7().String())
	assert.ErrorIs(t, err, quotes.ErrNotFound)
}

func TestGetByUpdateID_InvalidUUID(t *testing.T) {
	repo := memory.NewRepository()

	_, _, err := quotes.GetByUpdateID(t.Context(), repo, "not-a-uuid")
	assert.Error(t, err)
}

func TestGetByUpdateID_ReturnsPendingRequestWithoutRate(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(testNow)

	created, err := quotes.RequestUpdate(t.Context(), repo, fc, "EUR/USD", "")
	require.NoError(t, err)

	req, rate, err := quotes.GetByUpdateID(t.Context(), repo, created.Request.ID.String())
	require.NoError(t, err)
	assert.Nil(t, rate)
	assert.Equal(t, domainquotes.StatusPending, req.Status)
}
