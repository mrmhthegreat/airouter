#!/usr/bin/env node
/**
 * Speech-to-text — full walkthrough via the OpenAI-compatible
 * /v1/audio/transcriptions endpoint.
 *
 * Send an audio file as multipart form data, get text back. Pair with
 * speak.mjs to make a file first:
 *
 *     node speak.mjs && node transcribe.mjs out.wav
 *
 * airouter routes the request across every STT provider you've added (Groq
 * Whisper, Gemini, …) with automatic failover. The request shape is
 * OpenAI-compatible — same body works against OpenAI directly.
 *
 * This file walks through:
 *   1. Setup (env vars, input file)
 *   2. Basic transcription — audio file → text
 *   3. Language hint (auto-detect by default)
 *   4. Spelling / style prompt (for names, jargon)
 *   5. Pinning one specific STT provider
 *   6. Reading the airouter response envelope
 *
 * RUN:
 *   export AIROUTER_URL=http://localhost:8080
 *   export AIROUTER_KEY=air_...
 *   node transcribe.mjs out.wav
 *   # or override the language:
 *   node transcribe.mjs out.wav en
 */

import { readFile } from "node:fs/promises";
import { extname } from "node:path";

// ── 1. Setup ────────────────────────────────────────────────────────────────
//
// AIROUTER_URL    — where your airouter server is running.
// AIROUTER_KEY    — your tenant API key (starts with air_).
// argv[2]         — path to the audio file. Supports mp3, wav, m4a, flac, ogg.
// argv[3]         — optional language hint (en, fr, es, ar, ur, ...)

const KEY = process.env.AIROUTER_KEY;
if (!KEY) {
  console.error("Error: set AIROUTER_KEY to your airouter tenant key (starts with air_).");
  console.error("  Get one with:  airouter tenant add my-app");
  process.exit(1);
}

const PATH = process.argv[2];
if (!PATH) {
  console.error("Usage: node transcribe.mjs <audio-file> [language]");
  console.error("  audio-file: path to a .wav/.mp3/.m4a/.flac/.ogg file");
  console.error("  language:   optional ISO-639-1 hint, e.g. en, fr, es, ar, ur");
  console.error("");
  console.error("Tip: run speak.mjs first to produce out.wav, then:");
  console.error("  node transcribe.mjs out.wav");
  process.exit(1);
}
const LANGUAGE_ARG = process.argv[3] || "";

const BASE = (process.env.AIROUTER_URL || "http://localhost:8080").replace(/\/$/, "");
const STT_URL = `${BASE}/v1/audio/transcriptions`;

function banner(s) {
  console.log(`\n── ${s} ──────────────────────────────────────────────`);
}

// MIME type detection — fetch needs it for the multipart upload.
const MIME_BY_EXT = {
  ".wav":  "audio/wav",
  ".mp3":  "audio/mpeg",
  ".m4a":  "audio/mp4",
  ".ogg":  "audio/ogg",
  ".flac": "audio/flac",
  ".webm": "audio/webm",
};
function mimeFor(path) {
  const ext = extname(path).toLowerCase();
  return MIME_BY_EXT[ext] || "application/octet-stream";
}

/**
 * Send one STT request. Returns the parsed JSON response.
 *
 * @param {string}  path     - path to the audio file (required)
 * @param {object}  [opts]
 * @param {string}  [opts.language]  - ISO-639-1 hint (e.g. "en", "ar", "ur").
 *                                     Default "" = auto-detect.
 * @param {string}  [opts.prompt]    - optional style/spelling hint (max 224
 *                                     tokens). Use this to help the model
 *                                     with names, jargon, or punctuation.
 * @param {string}  [opts.provider]  - pin one STT provider by name (e.g.
 *                                     "groq-whisper-large-v3-turbo").
 *                                     Default "" = let airouter pick.
 * @returns {Promise<{body: object, headers: Headers}>}
 */
async function transcribe(path, opts = {}) {
  const { language = "", prompt = "", provider = "" } = opts;

  const bytes = await readFile(path);
  const form = new FormData();
  form.append("file", new Blob([bytes], { type: mimeFor(path) }), path);
  form.append("model", provider || "auto");
  if (language) form.append("language", language);
  if (prompt) form.append("prompt", prompt);

  const r = await fetch(STT_URL, {
    method: "POST",
    headers: { Authorization: `Bearer ${KEY}` },
    body: form,
  });
  if (!r.ok) {
    console.error(`transcribe failed: HTTP ${r.status}`);
    console.error(await r.text());
    process.exit(1);
  }
  return { body: await r.json(), headers: r.headers };
}


// ── 2. Basic transcription ─────────────────────────────────────────────────
banner("basic transcription");
const { body: result } = await transcribe(PATH);
console.log("transcript:", result.text || "");
console.log("language:   ", result.language || "(not reported)");
console.log("duration:   ", result.duration ?? "(not reported)", "seconds");


// ── 3. Language hint ───────────────────────────────────────────────────────
//
// By default the model auto-detects the language. Pass a hint to force one —
// useful when the auto-detect guesses wrong, or when you know the audio is
// e.g. Urdu and want to skip the detection step (faster).
const forcedLang = LANGUAGE_ARG || "en";
banner(`language hint — ${forcedLang}`);
const { body: resultLang } = await transcribe(PATH, { language: forcedLang });
console.log(`transcript (forced ${forcedLang}):`, resultLang.text || "");


// ── 4. Spelling / style prompt ─────────────────────────────────────────────
//
// The optional `prompt` is NOT a system prompt — it's a transcription hint.
// Use it to:
//   - Spell names: "Dr. Smith, Acme Corp., Q3 2026 earnings call."
//   - Force punctuation: "Transcribe with full punctuation."
//   - Bias toward domain jargon: "Cardiology appointment. Terms: systolic, diastolic."
// Max 224 tokens. The model uses it as context, not as something to repeat.
banner("spelling / style prompt");
const { body: resultPrompt } = await transcribe(PATH, {
  prompt: "Transcribe in formal English with full punctuation.",
});
console.log("transcript (with prompt):", resultPrompt.text || "");


// ── 5. Pin one specific STT provider ──────────────────────────────────────
//
// Same as chat and TTS: pass a specific model id to skip routing.
// Find STT-capable models with:  airouter free-pool -category stt
banner("pin STT provider");
try {
  const { body: resultPinned } = await transcribe(PATH, {
    provider: "groq-whisper-large-v3-turbo",
  });
  console.log("transcript (pinned):", resultPinned.text || "");
} catch (e) {
  // If the pinned provider isn't installed, airouter returns 4xx.
  console.log("  (pinned provider not available:", e.message, ")");
}


// ── 6. Read the airouter envelope ─────────────────────────────────────────
//
// The HTTP response headers carry which provider actually answered.
banner("response headers");
const { headers } = await transcribe(PATH);
console.log("X-Airouter-Provider:", headers.get("x-airouter-provider") || "(not set)");
console.log("X-Airouter-Latency:", headers.get("x-airouter-latency") || "(not set)");


console.log("\nDone. See chat.mjs for chat completions, speak.mjs for text-to-speech.");
