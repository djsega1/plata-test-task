package quotes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
	"uuid"

	"golang.org/x/sync/singleflight"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
)

// attemptsExhaustedCode marks a failure terminal because maxAttempts was
// reached, not because the error itself was unretryable — kept distinct so
// a poison message doesn't look identical to a one-off transient failure.
const attemptsExhaustedCode = "attempts_exhausted"

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
	maxBackoff  time.Duration
	maxAttempts int
}

func NewWorker(
	repo Repository, provider RateProvider, clock Clock, limiter *RateLimiter, logger *slog.Logger,
	baseBackoff, maxBackoff time.Duration, maxAttempts int,
) *Worker {
	return &Worker{
		repo: repo, provider: provider, clock: clock, limiter: limiter, logger: logger,
		baseBackoff: baseBackoff, maxBackoff: maxBackoff, maxAttempts: maxAttempts,
	}
}

// resolvedQuote is what resolve hands back to Process: the rate, plus
// quoteID when reused from an existing row rather than freshly fetched.
type resolvedQuote struct {
	rate    domainquotes.CurrencyRate
	quoteID int64 // 0: rate was freshly fetched and still needs inserting
}

// Process resolves one claimed request. A returned error means nothing was
// recorded — Repository failed, or the error didn't classify as a
// CurrencyRateError — so the row stays in_progress for the reaper. A
// classified failure is handled internally via CompleteFailure and returns
// nil.
func (w *Worker) Process(ctx context.Context, req domainquotes.CurrencyRateUpdateRequest) error {
	resolved, err := w.resolve(ctx, req.Pair)
	// Read after resolve: it can block on a real upstream call, and both
	// CompleteSuccess and fail's backoff must anchor to completion time.
	now := w.clock.Now()
	if err != nil {
		return w.fail(ctx, req.ID, req.Pair, req.Attempts, err, now)
	}

	// A reused quote is already a row in the journal: just point this
	// update at it instead of re-inserting.
	if resolved.quoteID != 0 {
		err = w.repo.CompleteSuccessReuse(ctx, req.ID, resolved.quoteID, now)
	} else {
		err = w.repo.CompleteSuccess(ctx, req.ID, req.Pair, resolved.rate, now)
	}
	if err != nil {
		return err
	}
	w.logger.Info("update succeeded", "id", req.ID, "pair", req.Pair.String(),
		"price", resolved.rate.Value.String(), "quality", resolved.rate.Quality, "provider", resolved.rate.Provider)
	return nil
}

// resolve single-flights the whole reuse-or-fetch decision per pair, not
// just the provider call: re-reading GetLatestQuote inside the same
// critical section closes the window where a caller that read a stale
// quote just before another's fetch lands would otherwise fetch again on
// its own.
func (w *Worker) resolve(ctx context.Context, pair domainquotes.CurrencyPair) (resolvedQuote, error) {
	v, err, _ := w.sf.Do(pair.String(), func() (any, error) {
		latest, quoteID, err := w.repo.GetLatestQuote(ctx, pair)
		switch {
		case err == nil && w.clock.Now().Before(latest.StaleAfter):
			w.logger.Debug("reusing cached quote", "pair", pair.String(), "quality", latest.Quality, "stale_after", latest.StaleAfter)
			return resolvedQuote{rate: latest, quoteID: quoteID}, nil
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
			// A Retry-After from the upstream is a shared budget, not a
			// per-pair one: pause every pair's calls, not just this row's
			// own backoff, or the other five pairs keep spending a budget
			// the upstream just said is empty.
			var rateErr domainquotes.CurrencyRateError
			if errors.As(err, &rateErr) && rateErr.RetryAfter() > 0 {
				w.limiter.CoolDown(w.clock.Now().Add(rateErr.RetryAfter()))
			}
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
		return resolvedQuote{rate: rate}, nil
	})
	if err != nil {
		return resolvedQuote{}, err
	}
	return v.(resolvedQuote), nil
}

// fail records a provider/business failure against id. An err that isn't a
// CurrencyRateError is not this service's to classify, so — while there's
// still retry budget left — it's returned as-is for the caller to log as an
// infrastructure failure, leaving the row in_progress for the reaper.
//
// attempts is req.Attempts as ClaimBatch left it. Once it reaches
// maxAttempts, a retryable error still stops here instead of going back to
// pending, so a permanently unavailable upstream doesn't cycle forever
// unrecorded; errCode becomes attemptsExhaustedCode to distinguish this
// from a genuinely non-retryable error. The same budget applies to an
// unclassified err: without it, a bug that keeps failing before a row ever
// reaches CompleteFailure (a Repository outage, say) would cycle through
// the reaper indefinitely, never recorded and never surfaced to the client
// polling GET /quotes/updates/{id}.
func (w *Worker) fail(ctx context.Context, id uuid.UUID, pair domainquotes.CurrencyPair, attempts int, err error, now time.Time) error {
	var rateErr domainquotes.CurrencyRateError
	if !errors.As(err, &rateErr) {
		if attempts < w.maxAttempts {
			return err
		}
		rateErr = domainquotes.NewCurrencyRateError(domainquotes.InternalError, err.Error(), false, 0, "")
	}

	errCode, errMessage, retryable := string(rateErr.Code()), rateErr.Error(), rateErr.Retryable()
	if retryable && attempts >= w.maxAttempts {
		retryable = false
		errCode = attemptsExhaustedCode
		errMessage = fmt.Sprintf("giving up after %d attempts: %s", attempts, rateErr.Error())
	}

	nextAttemptAt := w.backoff(rateErr, attempts, now)
	if err := w.repo.CompleteFailure(ctx, id, errCode, errMessage, retryable, nextAttemptAt, now); err != nil {
		return err
	}

	// Warn if retryable (expected, will retry), Error if permanent.
	level, msg := slog.LevelWarn, "update deferred for retry"
	if !retryable {
		level, msg = slog.LevelError, "update failed permanently"
	}
	w.logger.Log(ctx, level, msg,
		"id", id, "pair", pair.String(), "code", errCode, "error", errMessage, "attempts", attempts, "next_attempt_at", nextAttemptAt)
	return nil
}

// backoff grows baseBackoff exponentially with attempts (5s, 10s, 20s, ...),
// capped at maxBackoff, then raised further to the error's own RetryAfter
// when that's longer. Spread by +-25% jitter so retries from the same
// failure don't line up.
func (w *Worker) backoff(rateErr domainquotes.CurrencyRateError, attempts int, now time.Time) time.Time {
	shift := max(attempts-1, 0)
	delay := w.baseBackoff * time.Duration(1<<uint(shift))
	if delay <= 0 || delay > w.maxBackoff { // delay <= 0: overflowed
		delay = w.maxBackoff
	}
	if rateErr.RetryAfter() > delay {
		delay = rateErr.RetryAfter()
	}
	jitter := 0.75 + rand.Float64()*0.5
	return now.Add(time.Duration(float64(delay) * jitter))
}
