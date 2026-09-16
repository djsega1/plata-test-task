# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An asynchronous currency-quotes service (Go, PostgreSQL) built for the Plata Go-engineer test
assignment. A client asks for a quote refresh and gets an update id back; the refresh happens in the
background; the client then polls for the result, or asks for the latest quote of a pair.

**`docs/design.md` is the source of truth for decisions** — it is short on purpose and reflects only
the current state and why, not the history of how it got there. Read the relevant part before
changing behaviour, and when a decision changes, update the doc in the same change: a stale design
doc is worse than none. §6 lists what's left to build, if time allows.

## Commands

```bash
go build ./...                      # build
go vet ./...                        # vet
gofmt -l .                          # must print nothing
golangci-lint run ./...             # errcheck, staticcheck, unused, bodyclose, forbidigo (no time.Sleep), ...
go test ./...                       # unit tests — MUST pass without a database
go test ./internal/provider/...     # one package
go test ./internal/provider/ -run TestRateParsesResponse    # one test
go test ./internal/provider/ -run '^$' -bench . -benchtime 2s
go test ./... -cover

# task vet/lint add -tags=integration: statically covers integration-tagged
# files too (no database needed for that, only for actually running them)
task vet
task lint

# integration tests (need PostgreSQL 18+; skipped when the variable is unset)
TEST_DATABASE_URL='postgres://postgres@localhost:5432/quotes' go test -tags=integration ./internal/storage/postgres/
TEST_DATABASE_URL='postgres://postgres@localhost:5432/quotes' task test:integration
```

A local cluster when no Docker is available (Debian/Ubuntu paths, PostgreSQL 18+ — see "IDs are
UUIDv7" below):

```bash
sudo -u postgres /usr/lib/postgresql/18/bin/initdb -D /tmp/pgb/data -U postgres --auth=trust
sudo -u postgres /usr/lib/postgresql/18/bin/pg_ctl -D /tmp/pgb/data \
  -o '-p 5433 -k /tmp/pgb/sock -c listen_addresses=' -l /tmp/pgb/pg.log start
```

## Architecture

Dependencies point inwards: `api/http` → `quotes` (domain and use cases) → ports implemented by
`storage/*` and `provider/*`. The domain knows nothing about HTTP, SQL, or any particular upstream.

## File layout

```
cmd/server/main.go                 — wiring: config, adapters, http.Server, dispatcher goroutine,
                                      graceful shutdown
cmd/server/provider.go             — factory: cfg.Provider -> RateProvider adapter
cmd/throughput/main.go             — standalone POST load generator for scripts/throughput.sh
                                      (docs/design.md §8); stdlib only, not part of the running
                                      service

internal/
  domain/
    quotes/                        — pure domain: no ports, no I/O; everything about a quote lives here
      currency_code.go             — CurrencyCode, allow-list
      currency_pair.go             — CurrencyPair
      currency_rate.go             — CurrencyRate value object
      update_request.go            — UpdateRequest, its status type and valid transitions
      errors.go                    — domain errors, implement error

  usecase/
    quotes/                        — calls the domain and the ports below; the ports' consumer
      repository.go                — port: Repository (declared here, not in domain)
      provider.go                  — port: RateProvider (GetCurrencyRate, plus Provider/Indicative/StaleAfter —
                                      identity and freshness cadence are upstream-specific, so each adapter owns them)
      clock.go                     — port: Clock (fake clocks in tests)
      request_update.go            — use case
      get_by_update_id.go          — use case
      get_latest.go                — use case
      dispatcher.go, worker.go     — nudge ∪ ticker ∪ reaper, worker pool, per-pair single-flight

  provider/
    exchangeratedev/adapter.go     — implements usecase/quotes.RateProvider against /v1/rate/{slug}
    fake/adapter.go                — implements usecase/quotes.RateProvider fully offline

  storage/
    postgres/                      — embed.FS migrations, pgx pool, implements Repository
    memory/                        — second implementation of the same ports, for use-case tests

  api/http/
    router.go, system_handlers.go, quotes_handlers.go, dto.go, middleware.go   — DTOs, kept
                                      separate from domain types; system_handlers.go is
                                      /healthz+/readyz, quotes_handlers.go the three business
                                      endpoints

  config/config.go                 — env parsing

pkg/
  clock/                            — Clock implementations: SystemClock, FakeClock
  httpclient/                       — builds an *http.Client with an explicit, tuned Transport;
                                      cmd/server is the only caller that decides the numbers
```

**Ports are declared by the consumer.** `Repository`, `RateProvider` and `Clock` live in
`internal/usecase/quotes` — that package calls them, so it declares them, not `internal/domain/quotes`.
Domain stays free of ports entirely: it holds value objects and invariants, nothing that talks to a
database or an upstream. Adapters (`storage/postgres`, `storage/memory`, `provider/exchangeratedev`,
`provider/fake`) implement those interfaces structurally from the outside — they import
`internal/domain/quotes` for the shared value types their methods take and return, and optionally
`internal/usecase/quotes` for a compile-time assertion (`var _ quotes.RateProvider = (*Adapter)(nil)`),
but nothing in `domain` or `usecase` ever imports an adapter package. There is no `mocks/` package —
test doubles are hand-written next to the test that uses them. Keep it that way: an interface
declared at its implementation grows to serve every caller, and then every test has to satisfy
methods it does not care about.

**Two packages are both named `quotes`** (`domain/quotes`, `usecase/quotes`) — that is the cost of
splitting domain from use cases explicitly. Anywhere both are imported together, alias one
(`domainquotes ".../domain/quotes"` is the usual choice, since the use-case package is normally the
one already qualified by its role).

**One pair per upstream call.** The provider port is
`RateProvider.GetCurrencyRate(ctx, pair) (ProviderQuote, error)` against
`exchangerate.dev /v1/rate/{base}-{quote}`. `ProviderQuote` is deliberately not the domain
`CurrencyRate` — it carries only what an adapter itself knows (`Value`, `Derived`, `Quality`,
`QuotedAt`), skipping `NewCurrencyRate`'s invariant check on purpose, since a raw upstream value
isn't safe to persist yet; the worker builds the real `CurrencyRate` once it has this plus
`Provider`/`Indicative`/`StaleAfter`. The provider also offers `/v1/latest`, which returns
every currency in one call — deliberately not used: it would collapse six pairs into one request and
leave the background machinery (worker pool, per-pair single-flight, rate limiting) doing nothing,
and background processing is the point of this assignment. See §2 of the design doc.

**The queue is a table, not a channel.** `quote_updates` is both the record of an update request and
the job queue. Workers claim batches with `FOR UPDATE SKIP LOCKED`, so several replicas are safe.
Three independent things wake a worker: a non-blocking nudge from the HTTP handler (latency), a
ticker (the only mechanism that picks up backoff-deferred work and work created by another replica),
and a reaper that returns rows stuck in `in_progress` past the visibility timeout. Delivery is
at-least-once by design — reading a rate has no side effects.

**Claim, work and complete are three separate transactions.** They cannot be merged into one
statement: a CTE's updates are invisible to the rest of the same statement, so an outer `UPDATE`
that tries to close rows the CTE just claimed silently does nothing and leaves everything in
`in_progress`. This was observed, not theorised. Each transaction is opened and committed inside a
single `Repository` method (`ClaimBatch`, `CompleteSuccess`, `CompleteFailure`) — no `Tx` handle
crosses the port boundary. The reaper is not its own method: `ClaimBatch`'s `WHERE` already covers
rows stuck in `in_progress` past a visibility cutoff, alongside pending ones, so a reaper pass is
just another call to the same claim. "Work" (the provider call) happens between two of these calls,
holding no transaction open: a DB transaction should not sit open for the duration of an upstream
HTTP call.

**Single-flight per pair is a correctness requirement, not an optimisation.** Eight workers can
claim eight tasks for the same pair, all see a stale quote and all call the upstream. Requests for
*different* pairs must still go out in parallel — that is what the pool is for. Every update keeps
its own status and its own `quote_id` regardless.

The single-flight key wraps the whole reuse-or-fetch decision, not just the upstream call: a
`GetLatestQuote` read followed by a fetch, both inside one `singleflight.Do`. Wrapping only the
fetch leaves a window — a caller that reads a stale quote just before another caller's fetch lands
would join no in-flight call and fetch again on its own, right after a fetch that already covered
it. Re-reading freshness inside the same critical section closes that window: whichever caller
becomes the new leader sees the other one's write first.

**Two clocks, never one.** `quoted_at` is the upstream's `data_updated_at` — when the price became
current; `fetched_at` is when it was read. The assignment's "время обновления" is the first one, and
"latest" is ordered by `quoted_at DESC, id DESC`: the upstream can return a price older than one
already stored, and ordering by `fetched_at` would then return the staler row.

**`stale_after` is a cache policy, not a provider guarantee.** The upstream reports `source`
(`live`, `ecb_daily`, `fred_daily`) but promises no lifetime, and a different upstream could use an
entirely different quality vocabulary — so each adapter's `StaleAfter(quality, quotedAt)` derives its
own freshness window, not a shared function keyed on a fixed set of strings. `exchangerate.dev`'s:
~60s for `live`, next publication (~24h) for daily sources, a conservative `QUOTE_TTL` from config for
anything else.

**Money is `decimal`, never `float64`.** Rates are decoded from JSON as `json.Number` and parsed
into `shopspring/decimal`; the journal stores `NUMERIC(24,10)`; the API serialises prices as strings
so a JavaScript client cannot lose precision.

**Idempotency works on two levels.** An optional `Idempotency-Key` header, scoped to
`POST /quotes/updates`, protects against one client retrying (unique partial index; a replay returns
the original update). Separately, a worker reuses a quote that is still inside `stale_after` instead
of calling the upstream. Neither replaces the other.

## Conventions

- Comments and identifiers in English; the design doc and conversation in Russian.
- Upstream failures are normalised inside the adapter into `domainquotes.CurrencyRateError`
  (`Code()`/`Retryable()`/`RetryAfter()`, `internal/domain/quotes/errors.go`). The worker decides
  backoff vs permanent failure from `Retryable()` alone, so never return a bare `fmt.Errorf` from
  an adapter.
- Tests use fake clocks and controllable fakes; no `time.Sleep` in tests.
- Integration tests live behind `//go:build integration` and skip without `TEST_DATABASE_URL`, so
  that `go test ./...` stays green on a machine with no database.
- The upstream API key comes from the environment only. `exchangerate.dev` serves anonymous
  requests and `PROVIDER=fake` runs the whole service offline — a reviewer must never need a key.
- Keep the dependency list short (currently pgx, decimal, goose, testify — `uuid` is part of this
  project's Go standard library, not a fetched dependency). A new dependency needs a reason that
  survives the question "what does this cost in production?".
- **IDs are UUIDv7, never v4.** Generate with `uuid.NewV7()` — `uuid.New()`/`uuid.NewV4()` are
  disallowed everywhere in this codebase. v7 embeds a timestamp, so `quote_updates` primary-key
  inserts stay ordered instead of scattering across the B-tree the way random v4 would. Requires
  PostgreSQL 18+.
- **No business logic in the database.** Migrations declare columns, types, `PRIMARY KEY`,
  `FOREIGN KEY`, `UNIQUE` and `NOT NULL` only — those are structural/concurrency guarantees the
  application cannot otherwise get atomically (a unique partial index is what makes a concurrent
  idempotent insert safe; the app layer alone would have a check-then-insert race). Never a `CHECK`
  that encodes domain vocabulary or a state machine — valid `quality`/`status` values and the
  `rate > 0` invariant are validated by Go types and constructors in `internal/domain/quotes`
  before a row is ever written, not duplicated as SQL.

## Working agreement

The repository owner writes the commits. Leave finished work in the working tree, say what changed
and why it is ready, and do not run `git add`, `git commit` or `git push`.
