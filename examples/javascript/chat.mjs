#!/usr/bin/env node
/**
 * Chat example — full walkthrough using the `openai` npm package against
 * airouter.
 *
 * airouter speaks the OpenAI Chat Completions wire format, so any OpenAI SDK
 * works out of the box. Point baseURL at the airouter /v1 prefix and use your
 * air_... tenant key as the API key. airouter then routes your prompt across
 * every provider you've added (Groq, Gemini, OpenRouter, ZAI, Ollama, …),
 * automatically failing over when one hits a limit.
 *
 * This file walks through:
 *   1. Setup (env vars, client construction)
 *   2. "auto" routing — let airouter pick the best free provider
 *   3. Pinning one specific model
 *   4. JSON mode (response_format json_object)
 *   5. Multi-turn conversations (message history)
 *   6. Reading the airouter-specific response envelope (provider, latency, attempts)
 *   7. Error handling (rate limits, all-providers-busy)
 *
 * RUN:
 *   npm install openai
 *   export AIROUTER_URL=http://localhost:8080
 *   export AIROUTER_KEY=air_...           # from: airouter tenant add my-app
 *   node chat.mjs
 */

import OpenAI from "openai";

// ── 1. Setup ────────────────────────────────────────────────────────────────
//
// AIROUTER_URL  — where your airouter server is running. The standalone
//                 `airouter serve` defaults to :8080. If you've mounted
//                 airouter inside another server (like the emotion project),
//                 point this at the prefix you mounted it under, e.g.
//                 http://localhost:8000/airouter  (note: no /v1 here — the
//                 SDK appends /chat/completions to baseURL itself).
//
// AIROUTER_KEY  — your tenant API key, starts with `air_`. Get one with
//                 `airouter tenant add my-app` — the key is printed once.

const KEY = process.env.AIROUTER_KEY;
if (!KEY) {
  console.error("Error: set AIROUTER_KEY to your airouter tenant key (starts with air_).");
  console.error("  Get one with:  airouter tenant add my-app");
  process.exit(1);
}

const BASE = (process.env.AIROUTER_URL || "http://localhost:8080").replace(/\/$/, "");
// baseURL MUST point at /v1 — the SDK appends /chat/completions itself.
// So if AIROUTER_URL ends with /airouter (mounted inside another server),
// we end up with:  http://localhost:8000/airouter/v1/chat/completions
const client = new OpenAI({ baseURL: `${BASE}/v1`, apiKey: KEY });

function banner(s) {
  console.log(`\n── ${s} ──────────────────────────────────────────────`);
}

// ── 2. "auto" routing ──────────────────────────────────────────────────────
//
// model: "auto" tells airouter: pick the best free provider, fail over on
// error. Internally airouter tries providers in priority order (Groq first
// by default, then Gemini, then OpenRouter, then ZAI, then Ollama),
// respecting per-provider daily limits, rate limits, and cooldowns. The
// first one that answers wins.
banner("auto routing");
const r = await client.chat.completions.create({
  model: "auto",
  messages: [{ role: "user", content: "Say one short sentence about the ocean." }],
});
console.log("reply:  ", r.choices[0].message.content);
console.log("model:  ", r.model);
// The airouter envelope (provider used, latency, attempts) is on the raw
// response. Look for non-standard fields.
if (r.airouter) {
  console.log(`  provider=${r.airouter.provider}, latency=${r.airouter.latency_ms}ms, attempts=${r.airouter.attempts?.length || 0}`);
}


// ── 3. Pin one specific model ──────────────────────────────────────────────
//
// When you want determinism (or want to compare models), pin one by name.
// Use the exact model id airouter knows — see `airouter provider list` or
// `airouter free-pool -category text` for the full list.
banner("pinned model");
const pinned = await client.chat.completions.create({
  model: "openai/gpt-oss-120b",   // Groq's flagship free model
  messages: [{ role: "user", content: "What is 2+2? Answer with just the number." }],
});
console.log("reply:  ", pinned.choices[0].message.content);
console.log("model:  ", pinned.model);


// ── 4. JSON mode ───────────────────────────────────────────────────────────
//
// response_format: { type: "json_object" } asks the model for JSON-only
// output. ALWAYS pair it with a system prompt that describes the shape —
// models that support JSON mode will produce strict JSON, but they need to
// know what shape to emit. airouter validates the JSON on its end.
banner("JSON mode");
const jr = await client.chat.completions.create({
  model: "auto",
  messages: [
    { role: "system", content: "Reply as {\"emotion\": \"...\", \"intensity\": 1-10} only." },
    { role: "user", content: "I just lost my keys." },
  ],
  response_format: { type: "json_object" },
});
const parsed = JSON.parse(jr.choices[0].message.content);
console.log("parsed JSON:", parsed);


// ── 5. Multi-turn conversation ─────────────────────────────────────────────
//
// Send the full message history on every call. airouter is stateless — it
// doesn't remember previous turns. Maintain the history on your side.
banner("multi-turn");
const history = [
  { role: "system", content: "You are a helpful assistant. Be terse." },
  { role: "user", content: "My name is Ali." },
];
const t1 = await client.chat.completions.create({ model: "auto", messages: history });
console.log("turn 1:", t1.choices[0].message.content);

history.push({ role: "assistant", content: t1.choices[0].message.content });
history.push({ role: "user", content: "What is my name?" });
const t2 = await client.chat.completions.create({ model: "auto", messages: history });
console.log("turn 2:", t2.choices[0].message.content);


// ── 6. Read the airouter envelope ──────────────────────────────────────────
//
// airouter attaches metadata to the response. The OpenAI SDK surfaces
// non-standard fields as properties on the response object. Look for:
//   - provider   — which provider answered (e.g. "groq-openai/gpt-oss-120b")
//   - attempts   — list of providers tried before the successful one
//   - latency_ms — total time including failover
banner("airouter metadata");
const r3 = await client.chat.completions.create({
  model: "auto",
  messages: [{ role: "user", content: "Pick a number between 1 and 10." }],
});
console.log("reply:    ", r3.choices[0].message.content);
console.log("model:    ", r3.model);
// The provider name is encoded in the model field when airouter picks one.
if (r3.model.includes("/")) {
  console.log("preset/model:", r3.model);
}


// ── 7. Error handling ──────────────────────────────────────────────────────
//
// airouter surfaces three error kinds through the OpenAI wire format:
//   - RateLimitError  → all providers are busy right now; retry later.
//                       err.response.headers['retry-after'] tells you when.
//   - APIError        → 4xx/5xx from the upstream provider that ultimately
//                       answered (rare — airouter usually fails over).
//   - BadRequestError → request itself was bad (empty prompt, bad JSON, …).
banner("error handling");
try {
  await client.chat.completions.create({
    model: "nonexistent/model",
    messages: [{ role: "user", content: "this will fail" }],
  });
} catch (e) {
  if (e.status === 429) {
    const retryAfter = e.response?.headers?.["retry-after"] || "?";
    console.log(`all providers busy — retry after ${retryAfter}s`);
  } else if (e.status) {
    console.log(`HTTP ${e.status}: ${e.message}`);
  } else {
    console.log(`error: ${e}`);
  }
}


// ── What's next ────────────────────────────────────────────────────────────
//
// - Manage providers:      see manage.mjs
// - Text-to-speech:        see speak.mjs
// - Speech-to-text:        see transcribe.mjs
// - List available models: airouter free-pool -category text
// - Add a new provider:    airouter provider add -preset groq -key gsk_...
console.log("\nDone. See manage.mjs / speak.mjs / transcribe.mjs for more.");
