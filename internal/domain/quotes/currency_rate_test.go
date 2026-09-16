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
			decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
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
			decimal.Zero, false, "live", "exchangerate.dev", true, now, now, now,
		); err == nil {
			t.Error("want error for zero rate")
		}
	})

	t.Run("negative rate", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("-1"), false, "live", "exchangerate.dev", true, now, now, now,
		); err == nil {
			t.Error("want error for negative rate")
		}
	})

	t.Run("zero quoted_at", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
			true, time.Time{}, now, now.Add(time.Minute),
		); err == nil {
			t.Error("want error for zero quoted_at")
		}
	})

	t.Run("quoted_at far in the future is rejected", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
			true, now.Add(time.Hour), now, now.Add(time.Hour+time.Minute),
		); err == nil {
			t.Error("want error for quoted_at an hour ahead of fetched_at")
		}
	})

	t.Run("quoted_at within clock-skew tolerance of fetched_at is accepted", func(t *testing.T) {
		if _, err := quotes.NewCurrencyRate(
			decimal.RequireFromString("18.4321"), false, "live", "exchangerate.dev",
			true, now.Add(10*time.Second), now, now.Add(time.Minute),
		); err != nil {
			t.Errorf("unexpected error for quoted_at only 10s ahead of fetched_at: %v", err)
		}
	})
}
