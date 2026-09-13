# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

An asynchronous currency-quotes service (Go, PostgreSQL) built for the Plata Go-engineer test
assignment. A client asks for a quote refresh and gets an update id back; the refresh happens in the
background; the client then polls for the result, or asks for the latest quote of a pair.

**`docs/design.md` is the source of truth for decisions** — it is short on purpose and grows as
features land. Read the relevant part before changing behaviour, and when a decision changes, update
the doc in the same change: a stale design doc is worse than none. §6 lists what is left to build,
with a done-criterion per step. `docs/design-notes.md` holds the long-form reasoning, measurements
and the history of reversals; it is background, not instructions.

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

# integration tests (need PostgreSQL; skipped when the variable is unset)
TEST_DATABASE_URL='postgres://postgres@localhost:5432/quotes' go test -tags=integration ./internal/storage/postgres/
```

A local cluster when no Docker is available (Debian/Ubuntu paths):

```bash
sudo -u postgres /usr/lib/postgresql/16/bin/initdb -D /tmp/pgb/data -U postgres --auth=trust
sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D /tmp/pgb/data \
  -o '-p 5433 -k /tmp/pgb/sock -c listen_addresses=' -l /tmp/pgb/pg.log start
```

## Architecture

Dependencies point inwards: `api/http` → `quotes` (domain and use cases) → ports implemented by
`storage/*` and `provider/*`. The domain knows nothing about HTTP, SQL, or any particular upstream.

## File layout

```
cmd/server/main.go                 — wiring: config, adapters, http.Server, graceful shutdown

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
      repository.go                — ports: Repository, Tx (declared here, not in domain)
      provider.go                  — port: RateProvider (GetCurrencyRate(ctx, pair) (quotes.CurrencyRate, error))
      clock.go                     — port: Clock (fake clocks in tests)
      request_update.go            — use case
      get_by_update_id.go          — use case
      get_latest.go                — use case
      dispatcher.go, worker.go     — nudge ∪ ticker ∪ reaper, worker pool, per-pair single-flight

  provider/
    exchangeratedev/adapter.go     — implements usecase/quotes.RateProvider against /v1/rate/{slug}
    fake/adapter.go                — implements usecase/quotes.RateProvider fully offline

  storage/
    postgres/                      — embed.FS migrations, pgx pool, implements Repository/Tx
    memory/                        — second implementation of the same ports, for use-case tests

  api/http/
    router.go, handlers.go, dto.go, middleware.go   — DTOs, kept separate from domain types

  config/config.go                 — env parsing

pkg/
  clock/                            — Clock implementations: SystemClock, FakeClock
```

**Ports are declared by the consumer.** `Repository`, `Tx`, `RateProvider` and `Clock` live in
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
`RateProvider.GetCurrencyRate(ctx, pair) (CurrencyRate, error)` against
`exchangerate.dev /v1/rate/{base}-{quote}`. The provider also offers `/v1/latest`, which returns
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
`in_progress`. This was observed, not theorised.

**Single-flight per pair is a correctness requirement, not an optimisation.** Eight workers can
claim eight tasks for the same pair, all see a stale quote and all call the upstream. Requests for
*different* pairs must still go out in parallel — that is what the pool is for. Every update keeps
its own status and its own `quote_id` regardless.

**Two clocks, never one.** `quoted_at` is the upstream's `data_updated_at` — when the price became
current; `fetched_at` is when it was read. The assignment's "время обновления" is the first one, and
"latest" is ordered by `quoted_at DESC, id DESC`: the upstream can return a price older than one
already stored, and ordering by `fetched_at` would then return the staler row.

**`stale_after` is a cache policy, not a provider guarantee.** The upstream reports `source`
(`live`, `ecb_daily`, `fred_daily`) but promises no lifetime. The service derives a freshness
window: ~60s for `live`, next publication for daily sources, a conservative `QUOTE_TTL` for anything
unknown. Do not key it off `market_session` — live currencies tick on weekends too.

**Money is `decimal`, never `float64`.** Rates are decoded from JSON as `json.Number` and parsed
into `shopspring/decimal`; the journal stores `NUMERIC(24,10)`; the API serialises prices as strings
so a JavaScript client cannot lose precision.

**Idempotency works on two levels.** An optional `Idempotency-Key` header, scoped to
`POST /quotes/updates`, protects against one client retrying (unique partial index; a replay returns
the original update). Separately, a worker reuses a quote that is still inside `stale_after` instead
of calling the upstream. Neither replaces the other.

## Conventions

- Comments and identifiers in English; the design doc and conversation in Russian.
- Upstream failures are normalised inside the adapter into
  `provider.Error{Code, Retryable, RetryAfter}`. The worker decides backoff vs permanent failure
  from `Retryable` alone, so never return a bare `fmt.Errorf` from an adapter.
- Tests use fake clocks and controllable fakes; no `time.Sleep` in tests.
- Integration tests live behind `//go:build integration` and skip without `TEST_DATABASE_URL`, so
  that `go test ./...` stays green on a machine with no database.
- The upstream API key comes from the environment only. `exchangerate.dev` serves anonymous
  requests and `PROVIDER=fake` runs the whole service offline — a reviewer must never need a key.
- Keep the dependency list short (currently pgx, decimal, uuid, goose, testify). A new dependency
  needs a reason that survives the question "what does this cost in production?".
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
