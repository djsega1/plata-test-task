package quotes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

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

// bufferLogger is discardLogger, but capturing JSON log lines into buf for
// assertions — at level so a test can capture Debug-level lines too
// (slog's own default level is Info).
func bufferLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

// decodeLogLines parses buf's newline-delimited JSON log records into maps.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var lines []map[string]any
	for raw := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// findLogLine returns the first log line whose "msg" matches, failing the
// test if none does.
func findLogLine(t *testing.T, lines []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, line := range lines {
		if line["msg"] == msg {
			return line
		}
	}
	t.Fatalf("no log line with msg %q among %d lines: %v", msg, len(lines), lines)
	return nil
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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

	_, err := dispatcher.ClaimAndDispatch(t.Context())
	require.NoError(t, err)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

	require.NotPanics(t, func() {
		_, err := dispatcher.ClaimAndDispatch(t.Context())
		assert.NoError(t, err)
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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 6, time.Hour, time.Minute)

	done := make(chan error, 1)
	go func() {
		_, err := dispatcher.ClaimAndDispatch(context.Background())
		done <- err
	}()

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

// Simulates two replicas (two independent Dispatcher/Worker/RateLimiter
// stacks, each with its own process-local singleflight.Group) sharing one
// Repository: only ClaimBatch's atomic claim prevents double-processing,
// not single-flight, which can't coordinate across replicas.
func TestDispatcher_TwoReplicasSharingOneRepositoryDoNotDoubleProcess(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pair := testPair(t)

	const numRequests = 20
	ids := make([]uuid.UUID, numRequests)
	for i := range numRequests {
		req := domainquotes.NewCurrencyRateUpdateRequest(pair, fc.Now())
		require.NoError(t, repo.CreateUpdateRequest(t.Context(), req, ""))
		ids[i] = req.ID
	}

	// batchSize (8) deliberately smaller than numRequests (20): neither
	// replica can drain the queue alone in one pass, so both must actually
	// claim rows for the "no overlap" assertion below to mean anything.
	newReplica := func() *quotes.Dispatcher {
		provider := &countingProvider{rate: quotes.ProviderQuote{
			Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
		}}
		worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
		return quotes.NewDispatcher(repo, fc, worker, discardLogger(), 8, 4, time.Hour, time.Minute)
	}
	replicaA, replicaB := newReplica(), newReplica()

	// Keep both replicas claiming concurrently until neither sees a full
	// batch — mirrors Run's own "full batch means more work" loop, just
	// driven by hand from two independent Dispatchers instead of one.
	for {
		var fullA, fullB bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			var err error
			fullA, err = replicaA.ClaimAndDispatch(t.Context())
			assert.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			var err error
			fullB, err = replicaB.ClaimAndDispatch(t.Context())
			assert.NoError(t, err)
		}()
		wg.Wait()
		if !fullA && !fullB {
			break
		}
	}

	for _, id := range ids {
		gotReq, _, err := repo.GetUpdateByID(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status,
			"id %s must resolve exactly once even claimed by either of two independent replicas", id)
		assert.Equal(t, 1, gotReq.Attempts, "id %s must not be claimed twice", id)
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

	worker := quotes.NewWorker(repo, provider, fc, limiter, discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

	_, err := dispatcher.ClaimAndDispatch(t.Context())
	require.NoError(t, err)

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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

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

// panicOnClaimBatchRepository panics from ClaimBatch specifically — unlike
// a panic inside one claimed row's Worker.Process (already covered by
// TestDispatcher_PanicInOneUpdateDoesNotStopTheBatch), nothing recovers a
// panic here before this fix. Embeds a nil Repository: every method but
// ClaimBatch is unused by the one test that constructs this.
type panicOnClaimBatchRepository struct {
	quotes.Repository
}

func (panicOnClaimBatchRepository) ClaimBatch(context.Context, int, time.Time, time.Time) ([]domainquotes.CurrencyRateUpdateRequest, error) {
	panic("boom")
}

// TestDispatcher_Run_PanicInClaimBatchDoesNotCrashTheProcess guards Run
// against a panic that isn't inside any claimed row's Worker.Process —
// ClaimAndDispatch's errgroup only recovers those. An unrecovered panic in
// this goroutine would otherwise crash the entire test binary (in
// production, the entire server, HTTP included), so the real assertion
// here is that the process is still alive at all: a second nudge only
// succeeds once Run is back at its select after surviving the first.
func TestDispatcher_Run_PanicInClaimBatchDoesNotCrashTheProcess(t *testing.T) {
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	worker := quotes.NewWorker(
		memory.NewRepository(), &countingProvider{}, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5,
	)
	dispatcher := quotes.NewDispatcher(panicOnClaimBatchRepository{}, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

	nudge := make(chan struct{})
	tick := make(chan time.Time)
	go dispatcher.Run(t.Context(), nudge, tick)

	sendOrFail(t, nudge, struct{}{}, "Run never became ready to receive the nudge")
	sendOrFail(t, nudge, struct{}{},
		"Run never recovered from the panic and returned to its select — the panic likely crashed the goroutine")
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
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), 10, 4, time.Hour, time.Minute)

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

// TestDispatcher_Run_FullBatchKeepsDrainingWithoutWaitingForNudgeOrTick
// guards backlog drain when nothing keeps nudging: a full batch
// (== batchSize claimed) means more work is likely still queued, so Run
// must keep claiming immediately instead of waiting for the next
// nudge/tick. Five pending requests against a batchSize of 2 means a
// version that only claims once per nudge would leave three of them
// pending after a single nudge; the fix must drain all five from that one
// nudge alone.
func TestDispatcher_Run_FullBatchKeepsDrainingWithoutWaitingForNudgeOrTick(t *testing.T) {
	repo := memory.NewRepository()
	fc := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pairs := allPairs(t)

	const numRequests = 5
	reqs := make([]domainquotes.CurrencyRateUpdateRequest, numRequests)
	for i := range reqs {
		reqs[i] = domainquotes.NewCurrencyRateUpdateRequest(pairs[i%len(pairs)], fc.Now())
		require.NoError(t, repo.CreateUpdateRequest(t.Context(), reqs[i], ""))
	}

	provider := &countingProvider{rate: quotes.ProviderQuote{
		Value: decimal.RequireFromString("1.08"), Quality: "live", QuotedAt: fc.Now(),
	}}
	worker := quotes.NewWorker(repo, provider, fc, quotes.NewRateLimiter(0, 0), discardLogger(), time.Second, time.Minute, 5)
	const batchSize = 2
	dispatcher := quotes.NewDispatcher(repo, fc, worker, discardLogger(), batchSize, batchSize, time.Hour, time.Minute)

	nudge := make(chan struct{})
	tick := make(chan time.Time)

	go dispatcher.Run(t.Context(), nudge, tick)

	sendOrFail(t, nudge, struct{}{}, "Run never became ready to receive the nudge")
	// A second send only succeeds once Run is back at its outer select —
	// with the fix, that happens only after every full batch has been
	// drained (three passes: 2+2+1), not just the first one.
	sendOrFail(t, nudge, struct{}{}, "Run never finished draining every full batch from the single nudge above")

	for _, req := range reqs {
		gotReq, _, err := repo.GetUpdateByID(t.Context(), req.ID)
		require.NoError(t, err)
		assert.Equal(t, domainquotes.StatusSucceeded, gotReq.Status,
			"a single nudge must drain the whole backlog, not just one batchSize-sized slice of it")
	}
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
