#!/usr/bin/env bash
# Quick start for airouter. Run this once on a fresh checkout.
#
# What it does:
#   1. Builds the CLI
#   2. Bootstraps the DB from the curated free-pool catalog (skips providers
#      whose env var you have not set)
#   3. Adds a tenant and prints its API key
#   4. Sends a tiny prompt
#   5. Prints the live provider status and today's usage
#
# Before running, set at least one provider env var, e.g.:
#   export GROQ_API_KEY=gsk_...

set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> building airouter"
make build

echo
echo "==> bootstrapping from the free-pool catalog"
./bin/airouter bootstrap

echo
echo "==> adding a tenant (the API key is shown once — copy it)"
TENANT_KEY=$(./bin/airouter tenant add my-app -limit 100 | awk '/^API key/ {print $NF}')
echo "tenant key: $TENANT_KEY"
echo "(save this — it is not shown again)"

echo
echo "==> asking a question (default output: text + footer on stderr)"
./bin/airouter ask "Write one short sentence about rain."

echo
echo "==> same question, raw text only (useful for piping into other tools)"
./bin/airouter ask -text "Write one short sentence about rain." | tr '[:lower:]' '[:upper:]'

echo
echo "==> asking for JSON output (validated; fails over if a model returns junk)"
./bin/airouter ask -json -system 'Reply as {"emotion": "..."} only.' "I just lost my keys." | head -5

echo
echo "==> live provider status"
./bin/airouter provider list

echo
echo "==> bootstrapping TTS + STT providers (in addition to text)"
./bin/airouter bootstrap -category tts,stt

echo
echo "==> text-to-speech: write 'Hello world' to /tmp/airouter-hello.wav"
./bin/airouter speak -o /tmp/airouter-hello.wav "Hello world from airouter" || echo "  (needs GROQ_API_KEY or GEMINI_API_KEY)"
[ -f /tmp/airouter-hello.wav ] && echo "  wrote $(stat -c %s /tmp/airouter-hello.wav 2>/dev/null || stat -f %z /tmp/airouter-hello.wav) bytes — listen with: play /tmp/airouter-hello.wav"

echo
echo "==> speech-to-text: transcribe the WAV we just made"
./bin/airouter transcribe /tmp/airouter-hello.wav 2>/dev/null || echo "  (needs GROQ_API_KEY for Whisper)"

echo
echo "==> today's usage (default tenant)"
./bin/airouter usage

echo
echo "==> done. Next steps:"
echo "  - Start the HTTP server:  AIROUTER_ADMIN_TOKEN=secret ./bin/airouter serve -addr :8080"
echo "  - Call it via curl:       curl -s localhost:8080/v1/generate -H 'Authorization: Bearer $TENANT_KEY' -d '{\"prompt\":\"hi\"}'"
echo "  - TTS over HTTP:          curl -s localhost:8080/v1/speak -H 'Authorization: Bearer $TENANT_KEY' -d '{\"text\":\"hi\"}' --output out.wav"
echo "  - STT over HTTP:          curl -s localhost:8080/v1/transcribe -H 'Authorization: Bearer $TENANT_KEY' -F file=@audio.mp3"
echo "  - Embed in a Go app:      see examples/embedded.go"
