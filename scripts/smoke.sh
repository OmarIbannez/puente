#!/usr/bin/env bash
# End-to-end smoke test against a running Puente server.
#
#   scripts/smoke.sh [BASE_URL]      (default http://localhost:8000)
#
# Flow: health -> sandbox (seeded) -> trial balance zero -> simulate a
# deposit -> advance until credited -> create a payout with an
# Idempotency-Key -> replay it (Idempotent-Replay: true) -> advance until
# liquidated with a CEP -> reconciliation finds 0 breaks -> inject a bank fee
# -> reconciliation finds the balance break. Needs curl and jq; exits
# non-zero on the first failure.
set -euo pipefail

BASE_URL="${1:-${BASE_URL:-http://localhost:8000}}"
BASE_URL="${BASE_URL%/}"

for bin in curl jq; do
  command -v "$bin" >/dev/null || { echo "smoke: $bin is required" >&2; exit 2; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
jar="$work/cookies"
body="$work/body"
headers="$work/headers"

fail() {
  echo "FAIL: $*" >&2
  [[ -s "$body" ]] && { echo "--- last response body:" >&2; cat "$body" >&2; echo >&2; }
  exit 1
}
ok() { echo "ok   $*"; }

# req METHOD PATH [JSON] [HEADER...] -> prints the status; body in $body.
req() {
  local method="$1" path="$2" data="${3:-}"
  shift 3 || shift $#
  local args=(-sS -o "$body" -D "$headers" -w '%{http_code}' -b "$jar" -c "$jar" -X "$method"
    -H 'Accept: application/json')
  [[ -n "$data" ]] && args+=(-H 'Content-Type: application/json' --data "$data")
  local h
  for h in "$@"; do args+=(-H "$h"); done
  curl "${args[@]}" "$BASE_URL$path"
}
expect() { [[ "$2" == "$1" ]] || fail "$3: HTTP $2, want $1"; }

echo "smoke: $BASE_URL"

# Wait for the server (cold starts on Render take a while).
for _ in $(seq 1 60); do
  [[ "$(req GET /api/health 2>/dev/null || true)" == 200 ]] && break
  sleep 1
done
expect 200 "$(req GET /api/health)" "health"
ok "health"

# The cookie jar keeps one sandbox for the whole run.
expect 200 "$(req GET /api/sandbox)" "sandbox"
sandbox="$(jq -r .id "$body")"
ok "sandbox $sandbox"

expect 200 "$(req GET /api/overview)" "overview"
[[ "$(jq '[.trial_balance[]] | all(. == 0)' "$body")" == true ]] || fail "seeded trial balance is not zero"
ok "seeded books balance"

expect 200 "$(req GET /api/customers)" "customers"
customer="$(jq -r '.customers[0].id' "$body")"
before="$(jq -r '.customers[0].available.USDC.minor' "$body")"

deposit="$(jq -nc --arg c "$customer" '{customer_id: $c, asset: "USDC", amount: "25"}')"
expect 201 "$(req POST /api/deposits/simulate "$deposit" "Idempotency-Key: smoke-dep-$sandbox")" "simulate deposit"
expect 200 "$(req POST /api/sandbox/advance '{"ticks":5}')" "advance"
expect 200 "$(req GET /api/customers)" "customers"
after="$(jq -r --arg id "$customer" '.customers[] | select(.id == $id) | .available.USDC.minor' "$body")"
(( after == before + 25000000 )) || fail "deposit not credited: $before -> $after"
ok "deposit credited"

payout="$(jq -nc --arg c "$customer" '{customer_id: $c, asset: "USDC", amount: "10",
  beneficiary_clabe: "012180015203300154", beneficiary_name: "Smoke Test", concepto: "Smoke"}')"
expect 201 "$(req POST /api/payouts "$payout" "Idempotency-Key: smoke-pay-$sandbox")" "create payout"
id="$(jq -r .id "$body")"
cp "$body" "$work/first"
expect 201 "$(req POST /api/payouts "$payout" "Idempotency-Key: smoke-pay-$sandbox")" "replay payout"
grep -qi '^idempotent-replay: true' "$headers" || fail "replay missing Idempotent-Replay header"
cmp -s "$body" "$work/first" || fail "replay body differs"
ok "idempotent replay"

expect 200 "$(req POST /api/sandbox/advance '{"ticks":2}')" "advance"
expect 200 "$(req GET "/api/payouts/$id")" "payout detail"
[[ "$(jq -r .status "$body")" == liquidated ]] || fail "payout not liquidated"
[[ -n "$(jq -r '.rail.cep.sello // empty' "$body")" ]] || fail "no CEP"
ok "payout liquidated with CEP"

expect 201 "$(req POST /api/recon/runs)" "recon"
[[ "$(jq .break_count "$body")" == 0 ]] || fail "clean recon found breaks"
expect 201 "$(req POST /api/sandbox/inject '{"kind":"bank_fee","amount":"12.50"}')" "inject"
expect 201 "$(req POST /api/recon/runs)" "recon"
[[ "$(jq -r '.breaks[0].kind' "$body")" == balance_mismatch ]] || fail "bank fee not detected"
ok "reconciliation: clean, then the injected bank fee"

echo "smoke: all checks passed"
