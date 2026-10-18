#!/usr/bin/env bash
# Proves one global limit holds across two instances.
#
# Capacity 20, no meaningful refill. Fire 40 requests through a round-robin
# load balancer. If each instance kept its own state we would see ~40 allowed
# (20 each). Shared atomic state means exactly 20.
set -euo pipefail

BASE=${BASE:-http://localhost:8080}

curl -sf -X PUT "$BASE/v1/rules" -H 'Content-Type: application/json' -d '{
  "identifier": "acme",
  "namespace": "search",
  "algorithm": "token_bucket",
  "capacity": 20,
  "refill_rate": 0.001
}' > /dev/null
echo "rule created: capacity 20"

echo "instances in rotation:"
for i in $(seq 1 4); do curl -s "$BASE/whoami"; done | sort -u

allowed=0; denied=0
for i in $(seq 1 40); do
  r=$(curl -s -X POST "$BASE/v1/check" -H 'Content-Type: application/json' \
      -d '{"identifier":"acme","namespace":"search"}' | grep -o '"allowed":[a-z]*')
  if [ "$r" = '"allowed":true' ]; then allowed=$((allowed+1)); else denied=$((denied+1)); fi
done

echo "allowed=$allowed denied=$denied"
if [ "$allowed" -eq 20 ]; then
  echo "PASS: exactly 20 allowed across both instances"
else
  echo "FAIL: expected 20, got $allowed"; exit 1
fi