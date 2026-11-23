#!/usr/bin/env python3
"""Minimal chat example using only `requests` (no OpenAI SDK).

Drop-in for environments where you cannot install the `openai` package.
Same /v1/chat/completions endpoint, just hand-rolled JSON.
"""

import os
import sys

import requests

AIROUTER_URL = os.environ.get("AIROUTER_URL", "http://localhost:8080")
AIROUTER_KEY = os.environ.get("AIROUTER_KEY", "")

if not AIROUTER_KEY:
    sys.exit("error: set AIROUTER_KEY to your tenant api key (e.g. air_3f9b8e2c...)")


def chat(messages, model="auto", response_format=None):
    """POST /v1/chat/completions and return the assistant text."""
    body = {"model": model, "messages": messages}
    if response_format is not None:
        body["response_format"] = response_format
    r = requests.post(
        f"{AIROUTER_URL}/v1/chat/completions",
        headers={"Authorization": f"Bearer {AIROUTER_KEY}"},
        json=body,
    )
    if not r.ok:
        # print the body so the user sees what went wrong, not just the code
        sys.exit(f"HTTP {r.status_code}: {r.text}")
    return r.json()["choices"][0]["message"]["content"]


print("auto:", chat([{"role": "user", "content": "Say hello in one short sentence."}]))

# JSON mode: same endpoint, add response_format + a system prompt describing
# the shape you want.
print(
    "json:",
    chat(
        [
            {"role": "system", "content": 'Respond ONLY as {"emotion":"...","intensity":1-10}.'},
            {"role": "user", "content": "I just lost my keys and I am running late."},
        ],
        response_format={"type": "json_object"},
    ),
)
