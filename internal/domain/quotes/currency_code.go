package quotes

import (
	"fmt"
	"slices"
)

type CurrencyCode string

const (
	CodeUSD CurrencyCode = "USD"
	CodeMXN CurrencyCode = "MXN"
	CodeEUR CurrencyCode = "EUR"
)

func (c CurrencyCode) Valid() bool {
	return slices.Contains([]CurrencyCode{CodeUSD, CodeMXN, CodeEUR}, c)
}

func NewCurrencyCode(s string) (CurrencyCode, error) {
	c := CurrencyCode(s)
	if !c.Valid() {
		return "", fmt.Errorf("unsupported currency code: %s", s)
	}
	return c, nil
}
