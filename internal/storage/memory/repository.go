// Package memory implements usecase/quotes.Repository entirely in-process,
// for use-case tests that would otherwise need a running PostgreSQL.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
	"uuid"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
)

var _ quotes.Repository = (*Repository)(nil)

// record holds a CurrencyRateUpdateRequest plus the queue bookkeeping
// columns storage/postgres keeps in quote_updates but that don't belong on
// the domain type itself.
type record struct {
	req            domainquotes.CurrencyRateUpdateRequest
	idempotencyKey string
	nextAttemptAt  time.Time
	lockedAt       time.Time
	quoteID        int64 // 0 means "no quote yet"
}

type storedQuote struct {
	pair domainquotes.CurrencyPair
	rate domainquotes.CurrencyRate
}

// Repository is a mutex-guarded, in-memory stand-in for storage/postgres.
type Repository struct {
	mu sync.Mutex

	records          map[uuid.UUID]*record
	idempotencyIndex map[string]uuid.UUID

	quotes      map[int64]storedQuote
	nextQuoteID int64
}

func NewRepository() *Repository {
	return &Repository{
		records:          make(map[uuid.UUID]*record),
		idempotencyIndex: make(map[string]uuid.UUID),
		quotes:           make(map[int64]storedQuote),
	}
}

func (r *Repository) CreateUpdateRequest(_ context.Context, req domainquotes.CurrencyRateUpdateRequest, idempotencyKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if idempotencyKey != "" {
		if _, exists := r.idempotencyIndex[idempotencyKey]; exists {
			return quotes.ErrIdempotencyKeyExists
		}
	}

	r.records[req.ID] = &record{
		req:            req,
		idempotencyKey: idempotencyKey,
		nextAttemptAt:  req.CreatedAt,
	}
	if idempotencyKey != "" {
		r.idempotencyIndex[idempotencyKey] = req.ID
	}
	return nil
}

func (r *Repository) GetByIdempotencyKey(_ context.Context, key string) (domainquotes.CurrencyRateUpdateRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id, ok := r.idempotencyIndex[key]
	if !ok {
		return domainquotes.CurrencyRateUpdateRequest{}, quotes.ErrNotFound
	}
	return r.records[id].req, nil
}

// ClaimBatch mirrors storage/postgres's folded claim+reaper WHERE: pending
// rows due by now, plus in_progress rows locked before visibleSince, both
// ordered by next_attempt_at.
func (r *Repository) ClaimBatch(_ context.Context, limit int, now, visibleSince time.Time) ([]domainquotes.CurrencyRateUpdateRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var candidates []*record
	for _, rec := range r.records {
		switch rec.req.Status {
		case domainquotes.StatusPending:
			if !rec.nextAttemptAt.After(now) {
				candidates = append(candidates, rec)
			}
		case domainquotes.StatusInProgress:
			if rec.lockedAt.Before(visibleSince) {
				candidates = append(candidates, rec)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].nextAttemptAt.Equal(candidates[j].nextAttemptAt) {
			return candidates[i].nextAttemptAt.Before(candidates[j].nextAttemptAt)
		}
		return candidates[i].req.ID.String() < candidates[j].req.ID.String()
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Compute every transition before mutating anything: storage/postgres
	// claims in one UPDATE statement, which can't apply to some rows and
	// fail on others, so this can't either. TransitionTo has a value
	// receiver, so this loop has no side effects to undo if it returns early.
	claimed := make([]domainquotes.CurrencyRateUpdateRequest, len(candidates))
	for i, rec := range candidates {
		var next domainquotes.CurrencyRateUpdateRequest
		if rec.req.Status == domainquotes.StatusPending {
			var err error
			if next, err = rec.req.TransitionTo(domainquotes.StatusInProgress, now); err != nil {
				return nil, err
			}
		} else {
			// Already in_progress: the reaper reclaims it, not a fresh
			// pending->in_progress transition, so just refresh bookkeeping.
			next = rec.req
			next.UpdatedAt = now
		}
		// storage/postgres's ClaimBatch increments attempts on every claim,
		// reaper reclaims included, so this mirrors that regardless of
		// which branch above ran.
		next.Attempts++
		claimed[i] = next
	}

	for i, rec := range candidates {
		rec.req = claimed[i]
		rec.lockedAt = now
	}
	return claimed, nil
}

func (r *Repository) CompleteSuccess(
	_ context.Context, id uuid.UUID, pair domainquotes.CurrencyPair, rate domainquotes.CurrencyRate, now time.Time,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok || rec.req.Status != domainquotes.StatusInProgress {
		return fmt.Errorf("memory: complete success: %s not in_progress", id)
	}

	// Validate before the journal write below: findOrCreateQuote mutates
	// r.quotes unconditionally, and storage/postgres's version of this write
	// sits in a transaction that rolls back on failure — nothing may be
	// written here either until the transition is known to succeed.
	next, err := rec.req.TransitionTo(domainquotes.StatusSucceeded, now)
	if err != nil {
		return err
	}
	// Cleared here too: a request that failed once (retryable), requeued,
	// and now succeeds must not keep reporting its earlier attempt's error
	// once it's read back.
	next.ErrorCode = ""
	next.ErrorMessage = ""

	rec.quoteID = r.findOrCreateQuote(pair, rate)
	rec.req = next
	return nil
}

// findOrCreateQuote dedups on (provider, pair, quoted_at), same as the
// unique index on the quotes table.
func (r *Repository) findOrCreateQuote(pair domainquotes.CurrencyPair, rate domainquotes.CurrencyRate) int64 {
	for id, q := range r.quotes {
		if q.pair == pair && q.rate.Provider == rate.Provider && q.rate.QuotedAt.Equal(rate.QuotedAt) {
			return id
		}
	}
	r.nextQuoteID++
	r.quotes[r.nextQuoteID] = storedQuote{pair: pair, rate: rate}
	return r.nextQuoteID
}

func (r *Repository) CompleteFailure(
	_ context.Context, id uuid.UUID, errCode, errMessage string, retryable bool, nextAttemptAt, now time.Time,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok || rec.req.Status != domainquotes.StatusInProgress {
		return fmt.Errorf("memory: complete failure: %s not in_progress", id)
	}

	nextStatus := domainquotes.StatusFailed
	if retryable {
		nextStatus = domainquotes.StatusPending
	}
	next, err := rec.req.TransitionTo(nextStatus, now)
	if err != nil {
		return err
	}
	next.ErrorCode = errCode
	next.ErrorMessage = errMessage
	rec.req = next
	if retryable {
		rec.nextAttemptAt = nextAttemptAt
	}
	return nil
}

func (r *Repository) GetUpdateByID(_ context.Context, id uuid.UUID) (domainquotes.CurrencyRateUpdateRequest, *domainquotes.CurrencyRate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok {
		return domainquotes.CurrencyRateUpdateRequest{}, nil, quotes.ErrNotFound
	}
	if rec.quoteID == 0 {
		return rec.req, nil, nil
	}
	rate := r.quotes[rec.quoteID].rate
	return rec.req, &rate, nil
}

// GetLatestQuote orders by quoted_at then id, same tie-break as the SQL
// index.
func (r *Repository) GetLatestQuote(_ context.Context, pair domainquotes.CurrencyPair) (domainquotes.CurrencyRate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var (
		bestID int64
		best   domainquotes.CurrencyRate
		found  bool
	)
	for id, q := range r.quotes {
		if q.pair != pair {
			continue
		}
		if !found || q.rate.QuotedAt.After(best.QuotedAt) || (q.rate.QuotedAt.Equal(best.QuotedAt) && id > bestID) {
			best = q.rate
			bestID = id
			found = true
		}
	}
	if !found {
		return domainquotes.CurrencyRate{}, quotes.ErrNotFound
	}
	return best, nil
}
