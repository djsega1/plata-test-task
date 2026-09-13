package quotes

import (
	"fmt"
	"strings"
)

type CurrencyPair struct {
	base  CurrencyCode
	quote CurrencyCode
}

func (p CurrencyPair) Base() CurrencyCode {
	return p.base
}

func (p CurrencyPair) Quote() CurrencyCode {
	return p.quote
}

func (p CurrencyPair) Slug(sep string) string {
	return string(p.base) + sep + string(p.quote)
}

func (p CurrencyPair) String() string {
	return p.Slug("/")
}

func ParseCurrencyPair(s, sep string) (CurrencyPair, error) {
	codes := strings.Split(s, sep)

	if len(codes) != 2 {
		return CurrencyPair{}, fmt.Errorf("pair doesn't match iso 4217")
	}

	base, err := NewCurrencyCode(codes[0])
	if err != nil {
		return CurrencyPair{}, fmt.Errorf("invalid base code: %w", err)
	}

	quote, err := NewCurrencyCode(codes[1])
	if err != nil {
		return CurrencyPair{}, fmt.Errorf("invalid quote code: %w", err)
	}

	return NewCurrencyPair(base, quote)
}

func NewCurrencyPair(base, quote CurrencyCode) (CurrencyPair, error) {
	if !base.Valid() {
		return CurrencyPair{}, fmt.Errorf("invalid base code: %s", base)
	}

	if !quote.Valid() {
		return CurrencyPair{}, fmt.Errorf("invalid quote code: %s", quote)
	}

	if base == quote {
		return CurrencyPair{}, fmt.Errorf("base code (%s) equals to quote code (%s)", base, quote)
	}

	return CurrencyPair{
		base:  base,
		quote: quote,
	}, nil
}
