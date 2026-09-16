package quotes

import (
	"fmt"
	"slices"
	"time"
	"uuid"
)

type CurrencyRateUpdateStatus string

const (
	StatusPending    CurrencyRateUpdateStatus = "pending"
	StatusInProgress CurrencyRateUpdateStatus = "in_progress"
	StatusSucceeded  CurrencyRateUpdateStatus = "succeeded"
	StatusFailed     CurrencyRateUpdateStatus = "failed"
)

// validNextStatuses lists each status's allowed next statuses. in_progress
// can return to pending: a retryable provider error schedules a retry, and
// the reaper reclaims a row stuck past its visibility timeout the same way.
var validNextStatuses = map[CurrencyRateUpdateStatus][]CurrencyRateUpdateStatus{
	StatusPending:    {StatusInProgress},
	StatusInProgress: {StatusSucceeded, StatusFailed, StatusPending},
}

func (s CurrencyRateUpdateStatus) Valid() bool {
	return slices.Contains([]CurrencyRateUpdateStatus{StatusPending, StatusInProgress, StatusSucceeded, StatusFailed}, s)
}

func (s CurrencyRateUpdateStatus) CanTransitionTo(next CurrencyRateUpdateStatus) bool {
	return slices.Contains(validNextStatuses[s], next)
}

type CurrencyRateUpdateRequest struct {
	ID        uuid.UUID
	Pair      CurrencyPair
	Status    CurrencyRateUpdateStatus
	CreatedAt time.Time
	UpdatedAt time.Time

	// Attempts counts every claim (including reaper reclaims) for the
	// request's lifetime; it never resets. ErrorCode/ErrorMessage report the
	// most recent failure and can be non-empty on a Pending row mid-backoff;
	// only CompleteSuccess clears them.
	Attempts     int
	ErrorCode    string
	ErrorMessage string
}

func NewCurrencyRateUpdateRequest(pair CurrencyPair, now time.Time) CurrencyRateUpdateRequest {
	return CurrencyRateUpdateRequest{
		ID:        uuid.NewV7(),
		Pair:      pair,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (req CurrencyRateUpdateRequest) TransitionTo(next CurrencyRateUpdateStatus, now time.Time) (CurrencyRateUpdateRequest, error) {
	if !req.Status.CanTransitionTo(next) {
		return CurrencyRateUpdateRequest{}, fmt.Errorf("invalid status transition: %s -> %s", req.Status, next)
	}
	req.Status = next
	req.UpdatedAt = now
	return req, nil
}
