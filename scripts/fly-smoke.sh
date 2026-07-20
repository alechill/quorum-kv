#!/usr/bin/env bash
# Post-deploy smoke check: write a key through one node's public endpoint and
# read it back through a DIFFERENT node's endpoint (fly-force-instance-id
# pins the request to a specific machine; non-leaders proxy internally to the
# leader). No nodes are killed here — this is deploy verification, not the
# fault harness.
set -euo pipefail

APP="${FLY_APP:-quorum-kv-frontier}"

mapfile -t ids < <(flyctl machines list -a "$APP" --json | jq -r '.[] | select(.state=="started") | .id')
if [ "${#ids[@]}" -lt 2 ]; then
  echo "smoke FAIL: need at least 2 started machines, got ${#ids[@]}" >&2
  exit 1
fi

key="smoke-$(date +%s)-$RANDOM"
val="v-$(date +%s)-$RANDOM"

echo "writing $key=$val via machine ${ids[0]}"
put_resp=$(curl -fsS --retry 5 --retry-all-errors --max-time 15 \
  -X PUT "https://$APP.fly.dev/kv/$key" \
  -H "fly-force-instance-id: ${ids[0]}" \
  -H 'Content-Type: application/json' \
  -d "{\"value\":\"$val\"}")
echo "put response: $put_resp"

echo "reading $key via machine ${ids[1]}"
got=$(curl -fsS --retry 5 --retry-all-errors --max-time 15 \
  "https://$APP.fly.dev/kv/$key" \
  -H "fly-force-instance-id: ${ids[1]}" | jq -r '.value')

if [ "$got" != "$val" ]; then
  echo "smoke FAIL: wrote $val via ${ids[0]}, read $got via ${ids[1]}" >&2
  exit 1
fi
echo "smoke OK: wrote via ${ids[0]}, read back via ${ids[1]}: $got"
