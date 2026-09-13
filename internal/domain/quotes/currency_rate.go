package quotes

import (
	"time"

	"github.com/shopspring/decimal"
)

type CurrencyRate struct {
	Value         decimal.Decimal
	Derived       bool
	MarketSession string
	Quality       string
	QuotedAt      time.Time
}
