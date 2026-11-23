# Python examples — full guides

Each file in this folder is a **complete walkthrough** with detailed comments
explaining every step, every argument, and every error path. Open them as
guides, not just recipes.

## Files

| File | What it does | Deps |
|------|--------------|------|
| [chat.py](chat.py) | Chat completions: auto routing, pinned model, JSON mode, multi-turn, error handling | `openai` |
| [speak.py](speak.py) | Text → audio: voices, formats (wav/mp3/pcm), language hints, provider pinning | `requests` |
| [transcribe.py](transcribe.py) | Audio → text: language hints, spelling/style prompts, provider pinning | `requests` |
| [manage.py](manage.py) | Tenant self-service: 13-step walkthrough (whoami, list, add, pick, limits, set-key, toggle, import/export, usage, profile, rotate-key) | `requests` |

## Setup (one time)

```bash
pip install openai requests

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
python chat.py
python speak.py
python transcribe.py          # after speak.py creates out.wav
python manage.py              # also reads $GROQ_API_KEY
```

## What each file teaches

### chat.py

- How the OpenAI Python SDK maps to airouter (just `base_url` + `api_key`)
- "auto" routing — what it does, when to use it
- Pinning one specific model — when you want determinism
- JSON mode — `response_format: {type: "json_object"}` + why you MUST pair it with a system prompt
- Multi-turn conversations — airouter is stateless; you maintain the history
- The airouter response envelope — provider, attempts, latency
- Error handling — `RateLimitError`, `APIStatusError`, `APIError`

### speak.py

- Voice ids (provider-specific: Kore for Gemini, tara for Groq Orpheus English, …)
- Output formats (wav / mp3 / pcm) — when to use which
- Language hint for non-English text (ISO 639-1 codes)
- Pinning one TTS provider
- Reading the response headers (`X-Airouter-Provider`, `X-Airouter-Latency`)

### transcribe.py

- Audio file types supported (wav, mp3, m4a, flac, ogg, webm)
- Language hint — when to force one vs let the model auto-detect
- Spelling / style prompt — names, jargon, punctuation (NOT a system prompt!)
- Pinning one STT provider
- The response envelope

### manage.py

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
