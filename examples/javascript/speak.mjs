#!/usr/bin/env node
/**
 * Text-to-speech — full walkthrough via the OpenAI-compatible
 * /v1/audio/speech endpoint.
 *
 * Sends {model, input, voice, response_format} as JSON and gets raw audio
 * bytes back. airouter routes the request across every TTS provider you've
 * added (Gemini TTS, Groq Orpheus, …) with automatic failover. The chosen
 * provider is surfaced in the X-Airouter-Provider response header.
 *
 * This file walks through:
 *   1. Setup (env vars)
 *   2. Basic TTS — text → WAV file
 *   3. Choosing a voice (provider-specific)
 *   4. Choosing an output format (wav / mp3 / pcm)
 *   5. Language hint for non-English text
 *   6. Pinning one specific TTS provider
 *   7. Reading the airouter response envelope (provider used, latency)
 *
 * RUN:
 *   export AIROUTER_URL=http://localhost:8080
 *   export AIROUTER_KEY=air_...
 *   node speak.mjs
 *   # then play the file:
 *   #   ffplay -nodisp -autoexit out.wav   (or: play out.wav, afplay out.wav on macOS)
 */

import { writeFile } from "node:fs/promises";

// ── 1. Setup ────────────────────────────────────────────────────────────────
const KEY = process.env.AIROUTER_KEY;
if (!KEY) {
  console.error("Error: set AIROUTER_KEY to your airouter tenant key (starts with air_).");
  console.error("  Get one with:  airouter tenant add my-app");
  process.exit(1);
}

// AIROUTER_URL  — where your airouter server is running.
// AIROUTER_TTS_OUT — filename to write the audio to (default out.wav).
const BASE = (process.env.AIROUTER_URL || "http://localhost:8080").replace(/\/$/, "");
const TTS_URL = `${BASE}/v1/audio/speech`;

function banner(s) {
  console.log(`\n── ${s} ──────────────────────────────────────────────`);
}

/**
 * Send one TTS request and write the audio to `out`. Returns the raw bytes.
 *
 * @param {string}  text      - the text to synthesize (required)
 * @param {object}  [opts]
 * @param {string}  [opts.voice]     - voice id, provider-specific. Defaults:
 *                                     "Kore" for Gemini, "tara" for Groq
 *                                     Orpheus English. See
 *                                     `airouter free-pool -category tts`.
 * @param {string}  [opts.format]    - "wav" (default), "mp3", or "pcm". WAV
 *                                     is safest — every player handles it.
 * @param {string}  [opts.language]  - ISO-639-1 hint (e.g. "en", "ar", "ur")
 * @param {string}  [opts.provider]  - pin one TTS provider by name (e.g.
 *                                     "gemini-tts-gemini-3.8-flash-tts").
 *                                     Default "" = let airouter pick.
 * @param {string}  [opts.out]       - filename to write the audio bytes to.
 * @returns {Promise<Buffer>} the raw audio bytes
 */
async function speak(text, opts = {}) {
  const {
    voice = "",
    format = "wav",
    language = "",
    provider = "",
    out = "out.wav",
  } = opts;

  const body = {
    model: provider || "auto",   // "auto" = router picks the best TTS provider
    input: text,
    voice,
    response_format: format,
  };
  if (language) body.language = language;

  const r = await fetch(TTS_URL, {
    method: "POST",
    headers: { Authorization: `Bearer ${KEY}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    console.error(`speech failed: HTTP ${r.status}`);
    console.error(await r.text());
    process.exit(1);
  }
  const buf = Buffer.from(await r.arrayBuffer());
  await writeFile(out, buf);
  return { buf, headers: r.headers };
}


// ── 2. Basic TTS ────────────────────────────────────────────────────────────
banner("basic TTS — text → WAV");
const { buf } = await speak("Hello world from airouter. This is a text-to-speech test.");
console.log(`wrote out.wav (${buf.length} bytes)`);


// ── 3. Choosing a voice ────────────────────────────────────────────────────
//
// Voice ids are provider-specific:
//   - Gemini TTS:  Kore, Puck, Charon, Aoede, Leda, Orus, Fenrir, Zap
//   - Groq Orpheus: tara (English), sroas (English), zofa (Spanish), ...
//
// See `airouter free-pool -category tts` for the catalog.
banner("specific voice");
await speak("The wind whispered through the autumn leaves.", { voice: "Kore", out: "voice.wav" });
console.log("wrote voice.wav with voice=Kore (Gemini's default)");


// ── 4. Output format ───────────────────────────────────────────────────────
//
// wav  — uncompressed PCM + WAV header. Largest, universally playable.
// mp3  — compressed. Smaller, fine for storage / streaming.
// pcm  — raw 24kHz 16-bit mono PCM, no header. Use when feeding another tool
//        that wants raw samples.
banner("output format — mp3");
await speak("Same text, but as MP3.", { format: "mp3", out: "out.mp3" });
console.log("wrote out.mp3");


// ── 5. Language hint ───────────────────────────────────────────────────────
//
// For non-English text, pass a language hint so the model pronounces it
// correctly. ISO 639-1 codes: en, ar, ur, fr, es, de, hi, ja, …
banner("language hint — Urdu");
await speak("آج موسم بہت اچھا ہے۔", { language: "ur", out: "urdu.wav" });
console.log("wrote urdu.wav with language=ur");


// ── 6. Pin a specific TTS provider ─────────────────────────────────────────
//
// Same as chat: pass a specific model id to skip routing.
// Find TTS-capable models with:  airouter free-pool -category tts
banner("pin TTS provider");
try {
  await speak("Pinned to one specific TTS model.", {
    provider: "gemini-tts-gemini-3.8-flash-tts",
    out: "pinned.wav",
  });
  console.log("wrote pinned.wav with provider=gemini-tts-gemini-3.8-flash-tts");
} catch (e) {
  console.log("  (pinned provider not available:", e.message, ")");
}


// ── 7. Read the airouter envelope ──────────────────────────────────────────
//
// The HTTP response headers carry metadata about which provider answered and
// how long it took. Useful for analytics.
banner("response headers");
const { headers } = await speak("Read the response headers.");
console.log("content-type:       ", headers.get("content-type"));
console.log("X-Airouter-Provider:", headers.get("x-airouter-provider") || "(not set)");
console.log("X-Airouter-Latency:", headers.get("x-airouter-latency") || "(not set)");


console.log("\nDone. Files written: out.wav, voice.wav, out.mp3, urdu.wav, pinned.wav");
console.log("Next: run transcribe.mjs to turn one of these back into text.");
