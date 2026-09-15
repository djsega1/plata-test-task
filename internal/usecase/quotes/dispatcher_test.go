package quotes_test

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domainquotes "github.com/djsega1/plata-test-task/internal/domain/quotes"
	"github.com/djsega1/plata-test-task/internal/storage/memory"
	"github.com/djsega1/plata-test-task/internal/usecase/quotes"
	"github.com/djsega1/plata-test-task/pkg/clock"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// allPairs is every ordered pair the allow-list (USD, EUR, MXN) admits.
func allPairs(t *testing.T) []domainquotes.CurrencyPair {
	t.Helper()
	codes := []domainquotes.CurrencyCode{domainquotes.CodeUSD, domainquotes.CodeEUR, domainquotes.CodeMXN}
	var pairs []domainquotes.CurrencyPair
	for _, base := range codes {
		for _, quote := range codes {
			if base == quote {
				continue
			}
			pair, err := domainquotes.NewCurrencyPair(base, quote)
			require.NoError(t, err)
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

func TestDispatcher_ClaimAndDispatch_PendingToSucceeded(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	require.NoError(t, repo.CreateUpdateRequest(t.Context(), domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now()), ""))

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour)

	require.NoError(t, dispatcher.ClaimAndDispatch(t.Context()))

	got, err := quotes.GetLatest(t.Context(), repo, pair.String())
	require.NoError(t, err)
	assert.True(t, decimal.RequireFromString("1.08").Equal(got.Value))
}

// TestDispatcher_PanicInOneUpdateDoesNotStopTheBatch simulates a bug deep in
// GetCurrencyRate (worst case: NewCurrencyRateError panicking on an invalid
// code — see errors.go) panicking instead of returning. ClaimAndDispatch
// must recover it, same as any other Worker.Process failure: logged, that
// one row left in_progress for the reaper, everything else in the batch
// still completes.
func TestDispatcher_PanicInOneUpdateDoesNotStopTheBatch(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pairs := allPairs(t)
	panicPair, okPair := pairs[0], pairs[1]

	panicReq := domainquotes.NewCurrencyRateUpdateRequest(panicPair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), panicReq, ""))
	okReq := domainquotes.NewCurrencyRateUpdateRequest(okPair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), okReq, ""))

	provider := &countingProvider{
		panicPair: &panicPair,
		rate:      quotes.ProviderQuote{Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now()},
	}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour)

	require.NotPanics(t, func() {
		assert.NoError(t, dispatcher.ClaimAndDispatch(t.Context()))
	})

	gotOK, _, err := repo.GetUpdateByID(t.Context(), okReq.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotOK.Status, "a panic in one row must not stop the rest of the batch")

	gotPanicked, _, err := repo.GetUpdateByID(t.Context(), panicReq.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusInProgress, gotPanicked.Status,
		"same outcome as any other unrecorded Worker.Process failure: left in_progress for the reaper")
}

// TestDispatcher_SamePairSingleFlightsToOneProviderCall claims 100 pending
// requests for one pair and proves the provider is called exactly once.
// It drives Worker.Process directly (not through the dispatcher's pool) so
// it can use a real sync.WaitGroup as the "all 100 are in flight" barrier:
// each goroutine signals readiness as its very first action, so
// ready.Wait() cannot return until the Go runtime has scheduled and run
// every one of them at least that far — a real barrier, not a timing
// guess — before the gate (and therefore the single-flight leader) is
// ever allowed to release.
func TestDispatcher_SamePairSingleFlightsToOneProviderCall(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	const numRequests = 100
	for range numRequests {
		req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
		require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	}
	claimed, err := repo.ClaimBatch(t.Context(), numRequests, fc.Now(), fc.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, claimed, numRequests)

	provider := &countingProvider{
		gate: make(chan struct{}),
		rate: quotes.ProviderQuote{Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now()},
	}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)

	var ready, done sync.WaitGroup
	ready.Add(numRequests)
	done.Add(numRequests)
	for _, req := range claimed {
		go func(req domainquotes.CurrencyRateUpdateRequest) {
			defer done.Done()
			ready.Done()
			assert.NoError(t, worker.Process(context.Background(), req))
		}(req)
	}
	waitOrFail(t, &ready, "not all 100 requests reached Process before the gate opened")

	// Extra margin past the barrier for the trivial, non-blocking work
	// (one mutex-guarded map read in GetLatestQuote) between "goroutine
	// started" and "call arrived at singleflight".
	for range 10000 {
		runtime.Gosched()
	}
	close(provider.gate)
	waitOrFail(t, &done, "not all 100 Process calls finished after the gate opened")

	assert.Equal(t, 1, provider.callCount(), "single-flight must collapse same-pair concurrent fetches into one call")

	quotedAts := map[time.Time]bool{}
	for _, req := range claimed {
		gotReq, rate, err := repo.GetUpdateByID(t.Context(), req.ID)
		require.NoError(t, err)
		assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status)
		require.NotNil(t, rate)
		quotedAts[rate.QuotedAt] = true
	}
	assert.Len(t, quotedAts, 1, "every request must point at the same journal row")
}

// waitOrFail is sync.WaitGroup.Wait with a non-blocking-forever escape
// hatch, so a real bug (a goroutine stuck, not just slow) fails the test
// instead of hanging it.
func waitOrFail(t *testing.T, wg *sync.WaitGroup, msg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

// TestDispatcher_DifferentPairsRunConcurrently proves the pool actually
// parallelizes across pairs: it blocks every GetCurrencyRate call on a
// shared gate and requires all six to have arrived — which can only happen
// if six are in flight at once — before releasing them.
func TestDispatcher_DifferentPairsRunConcurrently(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pairs := allPairs(t)
	require.Len(t, pairs, 6)

	for _, pair := range pairs {
		req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
		require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
	}

	provider := &countingProvider{
		gate:    make(chan struct{}),
		arrived: make(chan struct{}),
		rate:    quotes.ProviderQuote{Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now()},
	}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 6, time.Hour)

	done := make(chan error, 1)
	go func() { done <- dispatcher.ClaimAndDispatch(context.Background()) }()

	for range pairs {
		select {
		case <-provider.arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("fewer than six calls were ever in flight at once")
		}
	}
	close(provider.gate)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ClaimAndDispatch did not finish")
	}

	assert.Equal(t, len(pairs), provider.callCount())
	for _, pair := range pairs {
		got, err := quotes.GetLatest(t.Context(), repo, pair.String())
		require.NoError(t, err)
		assert.True(t, decimal.RequireFromString("1.08").Equal(got.Value))
	}
}

func TestDispatcher_ExhaustedBudgetDefersWithoutFailing(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	limiter := quotes.NewRateLimiter(1, 1)
	ok, _ := limiter.Allow(fc.Now()) // consume the only slot before the dispatcher runs
	require.True(t, ok)

	worker := quotes.NewWorker(repo, provider, fc, limiter, time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour)

	require.NoError(t, dispatcher.ClaimAndDispatch(t.Context()))

	assert.Equal(t, 0, provider.callCount())
	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusPending, gotReq.Status, "deferred, not failed")
}

// TestDispatcher_Run drives Run entirely through the nudge/tick channels it
// takes as parameters — no real timer is ever involved. A second send on
// the same channel only succeeds once Run has looped back to its select,
// which only happens after ClaimAndDispatch returns — so it doubles as a
// deterministic "the previous pass is done" barrier, without polling.
func TestDispatcher_Run(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	nudge := make(chan struct{})
	tick := make(chan time.Time)

	runReturned := make(chan struct{})
	go func() {
		dispatcher.Run(ctx, nudge, tick)
		close(runReturned)
	}()

	sendOrFail(t, nudge, struct{}{}, "Run never became ready to receive the nudge")
	sendOrFail(t, nudge, struct{}{}, "the nudge-triggered dispatch pass never completed")

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status, "a nudge must trigger a claim-and-dispatch pass")

	// tick is wired the same way as nudge; one round-trip is enough to show
	// Run is actually selecting on it, not just on nudge.
	sendOrFail(t, tick, time.Time{}, "Run never became ready to receive the tick")
	sendOrFail(t, nudge, struct{}{}, "the tick-triggered dispatch pass never completed")

	cancel()
	select {
	case <-runReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// TestDispatcher_Run_CancelDuringPassDoesNotAbortIt guards the bug found in
// cmd/server/main.go's shutdown: the context Run passes into ClaimAndDispatch
// must survive Run's own ctx being cancelled, so an in-flight pass finishes
// instead of having its upstream call cut out from under it. Cancelling ctx
// while GetCurrencyRate is parked on the gate must leave that call's own
// context un-done; releasing the gate afterwards must still let the request
// reach StatusSucceeded, not abandon it in_progress.
func TestDispatcher_Run_CancelDuringPassDoesNotAbortIt(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
	require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))

	provider := &countingProvider{
		gate:    make(chan struct{}),
		arrived: make(chan struct{}),
		rate: quotes.ProviderQuote{
			Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
		},
	}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), time.Second)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	nudge := make(chan struct{})
	tick := make(chan time.Time)

	runReturned := make(chan struct{})
	go func() {
		dispatcher.Run(ctx, nudge, tick)
		close(runReturned)
	}()

	sendOrFail(t, nudge, struct{}{}, "Run never became ready to receive the nudge")

	select {
	case <-provider.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("GetCurrencyRate never arrived")
	}

	cancel()
	select {
	case <-runReturned:
		t.Fatal("Run returned while its own ClaimAndDispatch pass was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	assert.NoError(t, provider.lastCallCtx().Err(),
		"the in-flight upstream call's context must not be cancelled by Run's own ctx")

	close(provider.gate)
	select {
	case <-runReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the in-flight pass finished")
	}

	gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
	require.NoError(t, err)
	assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status,
		"a pass already in flight when ctx is cancelled must still complete, not be left in_progress")
}

// sendOrFail sends v on ch, failing the test instead of hanging forever if
// nothing ever receives it.
func sendOrFail[T any](t *testing.T, ch chan<- T, v T, msg string) {
	t.Helper()
	select {
	case ch <- v:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}
