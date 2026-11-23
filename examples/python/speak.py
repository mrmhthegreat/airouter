#!/usr/bin/env python3
"""
Text-to-speech — full walkthrough via airouter's /v1/audio/speech endpoint.

Sends text, gets back raw audio bytes (WAV / MP3 / PCM), writes them to disk.
The request shape is OpenAI-compatible: same body works against OpenAI
directly. airouter routes the request across every TTS provider you've added
(Gemini TTS, Groq Orpheus, …) with automatic failover.

This file walks through:
  1. Setup (env vars)
  2. Basic TTS — text → WAV file
  3. Choosing a voice (provider-specific)
  4. Choosing an output format (wav / mp3 / pcm)
  5. Language hint for non-English text
  6. Pinning one specific TTS provider
  7. Reading the airouter response envelope (provider used, latency)

RUN:
  pip install requests
  export AIROUTER_URL=http://localhost:8080
  export AIROUTER_KEY=air_...
  python speak.py
  # then play the file:
  #   ffplay -autoexit out.wav    (or: play out.wav, afplay out.wav on macOS)
"""

from __future__ import annotations

import os
import sys
import requests

# ── 1. Setup ────────────────────────────────────────────────────────────────
#
# AIROUTER_URL   — where your airouter server is running.
# AIROUTER_KEY   — your tenant API key (starts with air_).
# AIROUTER_TTS_OUT — filename to write the audio to (default out.wav).

AIROUTER_URL = os.environ.get("AIROUTER_URL", "http://localhost:8080")
AIROUTER_KEY = os.environ.get("AIROUTER_KEY", "")
OUT_FILE     = os.environ.get("AIROUTER_TTS_OUT", "out.wav")

if not AIROUTER_KEY:
    sys.exit(
        "error: set AIROUTER_KEY to your tenant api key.\n"
        "  Get one with:  airouter tenant add my-app"
    )

HEADERS = {"Authorization": f"Bearer {AIROUTER_KEY}"}
TTS_URL = f"{AIROUTER_URL}/v1/audio/speech"


def banner(s: str) -> None:
    print(f"\n── {s} ──────────────────────────────────────────────")


def speak(text: str, *, voice: str = "", format: str = "wav",
          language: str = "", provider: str = "", out: str = OUT_FILE) -> bytes:
    """Send one TTS request and write the audio to `out`. Returns the raw bytes.

    Args:
        text:     the text to synthesize (required)
        voice:    voice id — provider-specific. Defaults: "Kore" for Gemini,
                  "tara" for Groq Orpheus English. See `airouter free-pool
                  -category tts` for the full voice list per model.
        format:   "wav" (default), "mp3", or "pcm". WAV is safest — every
                  player handles it.
        language: optional ISO-639-1 hint (e.g. "en", "ar", "ur"). Helps with
                  pronunciation for non-English text.
        provider: pin one TTS provider by name (e.g.
                  "gemini-tts-gemini-3.8-flash-tts"). Default "" = let
                  airouter pick.
        out:      filename to write the audio bytes to.
    """
    body = {
        "model": provider or "auto",   # "auto" = router picks the best TTS provider
        "input": text,
        "voice": voice,
        "response_format": format,
    }
    if language:
        body["language"] = language

    r = requests.post(TTS_URL, headers=HEADERS, json=body)
    if not r.ok:
        # The body has structured error info — show it instead of just the code.
        sys.exit(f"HTTP {r.status_code}: {r.text}")
    with open(out, "wb") as f:
        f.write(r.content)
    return r.content


# ── 2. Basic TTS ────────────────────────────────────────────────────────────
banner("basic TTS — text → WAV")
data = speak("Hello world from airouter. This is a text-to-speech test.")
print(f"wrote {len(data)} bytes to {OUT_FILE}")
print(f"play it with:  ffplay -autoexit {OUT_FILE}")


# ── 3. Choosing a voice ────────────────────────────────────────────────────
#
# Voice ids are provider-specific:
#   - Gemini TTS:  Kore, Puck, Charon, Aoede, Leda, Orus, Fenrir, Zap
#   - Groq Orpheus: tara (English), sroas (English), zofa (Spanish), ...
#
# See `airouter free-pool -category tts` for the catalog.
banner("specific voice")
speak("The wind whispered through the autumn leaves.",
      voice="Kore", out="voice.wav")
print("wrote voice.wav with voice=Kore (Gemini's default)")


# ── 4. Output format ───────────────────────────────────────────────────────
#
# wav  — uncompressed PCM + WAV header. Largest, universally playable.
# mp3  — compressed. Smaller, fine for storage / streaming.
# pcm  — raw 24kHz 16-bit mono PCM, no header. Use when feeding another tool
#        that wants raw samples.
banner("output format — mp3")
speak("Same text, but as MP3.", format="mp3", out="out.mp3")
print("wrote out.mp3")


# ── 5. Language hint ───────────────────────────────────────────────────────
#
# For non-English text, pass a language hint so the model pronounces it
# correctly. ISO 639-1 codes: en, ar, ur, fr, es, de, hi, ja, …
banner("language hint — Urdu")
speak("آج موسم بہت اچھا ہے۔", language="ur", out="urdu.wav")
print("wrote urdu.wav with language=ur")


# ── 6. Pin a specific TTS provider ─────────────────────────────────────────
#
# Same as chat: pass a specific model id to skip routing.
# Find TTS-capable models with:  airouter free-pool -category tts
banner("pin TTS provider")
speak("Pinned to one specific TTS model.",
      provider="gemini-tts-gemini-3.8-flash-tts",
      out="pinned.wav")
print("wrote pinned.wav with provider=gemini-tts-gemini-3.8-flash-tts")


# ── 7. Read the airouter envelope ──────────────────────────────────────────
#
# The HTTP response headers carry metadata about which provider answered and
# how long it took. Useful for analytics.
banner("response headers")
r = requests.post(TTS_URL, headers=HEADERS, json={
    "model": "auto",
    "input": "Read the response headers.",
    "response_format": "wav",
})
if r.ok:
    print("content-type:    ", r.headers.get("Content-Type"))
    print("X-Airouter-Provider:", r.headers.get("X-Airouter-Provider", "(not set)"))
    print("X-Airouter-Latency:", r.headers.get("X-Airouter-Latency", "(not set)"))
    print("bytes:           ", len(r.content))


print("\nDone. Files written: out.wav, voice.wav, out.mp3, urdu.wav, pinned.wav")
print("Next: run transcribe.py to turn one of these back into text.")
