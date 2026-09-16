#!/usr/bin/env bash
# Smoke test for `docker compose up` — see README's Quickstart.
# No jq dependency on purpose — a reviewer running this right after
# `docker compose up` shouldn't need to install anything beyond curl.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API="$BASE_URL/api/v1"
PAIR="${PAIR:-EUR/MXN}"
MAX_POLLS="${MAX_POLLS:-30}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

extract() {
  # extract <json> <field> — pulls a top-level "field":"value" or
  # "field":value (unquoted, e.g. a number/bool) pair. Good enough for this
  # script's flat response shapes; not a general JSON parser.
  echo "$1" | grep -oE "\"$2\":\"[^\"]*\"|\"$2\":[0-9a-zA-Z_.]*" | head -n1 | sed -E "s/\"$2\":\"?([^\"]*)\"?/\1/"
}

echo "==> GET /healthz"
curl -fsS "$BASE_URL/healthz" >/dev/null || fail "/healthz did not return 200"

echo "==> GET /readyz"
curl -fsS "$BASE_URL/readyz" >/dev/null || fail "/readyz did not return 200 (database not reachable?)"

echo "==> POST /quotes/updates (pair=$PAIR)"
CREATE_RESPONSE=$(curl -fsS -X POST "$API/quotes/updates" \
  -H 'Content-Type: application/json' \
  -d "{\"pair\":\"$PAIR\"}") || fail "POST /quotes/updates failed"
UPDATE_ID=$(extract "$CREATE_RESPONSE" update_id)
[ -n "$UPDATE_ID" ] || fail "no update_id in response: $CREATE_RESPONSE"
echo "    update_id=$UPDATE_ID"

echo "==> polling GET /quotes/updates/$UPDATE_ID"
STATUS=""
for i in $(seq 1 "$MAX_POLLS"); do
  POLL_RESPONSE=$(curl -fsS "$API/quotes/updates/$UPDATE_ID") || fail "GET /quotes/updates/$UPDATE_ID failed"
  STATUS=$(extract "$POLL_RESPONSE" status)
  echo "    [$i] status=$STATUS"
  case "$STATUS" in
    succeeded|failed) break ;;
  esac
  sleep 1
done

case "$STATUS" in
  succeeded)
    echo "    price=$(extract "$POLL_RESPONSE" price)"
    ;;
  failed)
    fail "update ended failed: $POLL_RESPONSE"
    ;;
  *)
    fail "update still $STATUS after $MAX_POLLS polls: $POLL_RESPONSE"
    ;;
esac

echo "==> GET /quotes/latest?pair=$PAIR"
LATEST_RESPONSE=$(curl -fsS -G "$API/quotes/latest" --data-urlencode "pair=$PAIR") || fail "GET /quotes/latest failed"
echo "    $LATEST_RESPONSE"

echo "PASS"
