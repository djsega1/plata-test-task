# plata-test-task

## Possible improvements

**Distributed rate limiting / single-flight (Redis).** `RateLimiter` and `Worker`'s per-pair
single-flight (`internal/usecase/quotes/ratelimiter.go`, `worker.go`) are both process-local: with
more than one `cmd/server` replica running, each replica enforces its own 12/min + 100/hour budget
and its own per-pair dedup, independently of the others. A shared Redis-backed limiter (or a
Postgres advisory lock per pair, staying within the existing dependency) would let replicas
coordinate, so N replicas together respect one budget and don't redundantly fetch the same pair at
the same time.

This is a correctness-neutral optimization, not a fix. Two replicas both fetching `EUR/USD` at once
each still write a valid `quotes` row — deduplicated by `(provider, base, quote, quoted_at)` if
`quoted_at` happens to match, or two adjacent rows if it doesn't — nothing breaks, no data is lost or
corrupted. The only cost under multiple replicas is wasted upstream calls and rate-limit budget, not
incorrect results. Not implemented: it would add a new runtime dependency (Redis) for a service that
is single-instance by default (`docs/design.md` A6), and isn't required by the assignment's scope.