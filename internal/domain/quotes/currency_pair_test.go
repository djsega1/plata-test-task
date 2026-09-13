package quotes_test

import (
	"testing"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

func TestNewCurrencyPair(t *testing.T) {
	t.Run("valid pair", func(t *testing.T) {
		pair, err := quotes.NewCurrencyPair(quotes.CodeEUR, quotes.CodeUSD)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pair.Base() != quotes.CodeEUR {
			t.Errorf("Base() = %v, want %v", pair.Base(), quotes.CodeEUR)
		}
		if pair.Quote() != quotes.CodeUSD {
			t.Errorf("Quote() = %v, want %v", pair.Quote(), quotes.CodeUSD)
		}
	})

	t.Run("invalid base", func(t *testing.T) {
		if _, err := quotes.NewCurrencyPair(quotes.CurrencyCode("GBP"), quotes.CodeUSD); err == nil {
			t.Error("want error for invalid base code")
		}
	})

	t.Run("invalid quote", func(t *testing.T) {
		if _, err := quotes.NewCurrencyPair(quotes.CodeUSD, quotes.CurrencyCode("GBP")); err == nil {
			t.Error("want error for invalid quote code")
		}
	})

	t.Run("base equals quote", func(t *testing.T) {
		if _, err := quotes.NewCurrencyPair(quotes.CodeUSD, quotes.CodeUSD); err == nil {
			t.Error("want error for base == quote — a currency has no rate against itself")
		}
	})
}

func TestCurrencyPair_SlugAndString(t *testing.T) {
	pair, err := quotes.NewCurrencyPair(quotes.CodeEUR, quotes.CodeMXN)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := pair.Slug("-"), "EUR-MXN"; got != want {
		t.Errorf(`Slug("-") = %q, want %q`, got, want)
	}
	if got, want := pair.Slug("/"), "EUR/MXN"; got != want {
		t.Errorf(`Slug("/") = %q, want %q`, got, want)
	}
	if got, want := pair.String(), "EUR/MXN"; got != want {
		t.Errorf("String() = %q, want %q (the public contract format, not the upstream slug)", got, want)
	}
}

func TestParseCurrencyPair(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		pair, err := quotes.ParseCurrencyPair("EUR/MXN", "/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pair.Base() != quotes.CodeEUR || pair.Quote() != quotes.CodeMXN {
			t.Errorf("got %s, want EUR/MXN", pair)
		}
	})

	t.Run("round-trips through String", func(t *testing.T) {
		original, err := quotes.NewCurrencyPair(quotes.CodeUSD, quotes.CodeEUR)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		parsed, err := quotes.ParseCurrencyPair(original.String(), "/")
		if err != nil {
			t.Fatalf("ParseCurrencyPair(%q): unexpected error: %v", original.String(), err)
		}
		if parsed != original {
			t.Errorf("round trip: got %s, want %s", parsed, original)
		}
	})

	t.Run("no separator in input", func(t *testing.T) {
		if _, err := quotes.ParseCurrencyPair("EURMXN", "/"); err == nil {
			t.Error("want error: no separator found")
		}
	})

	t.Run("too many parts", func(t *testing.T) {
		if _, err := quotes.ParseCurrencyPair("EUR/MXN/USD", "/"); err == nil {
			t.Error("want error: more than two parts")
		}
	})

	t.Run("invalid code", func(t *testing.T) {
		if _, err := quotes.ParseCurrencyPair("EUR/GBP", "/"); err == nil {
			t.Error("want error for a quote code outside the allow-list")
		}
	})
}
