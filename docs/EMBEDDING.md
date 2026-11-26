# Using airouter as a Go library (no HTTP, no admin token)

```go
c, err := airouter.Open(airouter.Options{DB: "app.db"})   // one SQLite file
defer c.Close()
resp, err := c.Generate(ctx, types.Request{TenantID: "shop42", Prompt: "Hello"})
```

`airouter.Open` needs **no HTTP server and no admin token**. The web admin and the HTTP API are optional: they exist only if you call `c.Handler(adminToken)` and mount it. An empty admin token simply disables the admin endpoints. If you want the smallest dependency set, import `pkg/router`, `pkg/store` and `pkg/sqlopen` directly: `pkg/router` does not import the HTTP server.

A complete, compiling example (tenant with a pool-only cap, an ElevenLabs voice as a WhatsApp voice note, cloning, one-call status): [`examples/whatsapp_voice`](../examples/whatsapp_voice/main.go).

## Methods you will use

| Method | What it does |
|--------|--------------|
| `Generate(ctx, types.Request)` | Text (or JSON). `MaxWait` waits briefly for a rate-limited provider before failing over. `resp.Own` tells you whether the tenant's own key answered. |
| `Speak(ctx, types.SpeakRequest)` | Text to speech. `Format` `wav`/`mp3`/`opus`/`pcm`. `Voice` by id or name, `VoiceSettings`, `Model` for ElevenLabs. |
| `Transcribe(ctx, types.TranscribeRequest)` | Speech to text: Groq Whisper, Gemini, ElevenLabs Scribe. |
| `Embed(ctx, types.EmbedRequest)` | Text to vectors with failover. See [Embeddings](#embeddings-for-vector-search): set `Dimensions` and pin a model. |
| `CreateTenant(ctx, types.Tenant{DailyRequestLimit, PoolOnlyCap})` | Makes a tenant and returns its API key once. |
| `UpdateTenantCap(ctx, id, dailyLimit, poolOnly, disabled)` | Change a cap later. Takes effect immediately. |
| `TenantOverview(ctx, id)` | **One call** for a status page: tenant, cap (`used`, `remaining`, `reached`, `resets_in_seconds`), usage per model, live state and reset time of own and shared models. |
| `ListVoices` / `CloneVoice` / `DeleteVoice` / `ShareVoice` | ElevenLabs voices, scoped per tenant; `ShareVoice(ctx, "", id, true)` lets the operator share a voice it cloned with every tenant. See [ELEVENLABS.md](ELEVENLABS.md). |
| `ListTenantProviders`, `AddTenantProvider`, `UpdateTenantLimits` | A tenant's own models, keys and limits. |

## Pool-only caps

`Tenant.PoolOnlyCap = true` makes `DailyRequestLimit` count **only answers from the shared pool**. Requests answered by the tenant's own keys are free and keep working after the cap is reached. It applies to text, speech and transcription. When the cap is reached and the tenant has no own model for that kind of request, the call fails with `*types.TenantLimitError` (`errors.As`, it has `RetryAfter`). If the tenant's own models are all busy, the request spills into the shared pool and counts.

## Embeddings (for vector search)

```go
resp, err := c.Embed(ctx, types.EmbedRequest{
    TenantID:   "shop42",
    Texts:      chunks,         // up to 2000 per call, no empty strings
    Provider:   "gemini-gemini-embedding-001", // pin ONE model (provider name)
    Dimensions: 768,            // enforce the size of your index
})
// store resp.Model and resp.Dimensions with the index
```

Add a model with category `embed`: `airouter bootstrap -category embed`, or `airouter provider add -preset gemini -model gemini-embedding-001 -category embed -key-stdin`. The built-in catalog entry's limits (60/min, 1000/day) are a **conservative guess, not a verified quote**: check Google AI Studio and adjust with `provider add -rpm ... -rpd ...`. Over HTTP use `POST /v1/embeddings` (OpenAI format, see [API.md](API.md#post-v1embeddings)).

**Read this before building search on it.** Vectors from different models, or of different sizes, cannot be compared with each other. airouter's failover can switch models when one hits its daily limit, and then new queries would be matched against an index built with another model, silently giving wrong results. Protect yourself:

1. Configure **one** embedding model, or pin it with `Provider` (or `model` over HTTP).
2. Always set `Dimensions`. A provider that answers with another size is skipped, and you get a clear error instead of bad vectors.
3. Store `resp.Model` and `resp.Dimensions` with the index, and re-embed everything if you ever change the model.
4. `gemini-embedding-001` returns **3072** numbers by default (Google recommends 3072, 1536 or 768). Many vector indexes cannot index 3072: ask for 768 or 1536. The `dimensions` parameter on Gemini's OpenAI-compatible endpoint was **not confirmed** here; if it is ignored, the size check rejects the answer and you will see it immediately.

How a call behaves: a long list is sent in batches of 100 to the **same** provider; if any batch fails the whole call moves to the next provider, so one call never mixes models. Token usage is estimated (about 4 characters per token) so per-minute token limits work. Empty texts answer `ErrBadRequest` before any provider is called. When every provider fails you get `*types.BadRequestError` (your input was rejected) or `*types.AllFailedError` (outage), with `status` and `kind` per attempt, instead of a misleading "limited, retry in 1h".

### What counts toward a tenant's cap

| Cap | Counts |
|-----|--------|
| Normal (`PoolOnlyCap` off) | **Every attempt**, including failed ones and the tenant's own models. |
| Pool-only (`PoolOnlyCap` on) | Only requests the **shared pool served successfully**. Failed attempts and own-key requests are free, so an outage or a failover does not use up a customer's cap. |

Trade-off to know: under a pool-only cap a request that fails on **every** provider costs the tenant nothing, so a tenant sending garbage that providers reject is not slowed by the cap (it still uses up per-provider minute and daily limits for everybody). Rate-limit abusive tenants in your app. `TenantOverview` reports `own_requests_today` and `pool_requests_today` as requests actually served.

## Several processes on one SQLite file

Measured, not guessed: the test `TestMultiProcessSharedSQLite` starts 8 operating-system processes with 8 threads each (9,600 requests) on one database file. Result: **no "database is locked" errors and no lost updates**; usage counters in the file added up exactly (three runs in a row, SQLite WAL mode and a 5 second busy timeout). So you do **not** need Postgres for two services sharing a file.

What is shared and what is not:

| | Shared across processes? |
|--|--------------------------|
| Tenants, providers, keys, voices | Yes (database, re-read every 5 seconds) |
| Daily usage counters and tenant caps | Yes (database) |
| Pauses after a provider error or daily limit | Yes (database) |
| **Per-minute limit (RPM/TPM) and worker slots** | **No, in each process's memory** |

Because of the last row, N processes together can send up to N times a provider's per-minute limit (measured: a 10 requests/minute provider received 27 requests from 3 processes). The provider then answers 429 and airouter fails over and cools down, so requests still succeed, but you waste calls. Two fixes:

1. Give each process its own budget: set the provider's `rpm` to the real limit divided by the number of processes.
2. Or run **one** owner process (the HTTP server) and let the other service call it over HTTP.

Move to a server database only if you need many machines, not for a few processes on one.

## Notes

- The pure-Go SQLite build (`CGO_ENABLED=0`) uses the same SQL; the multi-process test above was run with the default (CGO) driver.
- `Workers` for a tenant's own model is capped at 8.
