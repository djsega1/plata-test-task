package quotes_test

import (
	"testing"

	"github.com/djsega1/plata-test-task/internal/domain/quotes"
)

func TestCurrencyCode_Valid(t *testing.T) {
	tests := []struct {
		code quotes.CurrencyCode
		want bool
	}{
		{quotes.CodeUSD, true},
		{quotes.CodeEUR, true},
		{quotes.CodeMXN, true},
		{quotes.CurrencyCode("GBP"), false},
		{quotes.CurrencyCode(""), false},
	}
	for _, tt := range tests {
		if got := tt.code.Valid(); got != tt.want {
			t.Errorf("CurrencyCode(%q).Valid() = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestNewCurrencyCode(t *testing.T) {
	got, err := quotes.NewCurrencyCode("USD")
	if err != nil {
		t.Fatalf("NewCurrencyCode(USD): unexpected error: %v", err)
	}
	if got != quotes.CodeUSD {
		t.Errorf("NewCurrencyCode(USD) = %v, want %v", got, quotes.CodeUSD)
	}

	if _, err := quotes.NewCurrencyCode("GBP"); err == nil {
		t.Error("NewCurrencyCode(GBP): want error for a code outside the allow-list")
	}
}
