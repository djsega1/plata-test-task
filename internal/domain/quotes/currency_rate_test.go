package quotes_test

import (
	"testing"
	"time"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/shopspring/decimal"
)

func TestNewCurrencyRate(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	t.Run("valid", func(t *testing.T) {
		rate, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("18.4321"), false, "open", "live", "exchangerate.dev",
			true, now, now, now.Add(time.Minute),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !rate.Value.Equal(decimal.RequireFromString("18.4321")) {
			t.Errorf("Value = %s, want 18.4321", rate.Value)
		}
		if rate.Provider != "exchangerate.dev" {
			t.Errorf("Provider = %q, want %q", rate.Provider, "exchangerate.dev")
		}
	})

	t.Run("zero rate", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.Zero, false, "open", "live", "exchangerate.dev", true, now, now, now,
		); err == nil {
			t.Error("want error for zero rate")
		}
	})

	t.Run("negative rate", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("-1"), false, "open", "live", "exchangerate.dev", true, now, now, now,
		); err == nil {
			t.Error("want error for negative rate")
		}
	})
}
