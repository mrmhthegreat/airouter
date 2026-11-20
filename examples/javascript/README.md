# JavaScript examples — full guides

Each file in this folder is a **complete walkthrough** with detailed comments
explaining every step, every argument, and every error path. Open them as
guides, not just recipes.

## Files

| File | What it does | Deps |
|------|--------------|------|
| [chat.mjs](chat.mjs) | Chat completions: auto routing, pinned model, JSON mode, multi-turn, error handling | `openai` |
| [speak.mjs](speak.mjs) | Text → audio: voices, formats (wav/mp3/pcm), language hints, provider pinning | none (Node 18+) |
| [transcribe.mjs](transcribe.mjs) | Audio → text: language hints, spelling/style prompts, provider pinning | none (Node 18+) |
| [manage.mjs](manage.mjs) | Tenant self-service: 13-step walkthrough | none (Node 18+) |

## Setup (one time)

```bash
# Only chat.mjs needs the openai npm package:
npm install openai

# Start an airouter server (from the repo root):
go build -o airouter ./cmd/airouter
./airouter serve -addr :8080 &
export AIROUTER_URL=http://localhost:8080
export AIROUTER_KEY=$(cat .airouter-key)   # printed once on first run

# Add at least one provider key:
export GROQ_API_KEY=gsk_...                 # from https://console.groq.com

# Pick some models into your tenant scope:
echo "$GROQ_API_KEY" | ./airouter me pick -preset groq -key-stdin openai/gpt-oss-120b openai/gpt-oss-20b
./airouter me list
```

## Run any example

```bash
node chat.mjs
node speak.mjs
node transcribe.mjs out.wav       # after speak.mjs creates out.wav
node manage.mjs                   # also reads $GROQ_API_KEY
```

## What each file teaches

### chat.mjs

- How the `openai` npm package maps to airouter (just `baseURL` + `apiKey`)
- "auto" routing — what it does, when to use it
- Pinning one specific model — when you want determinism
- JSON mode — `response_format: {type: "json_object"}` + why you MUST pair it with a system prompt
- Multi-turn conversations — airouter is stateless; you maintain the history
- The airouter response envelope — provider, attempts, latency
- Error handling — `RateLimitError`, `APIError`, `BadRequestError`

### speak.mjs

- Voice ids (provider-specific: Kore for Gemini, tara for Groq Orpheus English, …)
- Output formats (wav / mp3 / pcm) — when to use which
- Language hint for non-English text (ISO 639-1 codes)
- Pinning one TTS provider
- Reading the response headers (`X-Airouter-Provider`, `X-Airouter-Latency`)
- Zero dependencies — uses built-in `fetch` + `Buffer` + `fs/promises`

### transcribe.mjs

- Audio file types supported (wav, mp3, m4a, flac, ogg, webm)
- Language hint — when to force one vs let the model auto-detect
- Spelling / style prompt — names, jargon, punctuation (NOT a system prompt!)
- Pinning one STT provider
- The response envelope
- Zero dependencies — uses built-in `FormData` + `Blob` + `fetch`

### manage.mjs

A 13-step tenant self-service walkthrough:

1. whoami — confirm your identity
2. List my models (own + shared)
3. Browse the catalog (presets + free-pool models you can pick from)
4. Add a model with my own API key (stored encrypted, never echoed back)
5. Pick several models with one key (bulk add from catalog)
6. Update my model's limits (rpm / rpd / tpm / workers / priority)
7. Replace a model's API key
8. Toggle / reset / remove a model
9. Import / export providers as JSON
10. My usage report
11. Update my profile (name, timezone)
12. Rotate my API key (old one stops working immediately)
13. Cleanup

Re-running is safe — it deletes the model it adds.

## What to read next

- [../README.md](../README.md) — the airouter project README
- [../CLICMD_REFACTOR.md](../CLICMD_REFACTOR.md) — the clicmd package reference
- `airouter free-pool -category text|tts|stt` — full model catalog
- `airouter me catalog` — what YOU can pick from
