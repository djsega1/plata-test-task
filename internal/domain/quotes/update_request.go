package quotes

import (
	"time"
	"uuid"
)

type CurrencyRateUpdateStatus string

type CurrencyRateUpdateRequest struct {
	ID        uuid.UUID
	Pair      CurrencyPair
	Status    CurrencyRateUpdateStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}
