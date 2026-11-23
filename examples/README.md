# airouter examples — full guides

Quick runnable examples for talking to an airouter server (or embedding
airouter as a Go library). Every example is a **complete walkthrough** with
detailed comments — not a "minimal" demo.

## Languages

| Language   | Files                                                | What you learn                            |
| ---------- | ---------------------------------------------------- | ----------------------------------------- |
| Go         | `embedded.go`, `embedded_full/main.go`, `http-client.go` | All three layers: in-process, HTTP mount, CLI |
| Python     | `python/{chat,speak,transcribe,manage}.py`           | OpenAI SDK + raw HTTP, with error handling |
| JavaScript | `javascript/{chat,speak,transcribe,manage}.mjs`      | Same in Node 18+ (zero dependencies for non-chat) |
| Shell      | `cli-quickstart.sh`, `http-client.sh`                | End-to-end bash walk-through + curl recipes |

Each example file is heavily commented — open it as a guide, not just a
recipe.

---

## Choose what you need

### "I want to call airouter from Go"

| Want | Read |
|------|------|
| The short tour (in-process Chat + Ask) | [embedded.go](embedded.go) |
| The full tour (in-process + HTTP mount + clicmd, all three layers) | [embedded_full/main.go](embedded_full/main.go) |
| Talk to a running server over HTTP from Go | [http-client.go](http-client.go) |

### "I want to call airouter from Python"

| Want | Read |
|------|------|
| Chat (auto routing, pinned model, JSON mode, multi-turn, error handling) | [python/chat.py](python/chat.py) |
| Text → audio (voices, formats, languages, pinning) | [python/speak.py](python/speak.py) |
| Audio → text (language hint, style prompt, pinning) | [python/transcribe.py](python/transcribe.py) |
| Tenant self-service (add/list/remove/limits/keys/import/usage/rotate) | [python/manage.py](python/manage.py) |

### "I want to call airouter from JavaScript"

| Want | Read |
|------|------|
| Chat (same as Python, but in Node) | [javascript/chat.mjs](javascript/chat.mjs) |
| Text → audio | [javascript/speak.mjs](javascript/speak.mjs) |
| Audio → text | [javascript/transcribe.mjs](javascript/transcribe.mjs) |
| Tenant self-service | [javascript/manage.mjs](javascript/manage.mjs) |

### "I want to use the CLI"

| Want | Read |
|------|------|
| End-to-end bash walk-through (server + tenant + ask + curl) | [cli-quickstart.sh](cli-quickstart.sh) |
| curl recipes for every HTTP endpoint | [http-client.sh](http-client.sh) |
| The full CLI subcommand reference | [../CLICMD_REFACTOR.md](../CLICMD_REFACTOR.md) or `airouter --help` |

---

## Setup (one time)

### 1. Build and start a server

```bash
cd ..                 # back to the airouter repo root
go build -o airouter ./cmd/airouter
./airouter serve -addr :8080
```

On first run the server prints a default-tenant API key on stderr and saves
it to `./.airouter-key` (mode 0600):

```bash
export AIROUTER_KEY=$(cat .airouter-key)
export AIROUTER_URL=http://localhost:8080
```

### 2. Add at least one provider key

```bash
export GROQ_API_KEY=gsk_...           # from https://console.groq.com
# optionally also:
export GEMINI_API_KEY=...             # from https://aistudio.google.com
export OPENROUTER_API_KEY=sk-or-...   # from https://openrouter.ai
export ZAI_API_KEY=...                # from https://z.ai
```

### 3. Import the catalog into your own tenant scope

Pick models from the catalog with your own Groq key:

```bash
echo "$GROQ_API_KEY" | ./airouter me pick -preset groq -key-stdin openai/gpt-oss-120b openai/gpt-oss-20b qwen/qwen3.8-27b
./airouter me list
```

From here, any example below works.

---

## Each example in detail

### Go: `embedded.go` — the short tour (in-process only)

Embeds airouter as a Go library:

1. `airouter.Open(...)` → returns `*Client`
2. `c.AddFreePool(ctx, keys)` → imports env keys into the encrypted store
3. `c.Status(ctx, "")` → live provider list
4. `c.Chat(ctx, prompt)` → convenience text answer
5. `c.Ask(ctx, req)` → full Request/Response shape with JSON mode

```bash
GROQ_API_KEY=gsk_... go run ./examples
```

[Source](embedded.go) · about 130 lines, fully commented.

### Go: `embedded_full/main.go` — the full tour (three layers)

The complete embedder walkthrough:

1. **Layer 1 — in-process**: `c.Status`, `c.BootstrapProvidersOpts`, `c.SaveProvider`, `c.Generate`
2. **Layer 2 — HTTP mount**: `c.Handler(adminToken)` mounted at `/airouter/` inside a mux that also has custom routes
3. **Layer 3 — CLI**: explains how to dispatch to `clicmd.Dispatch(args)` from the same binary

Then runs an HTTP server on :8000 with both the custom routes (`/healthz`, `/ask`) and the mounted airouter handler (`/airouter/...`).

```bash
GROQ_API_KEY=gsk_... AIROUTER_ADMIN_TOKEN=change-me go run ./examples/embedded_full
```

Then in another terminal:

```bash
curl localhost:8000/healthz
curl -X POST localhost:8000/ask -d '{"prompt":"hello"}'
curl localhost:8000/airouter/healthz
curl localhost:8000/airouter/v1/presets/free-pool
curl localhost:8000/airouter/v1/providers -H 'Authorization: Bearer change-me'
```

[Source](embedded_full/main.go) · about 200 lines, fully commented.

### Go: `http-client.go` — standalone HTTP client

Standalone Go program that talks to a running airouter server using a tenant
key. Stdlib only — copy into any module and `go run` it. Demonstrates
`POST /v1/chat/completions`, `POST /v1/audio/speech`, `GET /v1/me/providers`,
`POST /v1/me/providers`.

```bash
AIROUTER_KEY=$(cat ../.airouter-key) AIROUTER_URL=http://localhost:8080 go run http-client.go
```

[Source](http-client.go).

### Python: `chat.py` — chat with full error handling

Uses the `openai` Python SDK. Walks through:

1. Setup (env vars, client construction)
2. "auto" routing — let airouter pick the best free provider
3. Pinning one specific model
4. JSON mode (`response_format: json_object`)
5. Multi-turn conversations (message history)
6. Reading the airouter-specific response envelope
7. Error handling (rate limits, all-providers-busy)

```bash
cd python && pip install openai
AIROUTER_KEY=$AIROUTER_KEY AIROUTER_URL=$AIROUTER_URL python chat.py
```

[Source](python/chat.py).

### Python: `speak.py` — text-to-speech

Walks through voices, output formats (wav/mp3/pcm), language hints, provider
pinning, and reading the response headers.

```bash
cd python && pip install requests
python speak.py
ffplay -autoexit out.wav
```

[Source](python/speak.py).

### Python: `transcribe.py` — speech-to-text

Walks through language hints, spelling/style prompts, provider pinning, and
the response envelope. Pair with `speak.py` to make a file first.

```bash
python speak.py && AIROUTER_STT_IN=out.wav python transcribe.py
```

[Source](python/transcribe.py).

### Python: `manage.py` — tenant self-service

13-step walkthrough of everything a tenant can do: whoami, list, browse
catalog, add, pick (bulk), update limits, set-key, toggle/reset, import/export,
usage, update profile, rotate key, cleanup.

```bash
export GROQ_API_KEY=gsk_...
python manage.py
```

[Source](python/manage.py).

### JavaScript: same four files, same walkthroughs

`javascript/chat.mjs`, `javascript/speak.mjs`, `javascript/transcribe.mjs`,
`javascript/manage.mjs` — same shape as the Python versions, but in Node 18+
with zero dependencies (except `openai` for `chat.mjs`).

```bash
cd javascript && npm install openai   # only for chat.mjs
node chat.mjs
node speak.mjs
node transcribe.mjs out.wav
node manage.mjs
```

### Shell: `cli-quickstart.sh` — end-to-end bash walk-through

Builds the binary, starts a server on `:18080` with a temp DB, grabs the
printed API key, and walks through the full tenant self-service flow: `me
whoami`, `me pick`, `me list`, `me usage`, plus one `ask` and one
`curl /v1/chat/completions`. Tears the server down on exit.

```bash
bash cli-quickstart.sh
```

[Source](cli-quickstart.sh).

### Shell: `http-client.sh` — curl recipes

Plain `curl` recipes for every HTTP endpoint, grouped:

- `POST /v1/chat/completions` (OpenAI-compatible)
- `POST /v1/generate` (native, richer response)
- `POST /v1/generate` with `json:true` (validated JSON mode)
- `POST /v1/speak` (TTS, raw WAV bytes back)
- `POST /v1/transcribe` (multipart audio upload, STT)
- `GET /v1/me` (who am I + my settings)
- `GET /v1/me/providers` (list MY providers only)
- `POST /v1/me/bootstrap?category=text&store_keys=1`
- `GET /v1/me/usage`
- `GET /v1/presets` and `GET /v1/presets/free-pool` (public catalog)

```bash
AIROUTER_KEY=$(cat ../.airouter-key) bash http-client.sh
```

[Source](http-client.sh).

---

## What to read next

- Project README: [../README.md](../README.md) — endpoint reference,
  configuration, CLI flags, the tenant-first model.
- The clicmd refactor: [../CLICMD_REFACTOR.md](../CLICMD_REFACTOR.md) — what
  changed in this version of airouter and why.
- Provider catalog: [../pkg/preset/preset.go](../pkg/preset/preset.go) —
  connection defaults and the curated free-tier model list.
- The `/v1/me/*` server code: [../pkg/server/me.go](../pkg/server/me.go).
