#!/usr/bin/env python3
"""
Tenant self-service — full walkthrough using airouter's /v1/me/* endpoints.

As a tenant (with your `air_...` API key) you can manage your OWN models,
keys, limits, profile, and usage — without involving the server operator.
The server's operator manages the shared pool; you manage your own private
models on top.

This file walks through:
  1. Setup (env vars)
  2. whoami — confirm your identity
  3. List my models (own + shared)
  4. Browse the catalog (presets + free-pool models you can pick from)
  5. Add a model with my own API key (stored encrypted, never echoed back)
  6. Pick several models with one key (bulk add from catalog)
  7. Update my model's limits (rpm / rpd / tpm / workers / priority)
  8. Replace a model's API key
  9. Toggle / reset / remove a model
 10. Import / export providers as JSON
 11. My usage report
 12. Update my profile (name, timezone)
 13. Rotate my API key (old one stops working immediately)

RUN:
  pip install requests
  export AIROUTER_URL=http://localhost:8080
  export AIROUTER_KEY=air_...
  export GROQ_API_KEY=gsk_...           # for steps that need a provider key
  python manage.py
"""

from __future__ import annotations

import json
import os
import sys
import requests

# ── 1. Setup ────────────────────────────────────────────────────────────────
AIROUTER_URL = os.environ.get("AIROUTER_URL", "http://localhost:8080")
AIROUTER_KEY = os.environ.get("AIROUTER_KEY", "")
GROQ_API_KEY = os.environ.get("GROQ_API_KEY", "")

if not AIROUTER_KEY:
    sys.exit(
        "error: set AIROUTER_KEY to your tenant api key.\n"
        "  Get one with:  airouter tenant add my-app"
    )

# airouter talks to the tenant-self-service endpoints under /v1/me/*.
# All require Authorization: Bearer <your tenant key>.
HEADERS = {"Authorization": f"Bearer {AIROUTER_KEY}", "Content-Type": "application/json"}
BASE = f"{AIROUTER_URL}/v1/me"


def banner(s: str) -> None:
    print(f"\n── {s} ──────────────────────────────────────────────")

def show(label: str, r: requests.Response) -> dict | None:
    """Pretty-print a labelled response. Returns parsed JSON on success."""
    print(f"\n== {label} ==")
    if not r.ok:
        print(f"HTTP {r.status_code}: {r.text}")
        return None
    try:
        body = r.json()
        print(json.dumps(body, indent=2, sort_keys=True))
        return body
    except ValueError:
        print(r.text or "(empty body)")
        return None


# ── 2. whoami ──────────────────────────────────────────────────────────────
banner("whoami")
show("GET /v1/me", requests.get(BASE, headers=HEADERS))


# ── 3. List my models ──────────────────────────────────────────────────────
#
# Returns {"own": [...], "shared": [...]}. "own" = models you've added
# (private to you). "shared" = server-wide models that also serve your
# requests (read-only for you).
banner("list my models")
r = requests.get(f"{BASE}/providers", headers=HEADERS)
body = show("GET /v1/me/providers", r)
if body:
    own = body.get("own") or []
    shared = body.get("shared") or []
    print(f"\nsummary: {len(own)} own model(s), {len(shared)} shared model(s) also serve you")


# ── 4. Browse the catalog ──────────────────────────────────────────────────
#
# The catalog is the curated free-tier preset list. You pick models from it
# by name + preset. Optional ?preset=groq filters to one preset.
banner("browse catalog")
show("GET /v1/me/catalog?preset=groq", requests.get(f"{BASE}/catalog?preset=groq", headers=HEADERS))


# ── 5. Add a model with my own key ─────────────────────────────────────────
#
# Tenant providers NEVER use the server's env-var keys — you supply your own
# in `api_key`. The key is stored encrypted in the DB; the response never
# echoes it back. The preset auto-fills base_url, reset_tz, and the right
# request shape for the provider kind.
banner("add a model")
add_body = {
    "name":     "groq-oss-managed",
    "preset":   "groq",
    "model":    "openai/gpt-oss-120b",
    "api_key":  GROQ_API_KEY or "gsk_REPLACE_ME",
    "rpm":      30,
    "rpd":      1000,
    "tpm":      8000,
    "workers":  2,
    "priority": 10,
}
r = requests.post(f"{BASE}/providers", headers=HEADERS, json=add_body)
body = show("POST /v1/me/providers", r)
new_id = body.get("id") if body else None


# ── 6. Pick several models with one key ────────────────────────────────────
#
# `catalog/add` lets you add MULTIPLE models from one preset with ONE key.
# Convenient when you have one Groq key and want all of Groq's free models
# available at once.
banner("pick from catalog (bulk add)")
if GROQ_API_KEY:
    r = requests.post(
        f"{BASE}/catalog/add",
        headers=HEADERS,
        json={
            "preset":  "groq",
            "api_key": GROQ_API_KEY,
            "models":  ["openai/gpt-oss-20b", "qwen/qwen3.8-27b"],
        },
    )
    show("POST /v1/me/catalog/add", r)
else:
    print("(skipped — set GROQ_API_KEY to demo bulk add)")


# ── 7. Update my model's limits ────────────────────────────────────────────
#
# Change your own model's RPM/RPD/TPM/workers/priority. Lower priority =
# tried first. Useful for pinning a "burst" model (high priority for fast
# answers) and a "baseline" model (low priority for fallback).
banner("update limits")
if new_id:
    r = requests.post(
        f"{BASE}/providers/{new_id}/limits",
        headers=HEADERS,
        json={"rpm": 15, "rpd": 500, "priority": 5},
    )
    show(f"POST /v1/me/providers/{new_id}/limits", r)
else:
    print("(skipped — no model id from step 5)")


# ── 8. Replace a model's API key ───────────────────────────────────────────
#
# If a key got rotated or revoked upstream, replace it without deleting +
# re-adding the provider. The new key is stored encrypted; any pause on the
# provider is lifted immediately.
banner("set-key")
if new_id and GROQ_API_KEY:
    r = requests.post(
        f"{BASE}/providers/{new_id}/key",
        headers=HEADERS,
        json={"api_key": GROQ_API_KEY},  # use the same key for the demo
    )
    show(f"POST /v1/me/providers/{new_id}/key", r)
else:
    print("(skipped — no model id or no GROQ_API_KEY)")


# ── 9. Toggle / reset / remove ─────────────────────────────────────────────
#
# enable / disable / reset / rm all take the model id in the URL.
# - disable: stop using this model until you re-enable
# - reset:   lift any cooldown / daily-limit pause (try this before debugging)
# - rm:      permanently remove the provider (usage history is kept)
banner("toggle / reset")
if new_id:
    show(f"POST /v1/me/providers/{new_id}/disable",
         requests.post(f"{BASE}/providers/{new_id}/disable", headers=HEADERS))
    show(f"POST /v1/me/providers/{new_id}/enable",
         requests.post(f"{BASE}/providers/{new_id}/enable", headers=HEADERS))
    show(f"POST /v1/me/providers/{new_id}/reset",
         requests.post(f"{BASE}/providers/{new_id}/reset", headers=HEADERS))


# ── 10. Import / export ────────────────────────────────────────────────────
#
# Export your own providers as JSON (handy for backup or moving to another
# server). Import takes the same shape back. The body can be either an array
# or `{"providers": [...]}`.
banner("export")
r = requests.get(f"{BASE}/export", headers=HEADERS)
export_body = show("GET /v1/me/export", r)

if export_body:
    with open("my_providers.json", "w") as f:
        json.dump(export_body, f, indent=2)
    print("(wrote my_providers.json — re-import with the next call)")
    # Import it back — airouter treats same-name entries as updates.
    banner("import")
    with open("my_providers.json", "rb") as f:
        r = requests.post(f"{BASE}/import", headers=HEADERS, data=f)
    show("POST /v1/me/import", r)


# ── 11. Usage report ───────────────────────────────────────────────────────
#
# Per-tenant usage: how many requests / tokens you've used today, against
# which providers, with your daily limit. Pass ?day=YYYY-MM-DD for a
# historical day (per provider's own timezone).
banner("usage")
show("GET /v1/me/usage", requests.get(f"{BASE}/usage", headers=HEADERS))


# ── 12. Update my profile ──────────────────────────────────────────────────
#
# Tenants can change their display name and timezone. The timezone matters:
# it's where your day boundary is for your daily request limit. The daily
# limit itself is set by the operator and is NOT changeable here.
banner("update profile")
show("PATCH /v1/me",
     requests.patch(BASE, headers=HEADERS, json={"name": "updated-demo", "tz": "UTC"}))


# ── 13. Rotate my API key ──────────────────────────────────────────────────
#
# Generates a new API key for this tenant. The OLD key stops working
# IMMEDIATELY. The new key is returned ONCE — save it now. After this call,
# AIROUTER_KEY in this script is stale; subsequent calls would 401.
banner("rotate key")
r = requests.post(f"{BASE}/rotate-key", headers=HEADERS)
body = show("POST /v1/me/rotate-key", r)
if body and body.get("api_key"):
    print("\n*** SAVE THIS NEW KEY NOW ***")
    print(f"new AIROUTER_KEY = {body['api_key']}")
    print("The old key no longer works. Update your env before re-running this script.")


# ── Cleanup: delete the model we added so re-running is clean ─────────────
if new_id:
    banner("cleanup")
    show(f"DELETE /v1/me/providers/{new_id}",
         requests.delete(f"{BASE}/providers/{new_id}", headers=HEADERS))


print("\nDone. See chat.py / speak.py / transcribe.py for using your models.")
