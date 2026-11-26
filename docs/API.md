# airouter HTTP API reference

Start the server:

```bash
export AIROUTER_ADMIN_TOKEN=change-me
export AIROUTER_SECRET=$(openssl rand -hex 32)
./bin/airouter tenant add acme -limit 2000     # prints a tenant key (air_...) ONCE
./bin/airouter serve -addr :8080
```

Base URL in all examples: `http://localhost:8080`. Machine-readable spec: [`openapi.yaml`](openapi.yaml) (open it in [`swagger.html`](swagger.html)).

## Contents

- [Authentication](#authentication)
- [Errors and status codes](#errors-and-status-codes)
- [Native API](#native-api): `/v1/generate`, `/v1/speak`, `/v1/transcribe`, `/v1/usage`, `/v1/overview`, `/v1/whoami`
- [Voices (ElevenLabs)](#voices-elevenlabs): list, clone, delete, per-request voice settings
- [OpenAI-compatible API](#openai-compatible-api)
- [Me: tenant self-service](#me-tenant-self-service-v1me)
- [Admin API](#admin-api)
- [Public endpoints](#public-endpoints)
- [Object reference](#object-reference)

---

## Authentication

| Who | Header | Value |
|-----|--------|-------|
| Tenant (a user or app) | `Authorization: Bearer air_...` | Key printed by `tenant add` (or `X-API-Key: air_...`) |
| Admin (operator) | `Authorization: Bearer <token>` | Value of `AIROUTER_ADMIN_TOKEN` |

- If `AIROUTER_ADMIN_TOKEN` is not set, admin endpoints answer `403`. Use the CLI instead.
- The admin token also works on tenant endpoints. Add `X-Tenant-ID: acme` (or `?tenant=acme`) to act as that tenant. Without it, the `default` tenant is used.
- Browser apps on another origin: start the server with `-cors https://myapp.com` (or `AIROUTER_CORS`). Allowed headers: `Authorization, Content-Type, X-API-Key, X-Tenant-ID`.

---

## Errors and status codes

Native endpoints return:

```json
{ "error": "human readable message", "retry_after_seconds": 35, "reasons": {"provider-name": "cooling down"}, "attempts": [ ... ] }
```

`retry_after_seconds`, `reasons` and `attempts` appear only when relevant. When `retry_after_seconds` is present, the standard `Retry-After` header is set too.

OpenAI-compatible endpoints return the OpenAI shape, with routing details in an extra `airouter` object:

```json
{ "error": { "message": "...", "type": "providers_busy", "code": 503, "param": null },
  "airouter": { "retry_after_seconds": 35, "reasons": { ... } } }
```

| Code | `type` (OpenAI shape) | Meaning | Retry? |
|------|------|---------|--------|
| 400 | `bad_request` / `consent_required` | Your request is invalid, every provider rejected it, or cloning without `consent=true`. | No, fix it |
| 401 | `unauthorized` | Missing or invalid key. | No |
| 403 | `forbidden` | Tenant disabled, admin API disabled, or voice cloning needs your own ElevenLabs key. | No |
| 404 | `not_found` / `model_not_found` / `voice_not_found` | Unknown id/model/voice (also: an id owned by another tenant). A wrong voice never pauses the provider. | No |
| 429 | `rate_limit` | **Your tenant's** daily request cap is used up. `Retry-After` set. | After reset |
| 499 | `client_closed_request` | You closed the connection. | n/a |
| 502 | `upstream_error` | Providers were tried and all failed. `attempts` lists why (`status`, `kind`). Same for speech, transcription and embeddings. | Yes, soon |
| 503 | `providers_busy` / `no_providers` | Every provider is limited or cooling down (`Retry-After`, `reasons`), or none are configured for the category. | Yes, after `Retry-After` |
| 504 | `timeout` | The whole request timed out (default 2 min). | Yes |

`attempt.kind` values: `auth`, `rate_limit`, `daily_limit`, `billing`, `bad_request`, `timeout`, `transient`, `bad_response`.

---

# Native API

## POST `/v1/generate`

Generate text (or JSON). Tries providers in priority order and fails over automatically.

**Auth:** tenant key. **Max body:** 2 MB.

### Request body (JSON)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `prompt` | string | one of `prompt` / `messages` | The user prompt. |
| `messages` | array of `{role, content}` | one of `prompt` / `messages` | Multi-turn chat. `role` is `system`, `user` or `assistant`. |
| `system` | string | no | System prompt. |
| `json` | boolean | no | Ask the model for JSON and validate it. If a model returns junk, the router tries the next provider. The parsed value is returned in `json`. |
| `max_tokens` | integer | no | Max output tokens. |
| `temperature` | number | no | Sampling temperature. |
| `provider` | string | no | Pin one provider by name (see `provider list`). |
| `strategy` | `"priority"` \| `"spread"` | no | `priority` (default) tries the lowest priority number first. `spread` balances load. |

`tenant_id` and `category` are ignored (the server sets them).

### Query parameters

| Param | Description |
|-------|-------------|
| `format=text` | Return only the text as `text/plain` (same as header `Accept: text/plain`). |
| `max_wait=5s` | How long to wait for a briefly rate-limited provider before failing over. `5s` or `5` (seconds), max 2 minutes. Default: the server's setting (20s). Same on `/v1/speak` and `/v1/transcribe`. |

### Example

```bash
curl -s http://localhost:8080/v1/generate \
  -H "Authorization: Bearer air_..." -H "Content-Type: application/json" \
  -d '{"prompt":"Write one sentence about rain","system":"Be brief"}'
```

### Response `200`

```json
{
  "text": "Rain taps softly on the window.",
  "provider": "groq-openai/gpt-oss-120b",
  "model": "openai/gpt-oss-120b",
  "tokens": 42,
  "latency_ms": 610,
  "attempts": [
    { "provider": "gemini-gemini-3.5-flash-lite", "error": "HTTP 429: ...", "status": 429, "kind": "rate_limit" }
  ]
}
```

| Field | Description |
|-------|-------------|
| `text` | The answer. |
| `json` | Parsed JSON value. Only present when the request had `json: true`. |
| `provider` / `model` | Who answered. Also in headers `X-AIRouter-Provider`, `X-AIRouter-Model`. |
| `tokens` | Total tokens used. |
| `latency_ms` | Time spent on the successful call. |
| `attempts` | Providers that failed before this one (omitted when empty). |
| `own` | `true` when the tenant's **own** key answered (free under a pool-only cap). Also on speak and transcribe responses. |

### JSON mode example

```bash
curl -s http://localhost:8080/v1/generate -H "Authorization: Bearer air_..." \
  -d '{"system":"Reply as {\"emotion\":\"...\"} only.","prompt":"I failed my exam today","json":true}'
# => {"text":"{\"emotion\":\"sad\"}","json":{"emotion":"sad"}, ... }
```

---

## POST `/v1/speak` (text to speech)

**Auth:** tenant key. **Max body:** 64 KB. Only TTS-category providers are used.

### Request body (JSON)

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `text` | string | yes | Text to speak. |
| `voice` | string | no | Voice id **or name**. Defaults: `Kore` (Gemini), `tara` (Groq Orpheus English), `hassan` (Orpheus Arabic). ElevenLabs: any voice you can use, by id or name (case-insensitive), including your clones; empty = first ready-made voice. |
| `format` | `"wav"` \| `"mp3"` \| `"opus"` \| `"pcm"` | no | **Audio** format, default `wav`. ElevenLabs makes mp3/opus natively. For Groq and Gemini airouter converts the WAV with `ffmpeg`; if `ffmpeg` is missing you get WAV and the response `format` says `wav`. `opus` is mono Ogg/Opus, what **WhatsApp / Telegram voice notes** need. |
| `language` | string | no | Language hint, e.g. `en`, `ar`. |
| `provider` | string | no | Pin one TTS provider by name. |
| `model` | string | no | ElevenLabs only: model for this request, e.g. `eleven_flash_v2_5`. |
| `voice_settings` | object | no | ElevenLabs only: `{stability, similarity_boost, style, speed, use_speaker_boost}`. Leave a field out to keep the voice's saved value. |

### Query parameters

| Param | Description |
|-------|-------------|
| `format=json` | Return a JSON envelope with base64 audio instead of raw bytes (same as `Accept: application/json`). Not to be confused with the body field `format`. |

### Response `200`

**Default: raw audio bytes**, with `Content-Type: audio/wav` (or the matching type) and headers `X-AIRouter-Provider`, `X-AIRouter-Model`, `X-AIRouter-Voice`.

```bash
curl -s http://localhost:8080/v1/speak -H "Authorization: Bearer air_..." \
  -H "Content-Type: application/json" \
  -d '{"text":"Hello world","voice":"tara","format":"wav"}' --output out.wav
```

**With `?format=json`:**

```json
{
  "audio_base64": "UklGRi4A...",
  "content_type": "audio/wav",
  "format": "wav",
  "provider": "groq-canopylabs/orpheus-v1-english",
  "model": "canopylabs/orpheus-v1-english",
  "voice": "tara",
  "latency_ms": 940
}
```

---

## POST `/v1/transcribe` (speech to text)

**Auth:** tenant key. **Content type:** `multipart/form-data`. **Max upload:** 32 MB. Only STT-category providers are used.

### Form fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `file` | file | yes | Audio (mp3, wav, m4a, ogg, flac, webm). |
| `language` | string | no | ISO-639-1 hint, e.g. `en`. |
| `prompt` | string | no | Spelling/style hint (about 224 tokens max). |
| `provider` | string | no | Pin one STT provider by name. |

### Query parameters

| Param | Description |
|-------|-------------|
| `format=text` | Return only the text as `text/plain` (also `Accept: text/plain`). |

```bash
curl -s http://localhost:8080/v1/transcribe -H "Authorization: Bearer air_..." \
  -F "file=@recording.mp3" -F "language=en"
```

### Response `200`

```json
{ "text": "Hello, this is a test.", "provider": "groq-whisper-large-v3-turbo",
  "model": "whisper-large-v3-turbo", "language": "en", "latency_ms": 1200 }
```

---

## GET `/v1/usage`

Usage plus live provider status for the caller's tenant.

| Query | Description |
|-------|-------------|
| `day` | `YYYY-MM-DD`. Default is today (per provider timezone). |

### Response `200`

```json
{
  "usage": {
    "tenant": "acme", "requests_today": 12, "tokens_today": 3400, "tenant_daily_limit": 2000,
    "providers": [
      { "provider": "groq-openai/gpt-oss-120b", "model": "openai/gpt-oss-120b", "day": "2026-11-24",
        "tenant_requests": 12, "tenant_tokens": 3400, "tenant_errors": 0,
        "all_tenants_requests": 20, "limit_per_day": 1000 }
    ]
  },
  "providers": [ /* ProviderStatus objects, see Object reference */ ],
  "server_time": "2026-11-24T10:00:00Z"
}
```

## GET `/v1/whoami`

```json
{ "role": "tenant", "tenant": { "id": "acme", "name": "acme", "tz": "UTC", "daily_request_limit": 2000, "created_at": "2026-11-20T08:00:00Z" } }
```

`role` is `admin` when the admin token is used.

---

## GET `/v1/overview`

Everything an app's "AI" page needs about the caller's tenant **in one call**: the cap, today's usage per model, and the live state and reset time of every model that serves the tenant.

```json
{
  "tenant": { "id": "acme", "tz": "UTC", "daily_request_limit": 50, "pool_only_cap": true },
  "cap": {
    "limit": 50, "used": 12, "remaining": 38, "pool_only": true, "reached": false,
    "resets_in_seconds": 41200, "own_requests_today": 30, "pool_requests_today": 12
  },
  "usage": { "requests_today": 42, "tokens_today": 9100, "providers": [ /* per model */ ] },
  "own":    [ /* ProviderStatus: state, used_today, resets_in_seconds ... for the tenant's own models */ ],
  "shared": [ { "name": "groq-openai/gpt-oss-120b", "model": "...", "category": "text", "state": "ok", "resets_in_seconds": 0 } ],
  "server_time": "2026-11-24T10:00:00Z"
}
```

`cap.used` counts only shared-pool answers when `pool_only` is true. Shared models show name, state and reset time only: never keys, URLs or other tenants' numbers.

---

# Voices (ElevenLabs)

Add an ElevenLabs provider first (`category` `tts` for speech models, `stt` for `scribe_v1`):

```bash
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model eleven_multilingual_v2 -key-stdin
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model scribe_v1 -category stt -key-stdin
```

Tenants add their own the same way: `airouter me add -preset elevenlabs -model eleven_multilingual_v2 -key-stdin`.

## GET `/v1/voices`

Voices this tenant can speak with: ready-made and library voices, the voices of its own ElevenLabs account, and its own clones. **Other tenants' clones are never listed.**

| Query | Description |
|-------|-------------|
| `provider` | Only this ElevenLabs provider (by name). |
| `search` | Case-insensitive match on name, description, category or label (e.g. `female`). |

```json
{ "voices": [
  { "id": "21m00Tcm4TlvDq8ikWAM", "name": "Rachel", "category": "premade",
    "labels": { "gender": "female" }, "provider": "elevenlabs-eleven_multilingual_v2", "own": false, "cloned": false },
  { "id": "Xb7hH8MSUJpSbSDYk0k2", "name": "Ali", "category": "cloned", "provider": "carol-el", "own": true, "cloned": true }
] }
```

## POST `/v1/voices/clone`

Instant Voice Cloning from recordings. `multipart/form-data`, **max 60 MB** request.

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Max 100 characters. |
| `consent` | yes | Must be `true`: you have the speaker's permission or the rights to clone this voice. Without it: `400 consent_required`. |
| `files` | yes | 1 to 25 clean recordings (repeat the field). Max 12 MB each, 50 MB total. |
| `description` | no | |
| `labels` | no | JSON object of strings, e.g. `{"gender":"male"}`. |
| `remove_background_noise` | no | `true` runs noise removal. Can make clean recordings worse. |
| `provider` | no | Pin one ElevenLabs provider by name. |

```bash
curl -s http://localhost:8080/v1/voices/clone -H "Authorization: Bearer air_..." \
  -F name=Ali -F consent=true -F 'labels={"gender":"male"}' \
  -F files=@sample1.mp3 -F files=@sample2.wav
# 201 {"id":"...","name":"Ali","category":"cloned","provider":"...","own":true,"cloned":true}
```

Rules:

- A tenant clones inside its **own** ElevenLabs account. With no ElevenLabs key of its own it gets `403`, unless the operator enabled cloning on the shared account (`TenantPolicy.AllowSharedVoiceClone`). The `default` tenant (CLI and embedded use) is always allowed.
- At most 10 clones per tenant by default (`TenantPolicy.MaxClonedVoices`).
- A cloned voice only exists in the account that made it, so requests using it are **pinned to that provider**. They never fail over to another account.
- Any other ElevenLabs voice (an id, or a name such as `Rachel`) is only ever sent to **ElevenLabs** providers. Other TTS providers (Groq, Gemini) would refuse it with HTTP 400, and three such refusals in a row pause that provider for 24 hours for every tenant.
- Instant cloning needs an ElevenLabs plan that includes it. If ElevenLabs refuses, you get `400` with its reason, and the provider is not paused.

## POST `/v1/voices/{id}/share` (admin token)

By default a cloned voice is private. The operator can share a voice **it cloned** (as the `default` tenant) with every tenant that can use that ElevenLabs provider, for example a house voice for all customers:

```bash
curl -s http://localhost:8080/v1/voices/VOICE_ID/share -H "Authorization: Bearer $ADMIN" -d '{"shared":true}'
# {"id":"VOICE_ID","shared":true}      send {"shared":false} to make it private again (effective at once)
```

Tenants then see it in `GET /v1/voices` with `"shared": true` and can speak with it by id or name. Tenants' own clones can never be shared, a tenant cannot share or unshare anything (`401`), and the usage is billed to the ElevenLabs account the voice lives in.

## DELETE `/v1/voices/{id}`

Deletes a voice **this tenant cloned through airouter**. Another tenant's voice, or one airouter did not create, answers `404 voice_not_found`.

## Using a voice

```bash
curl -s http://localhost:8080/v1/speak -H "Authorization: Bearer air_..." \
  -d '{"text":"Hello!","voice":"Ali","format":"opus","voice_settings":{"stability":0.4,"similarity_boost":0.8,"speed":1.05}}' \
  --output note.ogg
```

`voice` may be an id or a name. A partial name works when it matches exactly one voice. A wrong voice answers `404 voice_not_found` and **does not pause** the provider. The OpenAI-style `/v1/audio/speech` takes the same `voice` value.

---

# OpenAI-compatible API

Point any OpenAI SDK at `http://HOST/v1` with your airouter tenant key.

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="air_...")
print(client.chat.completions.create(model="auto",
      messages=[{"role": "user", "content": "Hello"}]).choices[0].message.content)
```

```js
import OpenAI from "openai";
const client = new OpenAI({ baseURL: "http://localhost:8080/v1", apiKey: "air_..." });
const r = await client.chat.completions.create({ model: "auto", messages: [{ role: "user", content: "Hello" }] });
```

## POST `/v1/chat/completions`

**Max body:** 4 MB.

| Field | Type | Notes |
|-------|------|-------|
| `model` | string | `auto` (or empty, `airouter`, `default`) = router chooses and fails over. Or a provider name / exact model id to pin one. Unknown model gives `404 model_not_found`. |
| `messages` | array | `role`: `system`, `developer` (treated as system), `user`, `assistant`. `content`: a string, or an array of `{"type":"text","text":"..."}` parts. |
| `temperature` | number | |
| `max_tokens` / `max_completion_tokens` | integer | `max_completion_tokens` wins if both are set. |
| `response_format` | `{type}` | `json_object` or `json_schema` turns on airouter's JSON mode. |
| `stream` | boolean | **Buffered streaming.** The router needs the whole answer to fail over safely, so SSE chunks are sent after it finishes. Time-to-first-token does not improve. |

**Not supported (clear `400`):** `tools` / `functions` (tool calling) and image inputs.

### Response `200`

```json
{
  "id": "chatcmpl-air-1a2b3c...",
  "object": "chat.completion",
  "created": 1763980000,
  "model": "openai/gpt-oss-120b",
  "choices": [ { "index": 0, "finish_reason": "stop", "message": { "role": "assistant", "content": "Hi!" } } ],
  "usage": { "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 42 },
  "airouter": { "provider": "groq-openai/gpt-oss-120b", "latency_ms": 610, "attempts": [] }
}
```

`usage` only fills `total_tokens`. Headers `X-AIRouter-Provider` and `X-AIRouter-Model` are set.

## GET `/v1/models`

Lists `auto` plus every text model visible to the tenant:

```json
{ "object": "list", "data": [ { "id": "auto", "object": "model", "created": 1763980000, "owned_by": "airouter" },
                              { "id": "openai/gpt-oss-120b", "object": "model", "created": 1763980000, "owned_by": "groq" } ] }
```

## POST `/v1/embeddings`

OpenAI-compatible embeddings with failover. Needs at least one model with category `embed`:

```bash
echo "$GEMINI_API_KEY" | airouter provider add -preset gemini -model gemini-embedding-001 -category embed -key-stdin
# or: airouter bootstrap -category embed
```

| Field | Type | Notes |
|-------|------|-------|
| `input` | string or list of strings | Required. Up to 2000 texts per call (sent upstream in batches of 100 to the **same** provider). Empty or blank texts answer `400`. |
| `model` | string | `auto` (default), or a provider name / exact model id to pin one. |
| `dimensions` | integer | Exact vector size wanted. Sent upstream **and enforced**: a provider that answers with another size is skipped. |
| `encoding_format` | `"float"` | Other values answer `400`. |

```bash
curl -s http://localhost:8080/v1/embeddings -H "Authorization: Bearer air_..." \
  -d '{"model":"gemini-embedding-001","input":["first chunk","second chunk"],"dimensions":768}'
```

```json
{ "object": "list", "model": "gemini-embedding-001",
  "data": [ { "object": "embedding", "index": 0, "embedding": [0.012, -0.034, ...] }, ... ],
  "usage": { "prompt_tokens": 9, "total_tokens": 9 },
  "airouter": { "provider": "gemini-gemini-embedding-001", "dimensions": 768, "own": false, "latency_ms": 310, "attempts": [] } }
```

**For vector search:** vectors from different models, or of different sizes, cannot be compared, and failover can switch models. Pin one model (`model`), set `dimensions`, and store `model` and `airouter.dimensions` next to your index. `gemini-embedding-001` returns **3072** numbers by default, more than many vector indexes accept: ask for 768 or 1536. `usage` is an estimate (about 4 characters per token). In Go: `Router.Embed`, see [EMBEDDING.md](EMBEDDING.md#embeddings-for-vector-search).

## POST `/v1/audio/speech`

| Field | Description |
|-------|-------------|
| `model` | `auto`, a provider name, or an exact TTS model id. |
| `input` | Text to speak (required). |
| `voice` | Voice id. |
| `response_format` | `wav` (default), `mp3`, `pcm`. |

Returns raw audio bytes with the right `Content-Type`.

## POST `/v1/audio/transcriptions`

Same handler as `/v1/transcribe`: multipart fields `file`, `language`, `prompt`, `provider`. Returns the native transcript JSON. The OpenAI `model` field is ignored; use `provider` to pin one. Errors on this path use the native error shape.

---

# Me: tenant self-service (`/v1/me/*`)

Each tenant can manage its **own** models, keys and limits. Auth: the tenant key (an admin may add `X-Tenant-ID`). A tenant only ever sees or changes its own models (anything else answers `404`). Tenant models are tried **before** the shared pool.

| Method | Path | Body | Returns |
|--------|------|------|---------|
| GET | `/v1/me` | | `{role, tenant}` |
| PATCH | `/v1/me` | `{"name","tz"}` | updated tenant (quota is operator-only) |
| POST | `/v1/me/rotate-key` | | `{tenant, api_key, note}` (old key stops working) |
| GET | `/v1/me/usage?day=` | | `{tenant, usage, own, shared}` |
| POST | `/v1/me/bootstrap` | `{"keys":{"GROQ_API_KEY":"..."},"preset":"groq","overwrite":false}` | `{tenant, added, skipped}` |
| GET | `/v1/me/providers` | | `{tenant, own:[ProviderStatus], shared:[{name,model,category,state}]}` |
| POST | `/v1/me/providers` | ProviderInput (`api_key` required except Ollama) | `201` ProviderConfig |
| POST | `/v1/me/providers/{id}/enable` \| `disable` \| `reset` | none | `{id, ok:true}` |
| POST | `/v1/me/providers/{id}/key` | `{"api_key":"..."}` | `{id, ok:true}` |
| POST | `/v1/me/providers/{id}/limits` | any of `rpm, rpd, tpm, workers, priority` (ints) | `{id, ok:true}` |
| DELETE | `/v1/me/providers/{id}` | | `{deleted:true}` |
| GET | `/v1/me/catalog` | | `{providers:[{preset,kind,key_env,needs_key}], models:[CatalogModel], verified_on}` |
| POST | `/v1/me/catalog/add` | `{"preset":"groq","api_key":"gsk_...","models":["openai/gpt-oss-20b"]}` | `{added, skipped}` |
| GET | `/v1/me/ollama` | | `{available, models:[{name,parameter_size,size_gb}], error?}` |
| POST | `/v1/me/ollama/add` | `{"models":["gemma3:4b"]}` | `{added, skipped}` |
| POST | `/v1/me/import` | `{"providers":[ProviderInput,...]}` | `{added:[...], failed:{name:reason}}` |
| GET | `/v1/me/export` | | `{"providers":[...]}` (no keys) as a file download |

Rules the server enforces for tenants: they cannot use env-var keys (`api_key_env` is rejected); the upstream URL comes from the preset (custom URLs only with `serve -allow-tenant-urls`); at most 8 workers per model; at most 100 models per tenant (`-max-tenant-models`); `-block-tenant-ollama` keeps your Ollama private.

---

# Admin API

Auth: `Authorization: Bearer <AIROUTER_ADMIN_TOKEN>`.

| Method | Path | Body / query | Returns |
|--------|------|--------------|---------|
| GET | `/v1/providers` | | array of ProviderStatus (all tenants) |
| POST | `/v1/providers` | ProviderInput (include `id` to update) | `201` ProviderConfig (key never echoed) |
| DELETE | `/v1/providers/{id}` | | `{deleted:true}` |
| POST | `/v1/providers/{id}/enable` | | `{id, disabled:false}` |
| POST | `/v1/providers/{id}/disable` | | `{id, disabled:true}` |
| POST | `/v1/providers/{id}/reset` | | `{id, paused:false}` (lifts a pause) |
| POST | `/v1/providers/{id}/key` | `{"api_key":"..."}` | `{id, key_stored:true}` |
| POST | `/v1/bootstrap` | query: `category` (`text` default, `all`, or `text,tts,stt`), `preset`, `overwrite=1`, `store_keys=1` | `{added, skipped, verified, categories_used, available_categories, summary, next_step}` |
| GET | `/v1/tenants` | | array of Tenant |
| POST | `/v1/tenants` | `{"id":"acme","name":"Acme","tz":"UTC","daily_request_limit":2000,"pool_only_cap":false}` | `201 {tenant, api_key, note}` (key shown once) |
| PATCH | `/v1/tenants/{id}` | any of `{"daily_request_limit":500,"pool_only_cap":true,"disabled":false}`; omitted fields keep their value | the updated tenant (takes effect immediately) |
| POST | `/v1/tenants/{id}/rotate-key` | | `{tenant, api_key, note}` |

```bash
curl -X POST localhost:8080/v1/bootstrap -H "Authorization: Bearer $ADMIN"
curl -X POST "localhost:8080/v1/bootstrap?category=tts,stt&overwrite=1" -H "Authorization: Bearer $ADMIN"
curl -X POST localhost:8080/v1/tenants -H "Authorization: Bearer $ADMIN" \
  -d '{"id":"acme","daily_request_limit":2000}'
```

---

# Public endpoints

| Method | Path | Returns |
|--------|------|---------|
| GET | `/healthz` | `{"status":"ok"}` |
| GET | `/` | Built-in web UI (Chat, Usage, Connect your app, My models, Admin) |
| GET | `/v1/presets` | Map of preset name to connection defaults |
| GET | `/v1/presets/free-pool?category=text,tts` | `{models, verified_on, summary, categories}` |

Presets: `groq`, `gemini`, `gemma`, `gemini-tts`, `elevenlabs`, `openrouter`, `mistral`, `zai`, `sambanova`, `huggingface`, `ollama`, `ollama-remote`, `openai`.

---

# Object reference

### ProviderInput (add / import)

| Field | Type | Description |
|-------|------|-------------|
| `preset` | string | Fills defaults from the catalog. |
| `name` | string | Unique name. Default `preset-model`. |
| `kind` | `openai` \| `ollama` | |
| `base_url` | string | Needed without a preset. |
| `api_key` | string | Stored encrypted. Never returned. |
| `api_key_env` | string | Env var holding the key. Admin only. |
| `model` | string | **Required.** Exact id from the provider console. A wrong id gives `HTTP 404` and a 24h pause. |
| `category` | `text` \| `tts` \| `stt` | Default `text`. |
| `priority` | int | Lower is tried first. Default 100. |
| `rpm` / `rpd` / `tpm` | int | Local caps. `0` = none. |
| `ash` / `asd` | int | Audio seconds per hour / day (TTS and STT). |
| `workers` | int | Max concurrent requests. Default 2. |
| `reset_tz` | string | IANA zone of the daily reset. Default `UTC`. |
| `json_mode` | bool | `false` if the model rejects `response_format` (Gemma). |
| `system_role` | bool | `false` if the model rejects system messages (Gemma). |
| `used_today_hint` | int | Requests already used today (only when adding). |
| `tenant_id` | string | Admin only. Restrict to one tenant. Empty = shared. |

### ProviderStatus

`id, name, tenant_id, base_url, workers, kind, model, preset, category, priority, state, used_today, tokens_today, audio_seconds_today, rpd, rpm, tpm, ash, asd, requests_last_minute, reset_tz, resets_in_seconds, has_key, limits_source, key_source, key_hint, upstream, remaining_today`

- `state`: `ok`, `disabled`, `no api key`, `cooling down`, `daily request limit reached`, `daily audio seconds limit reached`, or `paused: <reason>`.
- `limits_source`: `reported` (the provider told us its limits) or `config`.
- `key_source`: `env`, `db`, `none`, `not needed`. `key_hint` is a masked fingerprint like `…a1b2`.

### Tenant

`{ id, name, tz, daily_request_limit (0 = unlimited), pool_only_cap, disabled, created_at }`

**`pool_only_cap`**: when `true` the daily cap counts only answers from the **shared pool**. Requests served by the tenant's own keys are free and keep working after the cap is reached. Applies to text, speech and transcription alike. (A model the operator restricted to one tenant with `provider add -tenant` also counts as that tenant's own.) If the tenant's own models are all busy, the request spills into the shared pool and then counts.

### What happens on a provider failure

| Upstream result | Reaction |
|-----------------|----------|
| 401, 403, 404 | Pause 24h (bad key / model id), try next |
| 402 | Pause 1h (out of credits) |
| 429 with daily hint | Pause until the provider's local midnight |
| other 429 | Cool down for `Retry-After` / `retryDelay` (30s if none) |
| 400, 413, 422 | No penalty (usually your request). If every provider rejects it, you get `400` |
| 408, 5xx, timeout, network | Exponential backoff 20s, 40s, ... up to 5 min |
| 200 but empty / invalid JSON (JSON mode) | Try next provider, no penalty |

Each provider is tried at most once per request. Timeouts: 60s per upstream call, 2 min per request, and up to 20s wait for a briefly-limited provider.
