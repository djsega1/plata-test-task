package quotes

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMalformedPair and ErrPairNotAllowed distinguish two different failure
// shapes that ParseCurrencyPair/NewCurrencyPair used to both report as the
// same opaque error: a string that isn't even shaped like a pair, versus a
// well-formed pair this service just doesn't support. The api/http layer
// maps them to 400 and 422 respectively (docs/design.md §4) — a malformed
// shape is a bad request, not a rejected allow-list lookup.
var (
	ErrMalformedPair  = errors.New("pair is in wrong format")
	ErrPairNotAllowed = errors.New("pair is outside the allow-list")
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

	if len(codes) != 2 || codes[0] == "" || codes[1] == "" {
		return CurrencyPair{}, fmt.Errorf("%w: %q doesn't match BASE%sQUOTE", ErrMalformedPair, s, sep)
	}

	base, err := NewCurrencyCode(codes[0])
	if err != nil {
		return CurrencyPair{}, fmt.Errorf("%w: invalid base code: %w", ErrPairNotAllowed, err)
	}

	quote, err := NewCurrencyCode(codes[1])
	if err != nil {
		return CurrencyPair{}, fmt.Errorf("%w: invalid quote code: %w", ErrPairNotAllowed, err)
	}

	return NewCurrencyPair(base, quote)
}

func NewCurrencyPair(base, quote CurrencyCode) (CurrencyPair, error) {
	if !base.Valid() {
		return CurrencyPair{}, fmt.Errorf("%w: invalid base code: %s", ErrPairNotAllowed, base)
	}

	if !quote.Valid() {
		return CurrencyPair{}, fmt.Errorf("%w: invalid quote code: %s", ErrPairNotAllowed, quote)
	}

	if base == quote {
		return CurrencyPair{}, fmt.Errorf("%w: base code (%s) equals quote code (%s)", ErrPairNotAllowed, base, quote)
	}

	return CurrencyPair{
		base:  base,
		quote: quote,
	}, nil
}
