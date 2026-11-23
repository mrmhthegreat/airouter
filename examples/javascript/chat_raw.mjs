#!/usr/bin/env node
/**
 * Minimal chat example using plain fetch (zero dependencies).
 *
 * Same call as chat.mjs but without the openai npm package, so the wire format
 * is fully visible. Copy this into any runtime that has fetch (Node 18+, Deno,
 * Bun, browsers) and it works — airouter speaks the OpenAI Chat Completions
 * shape exactly. On HTTP errors we print the response body, not just the status,
 * so you can see what airouter actually rejected.
 *
 * Env vars:  AIROUTER_URL (default http://localhost:8080), AIROUTER_KEY
 */
const KEY = process.env.AIROUTER_KEY;
if (!KEY) {
  console.error("Error: set AIROUTER_KEY to your airouter tenant key (starts with air_).");
  console.error("  Get one with:  airouter tenant add my-app");
  process.exit(1);
}

const BASE = (process.env.AIROUTER_URL || "http://localhost:8080").replace(/\/$/, "");
const headers = { Authorization: `Bearer ${KEY}`, "Content-Type": "application/json" };

const r = await fetch(`${BASE}/v1/chat/completions`, {
  method: "POST",
  headers,
  body: JSON.stringify({
    model: "auto",
    messages: [{ role: "user", content: "Say one short sentence about the ocean." }],
  }),
});

if (!r.ok) {
  console.error(`chat failed: HTTP ${r.status}`);
  console.error(await r.text());
  process.exit(1);
}

const data = await r.json();
console.log("reply:", data.choices[0].message.content);
if (data.airouter) {
  console.log(
    `  (provider=${data.airouter.provider}, latency=${data.airouter.latency_ms}ms, attempts=${data.airouter.attempts})`,
  );
}
