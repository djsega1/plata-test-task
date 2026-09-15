package quotes

import (
	"context"
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
}

func NewDispatcher(
	repo Repository, clock Clock, worker *Worker, logger *slog.Logger,
	batchSize, poolSize int, visibilityTimeout time.Duration,
) *Dispatcher {
	return &Dispatcher{
		repo: repo, clock: clock, worker: worker, logger: logger,
		batchSize: batchSize, poolSize: poolSize, visibilityTimeout: visibilityTimeout,
	}
}

// ClaimAndDispatch claims one batch and runs it with at most poolSize
// requests in flight at once. A Worker.Process error is infrastructural
// (see Worker.Process) — it's logged, not propagated, so one bad row
// doesn't stop the rest of the batch.
func (d *Dispatcher) ClaimAndDispatch(ctx context.Context) error {
	now := d.clock.Now()
	visibleSince := now.Add(-d.visibilityTimeout)

	claimed, err := d.repo.ClaimBatch(ctx, d.batchSize, now, visibleSince)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(d.poolSize)
	for _, req := range claimed {
		g.Go(func() error {
			if err := d.worker.Process(gctx, req); err != nil {
				d.logger.Error("process update", "id", req.ID, "pair", req.Pair.String(), "error", err)
			}
			return nil
		})
	}
	return g.Wait()
}

// Run drives ClaimAndDispatch from three independent triggers: a
// non-blocking nudge, a ticker (the only mechanism that picks up
// backoff-deferred work and work created by another replica), and ctx
// cancellation to stop. It carries no business logic of its own, so it is
// not exercised on fake clocks the way ClaimAndDispatch is — nudge and tick
// are passed in as plain channels precisely so a caller could still drive
// this loop deterministically if it ever needed to.
func (d *Dispatcher) Run(ctx context.Context, nudge <-chan struct{}, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudge:
		case <-tick:
		}
		if err := d.ClaimAndDispatch(ctx); err != nil {
			d.logger.Error("claim and dispatch", "error", err)
		}
	}
}
