package quotes_test

import (
	"errors"
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
		_, err := quotes.NewCurrencyPair(quotes.CurrencyCode("GBP"), quotes.CodeUSD)
		if !errors.Is(err, quotes.ErrPairNotAllowed) {
			t.Errorf("want ErrPairNotAllowed for invalid base code, got %v", err)
		}
	})

	t.Run("invalid quote", func(t *testing.T) {
		_, err := quotes.NewCurrencyPair(quotes.CodeUSD, quotes.CurrencyCode("GBP"))
		if !errors.Is(err, quotes.ErrPairNotAllowed) {
			t.Errorf("want ErrPairNotAllowed for invalid quote code, got %v", err)
		}
	})

	t.Run("base equals quote", func(t *testing.T) {
		_, err := quotes.NewCurrencyPair(quotes.CodeUSD, quotes.CodeUSD)
		if !errors.Is(err, quotes.ErrPairNotAllowed) {
			t.Errorf("want ErrPairNotAllowed for base == quote — a currency has no rate against itself, got %v", err)
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
		_, err := quotes.ParseCurrencyPair("EURMXN", "/")
		if !errors.Is(err, quotes.ErrMalformedPair) {
			t.Errorf("want ErrMalformedPair: no separator found, got %v", err)
		}
	})

	t.Run("too many parts", func(t *testing.T) {
		_, err := quotes.ParseCurrencyPair("EUR/MXN/USD", "/")
		if !errors.Is(err, quotes.ErrMalformedPair) {
			t.Errorf("want ErrMalformedPair: more than two parts, got %v", err)
		}
	})

	t.Run("empty segment", func(t *testing.T) {
		_, err := quotes.ParseCurrencyPair("/MXN", "/")
		if !errors.Is(err, quotes.ErrMalformedPair) {
			t.Errorf("want ErrMalformedPair: empty base segment, got %v", err)
		}
	})

	t.Run("invalid code", func(t *testing.T) {
		_, err := quotes.ParseCurrencyPair("EUR/GBP", "/")
		if !errors.Is(err, quotes.ErrPairNotAllowed) {
			t.Errorf("want ErrPairNotAllowed for a quote code outside the allow-list, got %v", err)
		}
	})

	t.Run("lowercase is normalized, not rejected as unsupported", func(t *testing.T) {
		pair, err := quotes.ParseCurrencyPair("eur/mxn", "/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pair.Base() != quotes.CodeEUR || pair.Quote() != quotes.CodeMXN {
			t.Errorf("got %s, want EUR/MXN", pair)
		}
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		pair, err := quotes.ParseCurrencyPair("  EUR/MXN  ", "/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pair.Base() != quotes.CodeEUR || pair.Quote() != quotes.CodeMXN {
			t.Errorf("got %s, want EUR/MXN", pair)
		}
	})
}
