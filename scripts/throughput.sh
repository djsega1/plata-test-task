#!/usr/bin/env bash
# Measures the background pipeline's own throughput: how many quote_updates
# rows the dispatcher actually carries from pending to succeeded per 10
# seconds, under a POST-only burst against an already-running stack — the
# number docs/design.md's architecture (queue in Postgres, worker pool,
# per-pair single-flight) is actually built to produce, not raw HTTP
# request throughput. See docs/design.md §8 for the write-up and README's
# "Throughput" section for the headline numbers.
#
# Prerequisite: `docker compose up -d --build` already running.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
CONCURRENCY="${CONCURRENCY:-50}"
LOAD_DURATION="${LOAD_DURATION:-20s}"
DB_CONTAINER="${DB_CONTAINER:-plata-test-task-db-1}"
DB_USER="${DB_USER:-quotes}"
DB_NAME="${DB_NAME:-quotes}"
DRAIN_TIMEOUT="${DRAIN_TIMEOUT:-30}"

psql_scalar() {
  docker exec "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -tAc "$1"
}

psql_table() {
  docker exec "$DB_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -c "$1"
}

START_EPOCH=$(date -u +%s)

echo "==> generating POST-only load for $LOAD_DURATION at concurrency=$CONCURRENCY"
go run ./cmd/throughput -url "$BASE_URL" -concurrency "$CONCURRENCY" -duration "$LOAD_DURATION"

echo "==> waiting for the queue to drain (pending/in_progress -> 0, up to ${DRAIN_TIMEOUT}s)"
REMAINING=-1
for _ in $(seq 1 "$DRAIN_TIMEOUT"); do
  REMAINING=$(psql_scalar "SELECT count(*) FROM quote_updates WHERE created_at >= to_timestamp($START_EPOCH) AND status IN ('pending','in_progress')")
  [ "$REMAINING" -eq 0 ] && break
  sleep 1
done
echo "    remaining in flight: $REMAINING"

TOTAL=$(psql_scalar "SELECT count(*) FROM quote_updates WHERE created_at >= to_timestamp($START_EPOCH)")
SUCCEEDED=$(psql_scalar "SELECT count(*) FROM quote_updates WHERE created_at >= to_timestamp($START_EPOCH) AND status = 'succeeded'")
FAILED=$(psql_scalar "SELECT count(*) FROM quote_updates WHERE created_at >= to_timestamp($START_EPOCH) AND status = 'failed'")

echo "==> succeeded rows per second"
psql_table "
SELECT to_char(date_trunc('second', updated_at), 'HH24:MI:SS') AS second, count(*) AS succeeded
FROM quote_updates
WHERE created_at >= to_timestamp($START_EPOCH) AND status = 'succeeded'
GROUP BY 1 ORDER BY 1;
"

BEST_10S=$(psql_scalar "
WITH per_second AS (
  SELECT date_trunc('second', updated_at) AS second, count(*) AS c
  FROM quote_updates
  WHERE created_at >= to_timestamp($START_EPOCH) AND status = 'succeeded'
  GROUP BY 1
)
SELECT coalesce(max(w), 0) FROM (
  SELECT sum(c) OVER (ORDER BY second ROWS BETWEEN CURRENT ROW AND 9 FOLLOWING) AS w
  FROM per_second
) t;
")

DISTINCT_PAIRS_FETCHED=$(psql_scalar "
SELECT count(DISTINCT (base, quote))
FROM quotes
WHERE fetched_at >= to_timestamp($START_EPOCH);
")

echo
echo "created=$TOTAL succeeded=$SUCCEEDED failed=$FAILED"
echo "best 10-second window: $BEST_10S quotes/10s (rows reaching succeeded, i.e. queue-drain rate)"
echo "distinct (base,quote) actually fetched from the provider during the run: $DISTINCT_PAIRS_FETCHED"
echo "  (the rest reused an existing quote inside its stale_after window — see docs/design.md §8)"
