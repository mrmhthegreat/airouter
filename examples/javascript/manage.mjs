#!/usr/bin/env node
/**
 * Tenant self-service — full walkthrough using airouter's /v1/me/* endpoints.
 *
 * As a tenant (with your `air_...` API key) you can manage your OWN models,
 * keys, limits, profile, and usage — without involving the server operator.
 * The server's operator manages the shared pool; you manage your own private
 * models on top.
 *
 * This file walks through:
 *   1.  Setup (env vars)
 *   2.  whoami — confirm your identity
 *   3.  List my models (own + shared)
 *   4.  Browse the catalog (presets + free-pool models you can pick from)
 *   5.  Add a model with my own API key (stored encrypted, never echoed back)
 *   6.  Pick several models with one key (bulk add from catalog)
 *   7.  Update my model's limits (rpm / rpd / tpm / workers / priority)
 *   8.  Replace a model's API key
 *   9.  Toggle / reset / remove a model
 *  10.  Import / export providers as JSON
 *  11.  My usage report
 *  12.  Update my profile (name, timezone)
 *  13.  Rotate my API key (old one stops working immediately)
 *
 * RUN:
 *   export AIROUTER_URL=http://localhost:8080
 *   export AIROUTER_KEY=air_...
 *   export GROQ_API_KEY=gsk_...           # for steps that need a provider key
 *   node manage.mjs
 */

// ── 1. Setup ────────────────────────────────────────────────────────────────
const KEY = process.env.AIROUTER_KEY;
const GROQ_API_KEY = process.env.GROQ_API_KEY || "";

if (!KEY) {
  console.error("Error: set AIROUTER_KEY to your airouter tenant key (starts with air_).");
  console.error("  Get one with:  airouter tenant add my-app");
  process.exit(1);
}

const BASE = (process.env.AIROUTER_URL || "http://localhost:8080").replace(/\/$/, "");
const ME_URL = `${BASE}/v1/me`;

function banner(s) {
  console.log(`\n── ${s} ──────────────────────────────────────────────`);
}

/**
 * fetch wrapper that prints the response body on HTTP errors and returns JSON.
 * @param {string} method
 * @param {string} path  - path under /v1/me, e.g. "/providers" → "/v1/me/providers"
 * @param {object} [body] - request body (will be JSON-stringified)
 * @returns {Promise<object|null>}
 */
async function req(method, path, body) {
  const init = { method, headers: { Authorization: `Bearer ${KEY}` } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const r = await fetch(`${ME_URL}${path}`, init);
  const text = await r.text();
  if (!r.ok) {
    console.error(`\n${method} ${path} failed: HTTP ${r.status}`);
    console.error(text || "(empty body)");
    process.exit(1);
  }
  return text ? JSON.parse(text) : null;
}


// ── 2. whoami ──────────────────────────────────────────────────────────────
banner("whoami");
const me = await req("GET", "");
console.log(`  id=${me.tenant.id} name=${me.tenant.name} tz=${me.tenant.tz} limit=${me.tenant.daily_request_limit}`);


// ── 3. List my models ──────────────────────────────────────────────────────
//
// Returns {"own": [...], "shared": [...]}. "own" = models you've added
// (private to you). "shared" = server-wide models that also serve your
// requests (read-only for you).
banner("list my models");
const before = await req("GET", "/providers");
const mine = before.own || [];
const shared = before.shared || [];
console.log(`  ${mine.length} own model(s), ${shared.length} shared model(s) also serve you`);
for (const p of mine.slice(0, 5)) {
  console.log(`    - id=${p.id} name=${p.name} model=${p.model} state=${p.state}`);
}
if (mine.length > 5) console.log(`    ... and ${mine.length - 5} more`);


// ── 4. Browse the catalog ──────────────────────────────────────────────────
//
// The catalog is the curated free-tier preset list. You pick models from it
// by name + preset. Optional ?preset=groq filters to one preset.
banner("browse catalog (groq only)");
const catalog = await req("GET", "/catalog?preset=groq");
const catalogModels = catalog.models || [];
console.log(`  ${catalogModels.length} groq models available in the catalog:`);
for (const m of catalogModels.slice(0, 5)) {
  console.log(`    - ${m.model}  (rpd=${m.rpd}, rpm=${m.rpm}, prio=${m.priority})`);
}


// ── 5. Add a model with my own key ─────────────────────────────────────────
//
// Tenant providers NEVER use the server's env-var keys — you supply your own
// in `api_key`. The key is stored encrypted in the DB; the response never
// echoes it back. The preset auto-fills base_url, reset_tz, and the right
// request shape for the provider kind.
banner("add a model");
const added = await req("POST", "/providers", {
  name:     "groq-oss-managed",
  preset:   "groq",
  model:    "openai/gpt-oss-120b",
  api_key:  GROQ_API_KEY || "gsk_REPLACE_ME",
  rpm:      30,
  rpd:      1000,
  tpm:      8000,
  workers:  2,
  priority: 10,
});
console.log(`  added provider id=${added.id} name=${added.name}`);
console.log(`  (api_key is stored encrypted; never echoed back)`);


// ── 6. Pick several models with one key ────────────────────────────────────
//
// `catalog/add` lets you add MULTIPLE models from one preset with ONE key.
// Convenient when you have one Groq key and want all of Groq's free models
// available at once.
banner("pick from catalog (bulk add)");
if (GROQ_API_KEY) {
  const pick = await req("POST", "/catalog/add", {
    preset:  "groq",
    api_key: GROQ_API_KEY,
    models:  ["openai/gpt-oss-20b", "qwen/qwen3.8-27b"],
  });
  console.log(`  added=${(pick.added || []).length} skipped=${(pick.skipped || []).length}`);
} else {
  console.log("  (skipped — set GROQ_API_KEY to demo bulk add)");
}


// ── 7. Update my model's limits ────────────────────────────────────────────
//
// Change your own model's RPM/RPD/TPM/workers/priority. Lower priority =
// tried first. Useful for pinning a "burst" model (high priority for fast
// answers) and a "baseline" model (low priority for fallback).
banner("update limits");
const limitsResp = await req("POST", `/providers/${added.id}/limits`, {
  rpm: 15, rpd: 500, priority: 5,
});
console.log(`  limits updated for #${added.id}:`, limitsResp);


// ── 8. Replace a model's API key ───────────────────────────────────────────
//
// If a key got rotated or revoked upstream, replace it without deleting +
// re-adding the provider. The new key is stored encrypted; any pause on the
// provider is lifted immediately.
banner("set-key");
if (GROQ_API_KEY) {
  const keyResp = await req("POST", `/providers/${added.id}/key`, { api_key: GROQ_API_KEY });
  console.log(`  key replaced for #${added.id}:`, keyResp);
} else {
  console.log("  (skipped — no GROQ_API_KEY)");
}


// ── 9. Toggle / reset / remove ─────────────────────────────────────────────
//
// enable / disable / reset / rm all take the model id in the URL.
// - disable: stop using this model until you re-enable
// - reset:   lift any cooldown / daily-limit pause (try this before debugging)
// - rm:      permanently remove the provider (usage history is kept)
banner("toggle / reset");
console.log(`  disable:`, await req("POST", `/providers/${added.id}/disable`));
console.log(`  enable: `, await req("POST", `/providers/${added.id}/enable`));
console.log(`  reset:  `, await req("POST", `/providers/${added.id}/reset`));


// ── 10. Import / export ────────────────────────────────────────────────────
//
// Export your own providers as JSON (handy for backup or moving to another
// server). Import takes the same shape back. The body can be either an array
// or {"providers": [...]}.
banner("export");
const exportBody = await req("GET", "/export");
import { writeFile, readFile } from "node:fs/promises";
await writeFile("my_providers.json", JSON.stringify(exportBody, null, 2));
console.log("  wrote my_providers.json");

banner("import");
const importData = await readFile("my_providers.json", "utf8");
const importResp = await req("POST", "/import", JSON.parse(importData));
console.log(`  imported:`, importResp);


// ── 11. Usage report ───────────────────────────────────────────────────────
//
// Per-tenant usage: how many requests / tokens you've used today, against
// which providers, with your daily limit.
banner("usage");
const usage = await req("GET", "/usage");
console.log(`  server_time: ${usage.server_time}`);
console.log(`  tenant: ${usage.tenant}, requests today: ${usage.requests_today}, tokens: ${usage.tokens_today}`);
if (usage.providers) {
  for (const u of usage.providers.slice(0, 5)) {
    console.log(`    - ${u.provider}/${u.model}: ${u.tenant_requests} req, ${u.tenant_tokens} tok`);
  }
}


// ── 12. Update my profile ──────────────────────────────────────────────────
//
// Tenants can change their display name and timezone. The timezone matters:
// it's where your day boundary is for your daily request limit. The daily
// limit itself is set by the operator and is NOT changeable here.
banner("update profile");
const patched = await req("PATCH", "", { name: "updated-demo", tz: "UTC" });
console.log(`  saved: name=${patched.tenant.name} tz=${patched.tenant.tz}`);


// ── 13. Rotate my API key ──────────────────────────────────────────────────
//
// Generates a new API key for this tenant. The OLD key stops working
// IMMEDIATELY. The new key is returned ONCE — save it now. After this call,
// AIROUTER_KEY in this script is stale; subsequent calls would 401.
banner("rotate key (RUN LAST)");
const rotated = await req("POST", "/rotate-key");
console.log("  *** SAVE THIS NEW KEY NOW ***");
console.log(`    new AIROUTER_KEY = ${rotated.api_key}`);
console.log("  The old key no longer works. Update your env before re-running.");


// ── Cleanup: delete the model we added so re-running is clean ─────────────
banner("cleanup");
const del = await req("DELETE", `/providers/${added.id}`);
console.log(`  deleted #${added.id}:`, del);


console.log("\nDone. See chat.mjs / speak.mjs / transcribe.mjs for using your models.");
