#!/usr/bin/env python3
"""
Chat example — full walkthrough using the OpenAI Python SDK against airouter.

airouter speaks the OpenAI Chat Completions wire format, so the standard
`openai` package works out of the box. Point `base_url` at the airouter `/v1`
prefix and use your `air_...` tenant API key as the API key. airouter then
routes your prompt across every provider you've added (Groq, Gemini,
OpenRouter, ZAI, Ollama, …), automatically failing over when one hits a limit.

This file walks through:
  1. Setup (env vars, client construction)
  2. "auto" routing — let airouter pick the best free provider
  3. Pinning one specific model
  4. JSON mode (response_format json_object)
  5. Multi-turn conversations (message history)
  6. Reading the airouter-specific response envelope (provider, attempts, …)
  7. Error handling (rate limits, all-providers-busy)

RUN:
  pip install openai
  export AIROUTER_URL=http://localhost:8080
  export AIROUTER_KEY=air_...           # from: airouter tenant add my-app
  python chat.py
"""

from __future__ import annotations

import json
import os
import sys
import time

# pip install openai
from openai import (
    OpenAI,
    APIError,
    APIStatusError,
    RateLimitError,
)

# ── 1. Setup ────────────────────────────────────────────────────────────────
#
# AIROUTER_URL  — where your airouter server is running. The standalone
#                 `airouter serve` defaults to :8080. If you've mounted
#                 airouter inside another server (like the emotion project),
#                 point this at the prefix you mounted it under, e.g.
#                 http://localhost:8000/airouter  (note: no /v1 here — the
#                 SDK appends /chat/completions to base_url itself).
#
# AIROUTER_KEY  — your tenant API key, starts with `air_`. Get one with
#                 `airouter tenant add my-app` — the key is printed once.

AIROUTER_URL = os.environ.get("AIROUTER_URL", "http://localhost:8080")
AIROUTER_KEY = os.environ.get("AIROUTER_KEY", "")

if not AIROUTER_KEY:
    sys.exit(
        "error: set AIROUTER_KEY to your tenant api key.\n"
        "  Get one with:  airouter tenant add my-app\n"
        "  The key is printed once and starts with 'air_'."
    )

# base_url MUST point at /v1 — the SDK appends /chat/completions itself.
# So if AIROUTER_URL ends with /airouter (mounted inside another server),
# we end up with:  http://localhost:8000/airouter/v1/chat/completions
client = OpenAI(base_url=f"{AIROUTER_URL}/v1", api_key=AIROUTER_KEY)


def banner(s: str) -> None:
    print(f"\n── {s} ──────────────────────────────────────────────")


# ── 2. "auto" routing ──────────────────────────────────────────────────────
#
# model="auto" tells airouter: pick the best free provider, fail over on error.
# Internally airouter tries providers in priority order (Groq first by default,
# then Gemini, then OpenRouter, then ZAI, then Ollama), respecting per-provider
# daily limits, rate limits, and cooldowns. The first one that answers wins.
banner("auto routing")
resp = client.chat.completions.create(
    model="auto",
    messages=[{"role": "user", "content": "Say hello in one short sentence."}],
)
print("reply:  ", resp.choices[0].message.content)
# The airouter envelope (provider used, latency, attempts) is on the raw
# response. With the OpenAI Python SDK, look at resp.model_extra or the
# response headers — airouter adds an `airouter` field when it can.
print("model:  ", resp.model)
if getattr(resp, "airouter", None):
    print("airouter:", resp.airouter)


# ── 3. Pin one specific model ──────────────────────────────────────────────
#
# When you want determinism (or want to compare models), pin one by name.
# Use the exact model id airouter knows — see `airouter provider list` or
# `airouter free-pool -category text` for the full list.
banner("pinned model")
pinned = client.chat.completions.create(
    model="openai/gpt-oss-120b",  # Groq's flagship free model
    messages=[{"role": "user", "content": "What is 2+2? Answer with just the number."}],
)
print("reply:  ", pinned.choices[0].message.content)
print("model:  ", pinned.model)


# ── 4. JSON mode ───────────────────────────────────────────────────────────
#
# response_format={type: "json_object"} asks the model for JSON-only output.
# ALWAYS pair it with a system prompt that describes the shape — models that
# support JSON mode will produce strict JSON, but they need to know what shape
# to emit. airouter validates the JSON on its end and returns it in resp.JSON.
banner("JSON mode")
js = client.chat.completions.create(
    model="auto",
    response_format={"type": "json_object"},
    messages=[
        {
            "role": "system",
            "content": 'Respond ONLY as {"emotion":"...","intensity":1-10}.',
        },
        {"role": "user", "content": "I just lost my keys and I am running late."},
    ],
)
# Parse the model's JSON output
parsed = json.loads(js.choices[0].message.content)
print("parsed JSON:", parsed)


# ── 5. Multi-turn conversation ─────────────────────────────────────────────
#
# Send the full message history on every call. airouter is stateless — it
# doesn't remember previous turns. Maintain the history on your side.
banner("multi-turn")
history = [
    {"role": "system", "content": "You are a helpful assistant. Be terse."},
    {"role": "user", "content": "My name is Ali."},
]
r1 = client.chat.completions.create(model="auto", messages=history)
print("turn 1:", r1.choices[0].message.content)

history.append({"role": "assistant", "content": r1.choices[0].message.content})
history.append({"role": "user", "content": "What is my name?"})
r2 = client.chat.completions.create(model="auto", messages=history)
print("turn 2:", r2.choices[0].message.content)


# ── 6. Read the airouter envelope ──────────────────────────────────────────
#
# airouter attaches metadata to the response. The OpenAI SDK surfaces
# non-standard fields via resp.model_extra (or as attributes). Look for:
#   - provider   — which provider answered (e.g. "groq-openai/gpt-oss-120b")
#   - attempts   — list of providers tried before the successful one
#   - latency_ms — total time including failover
banner("airouter metadata")
r3 = client.chat.completions.create(
    model="auto",
    messages=[{"role": "user", "content": "Pick a number between 1 and 10."}],
)
print("reply:    ", r3.choices[0].message.content)
print("model:    ", r3.model)
# The provider name is encoded in the model field when airouter picks one.
# Split it to see the preset and the model id separately.
if "/" in r3.model:
    print("preset/model:", r3.model)


# ── 7. Error handling ──────────────────────────────────────────────────────
#
# airouter surfaces three error kinds through the OpenAI wire format:
#   - RateLimitError  → all providers are busy right now; retry later.
#                       resp.headers.get('Retry-After') tells you when.
#   - APIStatusError  → 4xx/5xx from the upstream provider that ultimately
#                       answered (rare — airouter usually fails over).
#   - APIError        → request itself was bad (empty prompt, bad JSON, …).
banner("error handling")
try:
    bad = client.chat.completions.create(
        model="nonexistent/model",
        messages=[{"role": "user", "content": "this will fail"}],
    )
except RateLimitError as e:
    retry = e.response.headers.get("Retry-After") if e.response else "?"
    print(f"all providers busy — retry after {retry}s")
except APIStatusError as e:
    print(f"HTTP {e.status_code}: {e.message}")
except APIError as e:
    print(f"API error: {e}")
else:
    print("no error:", bad.choices[0].message.content)


# ── What's next ────────────────────────────────────────────────────────────
#
# - Manage providers:      see manage.py
# - Text-to-speech:        see speak.py
# - Speech-to-text:        see transcribe.py
# - List available models: airouter free-pool -category text
# - Add a new provider:    airouter provider add -preset groq -key gsk_...
print("\nDone. See manage.py / speak.py / transcribe.py for more.")
