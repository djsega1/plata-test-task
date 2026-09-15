package quotes_test

import (
	"testing"
	"time"
	"uuid"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

func TestCurrencyRateUpdateStatusValid(t *testing.T) {
	valid := []quotes.CurrencyRateUpdateStatus{
		quotes.StatusPending, quotes.StatusInProgress, quotes.StatusSucceeded, quotes.StatusFailed,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}

	if quotes.CurrencyRateUpdateStatus("bogus").Valid() {
		t.Error(`"bogus".Valid() = true, want false`)
	}
}

func TestCanTransitionTo(t *testing.T) {
	tests := []struct {
		from, to quotes.CurrencyRateUpdateStatus
		want     bool
	}{
		{quotes.StatusPending, quotes.StatusInProgress, true},
		{quotes.StatusPending, quotes.StatusSucceeded, false},
		{quotes.StatusPending, quotes.StatusFailed, false},
		{quotes.StatusPending, quotes.StatusPending, false},
		{quotes.StatusInProgress, quotes.StatusSucceeded, true},
		{quotes.StatusInProgress, quotes.StatusFailed, true},
		{quotes.StatusInProgress, quotes.StatusPending, true},
		{quotes.StatusInProgress, quotes.StatusInProgress, false},
		{quotes.StatusSucceeded, quotes.StatusPending, false},
		{quotes.StatusSucceeded, quotes.StatusInProgress, false},
		{quotes.StatusFailed, quotes.StatusPending, false},
		{quotes.StatusFailed, quotes.StatusInProgress, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Errorf("%s.CanTransitionTo(%s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

// testPair is EUR/USD — the specific pair doesn't matter to the tests in
// this file, they just need one that's known-valid.
func testPair(t *testing.T) quotes.CurrencyPair {
	t.Helper()
	pair, err := quotes.NewCurrencyPair(quotes.CodeEUR, quotes.CodeUSD)
	if err != nil {
		t.Fatalf("NewCurrencyPair: unexpected error: %v", err)
	}
	return pair
}

func TestNewCurrencyRateUpdateRequest(t *testing.T) {
	pair := testPair(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	req := quotes.NewCurrencyRateUpdateRequest(pair, now)

	if req.ID == uuid.Nil() {
		t.Error("ID is nil")
	}
	if req.Pair != pair {
		t.Errorf("Pair = %v, want %v", req.Pair, pair)
	}
	if req.Status != quotes.StatusPending {
		t.Errorf("Status = %v, want %v", req.Status, quotes.StatusPending)
	}
	if !req.CreatedAt.Equal(now) || !req.UpdatedAt.Equal(now) {
		t.Errorf("CreatedAt/UpdatedAt = %v/%v, want both %v", req.CreatedAt, req.UpdatedAt, now)
	}
}

func TestNewCurrencyRateUpdateRequestIDsAreOrdered(t *testing.T) {
	pair := testPair(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	first := quotes.NewCurrencyRateUpdateRequest(pair, now)
	second := quotes.NewCurrencyRateUpdateRequest(pair, now)

	if first.ID.Compare(second.ID) >= 0 {
		t.Errorf("IDs not increasing: first=%s second=%s — want a UUIDv7, not v4", first.ID, second.ID)
	}
}

func TestCurrencyRateUpdateRequestTransitionTo(t *testing.T) {
	pair := testPair(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	req := quotes.NewCurrencyRateUpdateRequest(pair, created)

	t.Run("valid transition", func(t *testing.T) {
		later := created.Add(time.Second)
		next, err := req.TransitionTo(quotes.StatusInProgress, later)
		if err != nil {
			t.Fatalf("TransitionTo: unexpected error: %v", err)
		}
		if next.Status != quotes.StatusInProgress {
			t.Errorf("Status = %v, want %v", next.Status, quotes.StatusInProgress)
		}
		if !next.UpdatedAt.Equal(later) {
			t.Errorf("UpdatedAt = %v, want %v", next.UpdatedAt, later)
		}
		if next.ID != req.ID || next.Pair != req.Pair || !next.CreatedAt.Equal(req.CreatedAt) {
			t.Errorf("TransitionTo changed ID/Pair/CreatedAt: got %+v, from %+v", next, req)
		}
	})

	t.Run("invalid transition", func(t *testing.T) {
		if _, err := req.TransitionTo(quotes.StatusSucceeded, created); err == nil {
			t.Error("want error for pending -> succeeded")
		}
	})
}
