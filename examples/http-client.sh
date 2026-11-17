#!/usr/bin/env bash
# Talk to a running airouter HTTP server with curl.
#
# Assumes:
#   - airouter is running on localhost:8080 (start it with `airouter serve`)
#   - you have a tenant API key (from `airouter tenant add NAME`)

set -euo pipefail

BASE="${AIROUTER_URL:-http://localhost:8080}"
KEY="${AIROUTER_KEY:?set AIROUTER_KEY to your tenant api key}"

echo "==> plain text generation (JSON in, plain text out)"
curl -s "$BASE/v1/generate?format=text" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Write one short sentence about the ocean."}'
echo

echo "==> JSON generation (the router asks for JSON and validates it)"
curl -s "$BASE/v1/generate" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"I failed my exam today.","system":"Reply as {\"emotion\": \"...\", \"intensity\": 1-10} only.","json":true}'
echo

echo "==> live usage + provider status"
curl -s "$BASE/v1/usage" \
  -H "Authorization: Bearer $KEY" | head -40
echo

echo "==> pin one provider (skip the router's selection)"
curl -s "$BASE/v1/generate" \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"hi","provider":"groq-llama-3.1-8b-instant"}'
echo
