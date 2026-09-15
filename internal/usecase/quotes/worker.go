package quotes

import (
	"context"
	"errors"
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
	sf       singleflight.Group

	baseBackoff time.Duration
}

func NewWorker(repo Repository, provider RateProvider, clock Clock, limiter *RateLimiter, baseBackoff time.Duration) *Worker {
	return &Worker{repo: repo, provider: provider, clock: clock, limiter: limiter, baseBackoff: baseBackoff}
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
		return w.fail(ctx, req.ID, err, now)
	}
	return w.repo.CompleteSuccess(ctx, req.ID, req.Pair, rate, now)
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
			return latest, nil
		case err != nil && !errors.Is(err, ErrNotFound):
			return nil, err
		}
		// Stale or missing (ErrNotFound): fall through to fetch a fresh quote.

		if ok, retryAfter := w.limiter.Allow(w.clock.Now()); !ok {
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
// treats it like any other infrastructure failure.
func (w *Worker) fail(ctx context.Context, id uuid.UUID, err error, now time.Time) error {
	var rateErr domainquotes.CurrencyRateError
	if !errors.As(err, &rateErr) {
		return err
	}

	nextAttemptAt := w.backoff(rateErr, now)
	return w.repo.CompleteFailure(ctx, id, string(rateErr.Code()), rateErr.Error(), rateErr.Retryable(), nextAttemptAt, now)
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
