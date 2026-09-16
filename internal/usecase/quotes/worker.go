package quotes

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
	"uuid"

	"golang.org/x/sync/singleflight"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// Worker resolves a single claimed update request: reuse a quote still
// inside its StaleAfter window, or fetch a fresh one from the provider.
type Worker struct {
	repo     Repository
	provider RateProvider
	clock    Clock
	limiter  *RateLimiter
	logger   *slog.Logger
	sf       singleflight.Group

	baseBackoff time.Duration
}

func NewWorker(repo Repository, provider RateProvider, clock Clock, limiter *RateLimiter, logger *slog.Logger, baseBackoff time.Duration) *Worker {
	return &Worker{repo: repo, provider: provider, clock: clock, limiter: limiter, logger: logger, baseBackoff: baseBackoff}
}

// Process resolves one claimed request. A returned error means it wasn't
// recorded anywhere — either Repository itself failed, or the error from
// resolve didn't classify as a CurrencyRateError (see fail) — so the row
// stays in_progress and the reaper reclaims it. A classified provider or
// business failure is handled internally via CompleteFailure and returns
// nil.
func (w *Worker) Process(ctx context.Context, req domainquotes.CurrencyRateUpdateRequest) error {
	rate, err := w.resolve(ctx, req.Pair)
	// now is read after resolve, not before: resolve can block on a real
	// upstream call, and both CompleteSuccess's bookkeeping and fail's
	// backoff deadline must anchor to when processing actually finished,
	// not when it started.
	now := w.clock.Now()
	if err != nil {
		return w.fail(ctx, req.ID, req.Pair, err, now)
	}
	if err := w.repo.CompleteSuccess(ctx, req.ID, req.Pair, rate, now); err != nil {
		return err
	}
	w.logger.Info("update succeeded", "id", req.ID, "pair", req.Pair.String(),
		"price", rate.Value.String(), "quality", rate.Quality, "provider", rate.Provider)
	return nil
}

// resolve single-flights the whole decision per pair, not just the
// provider call: concurrent callers for the same pair share one execution
// of this closure, so a caller that read a stale quote just before another
// caller's fetch lands doesn't independently repeat that fetch — it
// re-reads GetLatestQuote inside the same critical section and finds the
// other caller's write. The rate limiter is charged once per execution,
// not once per caller.
func (w *Worker) resolve(ctx context.Context, pair domainquotes.CurrencyPair) (domainquotes.CurrencyRate, error) {
	v, err, _ := w.sf.Do(pair.String(), func() (any, error) {
		latest, err := w.repo.GetLatestQuote(ctx, pair)
		switch {
		case err == nil && w.clock.Now().Before(latest.StaleAfter):
			w.logger.Debug("reusing cached quote", "pair", pair.String(), "quality", latest.Quality, "stale_after", latest.StaleAfter)
			return latest, nil
		case err != nil && !errors.Is(err, ErrNotFound):
			return nil, err
		}
		// Stale or missing (ErrNotFound): fall through to fetch a fresh quote.

		if ok, retryAfter := w.limiter.Allow(w.clock.Now()); !ok {
			w.logger.Warn("outbound rate budget exhausted", "pair", pair.String(), "retry_after", retryAfter)
			return nil, domainquotes.NewCurrencyRateError(
				domainquotes.RateLimitedError, "outbound rate budget exhausted", true, retryAfter, "",
			)
		}

		raw, err := w.provider.GetCurrencyRate(ctx, pair)
		if err != nil {
			return nil, err
		}

		rate, err := domainquotes.NewCurrencyRate(
			raw.Value, raw.Derived, raw.Quality, w.provider.Provider(), w.provider.Indicative(),
			raw.QuotedAt, w.clock.Now(), w.provider.StaleAfter(raw.Quality, raw.QuotedAt),
		)
		if err != nil {
			return nil, domainquotes.NewCurrencyRateError(domainquotes.MalformedResponseError, err.Error(), false, 0, "")
		}
		w.logger.Info("fetched quote from provider", "pair", pair.String(),
			"provider", rate.Provider, "quality", rate.Quality, "derived", rate.Derived)
		return rate, nil
	})
	if err != nil {
		return domainquotes.CurrencyRate{}, err
	}
	return v.(domainquotes.CurrencyRate), nil
}

// fail records a provider/business failure against id. An err that isn't a
// CurrencyRateError (a cancelled context, an adapter's own transport error)
// is not this service's to classify — it's returned as-is, so the caller
// treats it like any other infrastructure failure (and the caller, e.g.
// Dispatcher, logs it — nothing here duplicates that).
func (w *Worker) fail(ctx context.Context, id uuid.UUID, pair domainquotes.CurrencyPair, err error, now time.Time) error {
	var rateErr domainquotes.CurrencyRateError
	if !errors.As(err, &rateErr) {
		return err
	}

	nextAttemptAt := w.backoff(rateErr, now)
	if err := w.repo.CompleteFailure(
		ctx, id, string(rateErr.Code()), rateErr.Error(), rateErr.Retryable(), nextAttemptAt, now,
	); err != nil {
		return err
	}

	// Retryable is logged at Warn (expected, will be retried) vs Error
	// (permanent — e.g. unsupported_pair, auth_error) — this is the only
	// place a classified failure is logged at all; without it, a failed
	// update was only ever visible by querying quote_updates.status='failed'.
	level, msg := slog.LevelWarn, "update deferred for retry"
	if !rateErr.Retryable() {
		level, msg = slog.LevelError, "update failed permanently"
	}
	w.logger.Log(ctx, level, msg,
		"id", id, "pair", pair.String(), "code", rateErr.Code(), "error", rateErr.Error(), "next_attempt_at", nextAttemptAt)
	return nil
}

// backoff is baseBackoff or err's own RetryAfter, whichever is later,
// spread by +-25% jitter so retries from the same failure don't line up.
func (w *Worker) backoff(rateErr domainquotes.CurrencyRateError, now time.Time) time.Time {
	delay := w.baseBackoff
	if rateErr.RetryAfter() > delay {
		delay = rateErr.RetryAfter()
	}
	jitter := 0.75 + rand.Float64()*0.5
	return now.Add(time.Duration(float64(delay) * jitter))
}
