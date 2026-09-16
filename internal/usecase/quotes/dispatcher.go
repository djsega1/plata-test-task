package quotes

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
)

// Dispatcher claims due work and hands it to a bounded pool of Workers.
type Dispatcher struct {
	repo   Repository
	clock  Clock
	worker *Worker
	logger *slog.Logger

	batchSize         int
	poolSize          int
	visibilityTimeout time.Duration
	passTimeout       time.Duration
}

func NewDispatcher(
	repo Repository, clock Clock, worker *Worker, logger *slog.Logger,
	batchSize, poolSize int, visibilityTimeout, passTimeout time.Duration,
) *Dispatcher {
	return &Dispatcher{
		repo: repo, clock: clock, worker: worker, logger: logger,
		batchSize: batchSize, poolSize: poolSize, visibilityTimeout: visibilityTimeout, passTimeout: passTimeout,
	}
}

// ClaimAndDispatch claims one batch and runs it with at most poolSize
// requests in flight at once, reporting whether the batch was full
// (Run uses that to decide whether to claim again immediately). A
// Worker.Process error is logged, not propagated, so one bad row doesn't
// stop the rest of the batch.
func (d *Dispatcher) ClaimAndDispatch(ctx context.Context) (full bool, err error) {
	now := d.clock.Now()
	visibleSince := now.Add(-d.visibilityTimeout)

	claimed, err := d.repo.ClaimBatch(ctx, d.batchSize, now, visibleSince)
	if err != nil {
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}
	d.logger.Debug("claimed batch", "count", len(claimed), "batch_size", d.batchSize, "full", len(claimed) == d.batchSize)

	// Plain errgroup.Group, not errgroup.WithContext: every Go func below
	// always returns nil, so there's no error for an auto-cancelling
	// context to react to. Used only for SetLimit's bounded pool.
	var g errgroup.Group
	g.SetLimit(d.poolSize)
	for _, req := range claimed {
		g.Go(func() error {
			// errgroup doesn't recover panics; one bad row must not take
			// down the rest of the batch. Same handling as a Process error:
			// log it, leave the row in_progress for the reaper.
			defer func() {
				if r := recover(); r != nil {
					d.logger.Error("panic processing update", "id", req.ID, "pair", req.Pair.String(), "panic", r)
				}
			}()
			if err := d.worker.Process(ctx, req); err != nil {
				d.logger.Error("process update", "id", req.ID, "pair", req.Pair.String(), "error", err)
			}
			return nil
		})
	}
	_ = g.Wait() // every Go func above always returns nil; this just drains the pool.
	return len(claimed) == d.batchSize, nil
}

// runPassRecovered wraps ClaimAndDispatch, turning a panic into an error.
// Covers ClaimBatch itself, which sits outside the errgroup's per-row
// recover — an unrecovered panic there would crash the whole process, not
// just this loop. Only Run uses it; direct callers (tests) still see panics.
func (d *Dispatcher) runPassRecovered(ctx context.Context) (full bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in claim and dispatch: %v", r)
		}
	}()
	return d.ClaimAndDispatch(ctx)
}

// Run drives ClaimAndDispatch from three triggers: a non-blocking nudge, a
// ticker (picks up backoff-deferred work and work from another replica),
// and ctx cancellation to stop.
//
// ctx only gates whether a new pass may *start*. Each pass runs on
// context.WithoutCancel(ctx) with its own passTimeout, so a pass already in
// flight keeps running to completion on shutdown instead of aborting
// in-progress upstream calls. passTimeout still bounds it in case any step
// (not just the upstream call) hangs.
//
// A full batch means more work is likely queued right behind it, so Run
// claims again immediately instead of waiting for the next nudge/tick.
func (d *Dispatcher) Run(ctx context.Context, nudge <-chan struct{}, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudge:
		case <-tick:
		}
		for {
			passCtx, cancelPass := context.WithTimeout(context.WithoutCancel(ctx), d.passTimeout)
			full, err := d.runPassRecovered(passCtx)
			cancelPass()
			if err != nil {
				d.logger.Error("claim and dispatch", "error", err)
				break
			}
			if !full {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}
