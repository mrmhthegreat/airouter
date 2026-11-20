#!/usr/bin/env python3
"""
Speech-to-text — full walkthrough via airouter's /v1/audio/transcriptions.

Send an audio file as multipart form data, get text back. Pair with speak.py
to make a file first:

    python speak.py && python transcribe.py

airouter routes the request across every STT provider you've added (Groq
Whisper, Gemini, …) with automatic failover. The request shape is
OpenAI-compatible — same body works against OpenAI directly.

This file walks through:
  1. Setup (env vars, input file)
  2. Basic transcription — audio file → text
  3. Language hint (auto-detect by default)
  4. Spelling / style prompt (for names, jargon)
  5. Pinning one specific STT provider
  6. Reading the airouter response envelope

RUN:
  pip install requests
  export AIROUTER_URL=http://localhost:8080
  export AIROUTER_KEY=air_...
  python transcribe.py
  # or override the input file:
  AIROUTER_STT_IN=my_recording.mp3 python transcribe.py
"""

from __future__ import annotations

import os
import sys
import requests

# ── 1. Setup ────────────────────────────────────────────────────────────────
#
# AIROUTER_URL    — where your airouter server is running.
# AIROUTER_KEY    — your tenant API key (starts with air_).
# AIROUTER_STT_IN — path to the audio file to transcribe. Default: out.wav
#                   (created by speak.py). Supports mp3, wav, m4a, flac, ogg.

AIROUTER_URL = os.environ.get("AIROUTER_URL", "http://localhost:8080")
AIROUTER_KEY = os.environ.get("AIROUTER_KEY", "")
IN_FILE      = os.environ.get("AIROUTER_STT_IN", "out.wav")

if not AIROUTER_KEY:
    sys.exit(
        "error: set AIROUTER_KEY to your tenant api key.\n"
        "  Get one with:  airouter tenant add my-app"
    )
if not os.path.isfile(IN_FILE):
    sys.exit(
        f"error: input audio not found: {IN_FILE}\n"
        f"  run speak.py first to create out.wav, or set AIROUTER_STT_IN=path/to/file"
    )

HEADERS = {"Authorization": f"Bearer {AIROUTER_KEY}"}
STT_URL = f"{AIROUTER_URL}/v1/audio/transcriptions"


def banner(s: str) -> None:
    print(f"\n── {s} ──────────────────────────────────────────────")


# MIME type detection — requests needs it for the multipart upload.
# For .wav the type is always audio/wav; for others we infer from the extension.
MIME_BY_EXT = {
    ".wav":  "audio/wav",
    ".mp3":  "audio/mpeg",
    ".m4a":  "audio/mp4",
    ".ogg":  "audio/ogg",
    ".flac": "audio/flac",
    ".webm": "audio/webm",
}

def mime_for(path: str) -> str:
    _, ext = os.path.splitext(path)
    return MIME_BY_EXT.get(ext.lower(), "application/octet-stream")


def transcribe(path: str, *, language: str = "", prompt: str = "",
               provider: str = "") -> dict:
    """Send one STT request. Returns the parsed JSON response.

    Args:
        path:     path to the audio file (required)
        language: ISO-639-1 hint (e.g. "en", "ar", "ur"). Default "" = auto-detect.
        prompt:   optional style/spelling hint (max 224 tokens). Use this to
                  help the model with names, jargon, or punctuation style.
                  Example: "Interview with Dr. Smith. Transcribe in formal English."
        provider: pin one STT provider by name (e.g.
                  "groq-whisper-large-v3-turbo"). Default "" = let airouter pick.
    """
    body = {"model": provider or "auto"}
    if language:
        body["language"] = language
    if prompt:
        body["prompt"] = prompt

    # Do NOT set Content-Type here — requests builds the multipart boundary
    # for us when we use the `files=` argument.
    with open(path, "rb") as f:
        r = requests.post(
            STT_URL,
            headers=HEADERS,
            files={"file": (os.path.basename(path), f, mime_for(path))},
            data=body,
        )
    if not r.ok:
        sys.exit(f"HTTP {r.status_code}: {r.text}")
    return r.json()


# ── 2. Basic transcription ─────────────────────────────────────────────────
banner("basic transcription")
result = transcribe(IN_FILE)
print("transcript:", result.get("text", ""))
print("language:   ", result.get("language", "(not reported)"))
print("duration:   ", result.get("duration", "(not reported)"), "seconds")


# ── 3. Language hint ───────────────────────────────────────────────────────
#
# By default the model auto-detects the language. Pass a hint to force one —
# useful when the auto-detect guesses wrong, or when you know the audio is
# e.g. Urdu and want to skip the detection step (faster).
banner("language hint — English")
result_en = transcribe(IN_FILE, language="en")
print("transcript (forced en):", result_en.get("text", ""))


# ── 4. Spelling / style prompt ─────────────────────────────────────────────
#
# The optional `prompt` is NOT a system prompt — it's a transcription hint.
# Use it to:
#   - Spell names: "Dr. Smith, Acme Corp., Q3 2026 earnings call."
#   - Force punctuation: "Transcribe with full punctuation."
#   - Bias toward domain jargon: "Cardiology appointment. Terms: systolic, diastolic."
# Max 224 tokens. The model uses it as context, not as something to repeat.
banner("spelling / style prompt")
result_prompt = transcribe(IN_FILE, prompt="Transcribe in formal English with full punctuation.")
print("transcript (with prompt):", result_prompt.get("text", ""))


# ── 5. Pin one specific STT provider ──────────────────────────────────────
#
# Same as chat and TTS: pass a specific model id to skip routing.
# Find STT-capable models with:  airouter free-pool -category stt
banner("pin STT provider")
try:
    result_pinned = transcribe(IN_FILE, provider="groq-whisper-large-v3-turbo")
    print("transcript (pinned):", result_pinned.get("text", ""))
except SystemExit as e:
    # If the pinned provider isn't installed, airouter returns 4xx — show it
    # but don't kill the script.
    print(f"  (pinned provider not available: {e})")


# ── 6. Read the airouter envelope ─────────────────────────────────────────
#
# The HTTP response headers carry which provider actually answered.
banner("response headers")
import json as _json
with open(IN_FILE, "rb") as f:
    r = requests.post(
        STT_URL,
        headers=HEADERS,
        files={"file": (os.path.basename(IN_FILE), f, mime_for(IN_FILE))},
        data={"model": "auto"},
    )
if r.ok:
    print("X-Airouter-Provider:", r.headers.get("X-Airouter-Provider", "(not set)"))
    print("X-Airouter-Latency:", r.headers.get("X-Airouter-Latency", "(not set)"))
    print("body:", _json.dumps(r.json(), indent=2))


print("\nDone. See chat.py for chat completions, speak.py for text-to-speech.")
