# airouter

> One Go binary + library that routes your prompt across many free-tier AI
> providers (Groq, Gemini, Gemma, OpenRouter, Z.AI GLM, Mistral, SambaNova,
> Ollama, any OpenAI-compatible URL) and **auto-switches when one hits its
> limit**. Supports **text chat**, **text-to-speech (TTS)**, and
> **speech-to-text (STT)** — same failover behaviour for all three.

`airouter` is for: hobby projects, prototypes, internal tools, hackathons, and
anyone who wants free LLM access without managing API keys in every script.
Configure it once with the providers you have free keys for, then send
prompts from the CLI, the HTTP server, or any Go program. The router picks the
best available provider, sends the request, and on a 429 / 5xx / bad-key error
fails over to the next one before your code even notices.

- **Three request types, one router:** text chat (`r.Generate`), text-to-speech
  (`r.Speak`), speech-to-text (`r.Transcribe`). Each one filters the provider
  pool by category so a TTS call never hits a chat model and vice versa.
- **Per-provider per-model limits** with RPM / RPD / TPM for chat and TTS, plus
  ASH (audio seconds per hour) and ASD (audio seconds per day) for TTS and STT.
  Every model entry is independent (Gemini 3.5 Flash-Lite and Gemma 4 31B on
  the same Gemini key are two entries with two quotas).
- **Per-provider timezone-aware daily resets.** Gemini's quota resets at PT
  midnight; the router knows that and unpauses the provider at the right time.
- **Three flavors of cooldown:** seconds (a transient 429), until-local-midnight
  (a daily cap), 24 hours (a bad key or model id).
- **Pauses persist** to SQLite — restarts and separate CLI invocations respect
  the same state.
- **Tenants** with hashed API keys and optional daily request caps. Provider
  keys are shared across tenants; a provider can also be restricted to one.
- **JSON mode** that asks the provider for JSON and validates the response,
  failing over if a model returns junk. Models without JSON mode or a system
  role (Gemma) are handled transparently.
- **Curated free-pool catalog** with three categories: `text` (18 models),
  `tts` (6 models), `stt` (3 models). One `bootstrap` command seeds whichever
  categories you want.
- **Same shape, three faces:** the CLI, the HTTP server, and the Go library
  all expose the same Request/Response types.

## Quick start

```bash
git clone https://github.com/mrmhthegreat/airouter && cd airouter
make build                                        # CGO + mattn/go-sqlite3 (fastest)
# or: CGO_ENABLED=0 make build                   # pure-Go SQLite (no C compiler needed)

export GROQ_API_KEY=gsk_...  GEMINI_API_KEY=...  # set the ones you have
./bin/airouter bootstrap                          # import curated free-pool entries
./bin/airouter provider list                      # live status

./bin/airouter ask "Write one sentence about rain"
./bin/airouter ask -json -system 'Reply as {"emotion":"..."} only.' "I failed my exam today"
echo "long text..." | ./bin/airouter ask -envelope # full Response JSON

./bin/airouter usage                              # today's usage per provider
```

The `bootstrap` command imports every entry from the curated free-pool catalog
whose env var is set. Entries without a key (Z.AI, OpenRouter, etc.) are
skipped with a clear message — set their env vars and re-run.

## Output format

The CLI auto-detects stdout:

- **stdout is a terminal** → text on stdout + a short `[provider / model, N tokens, Xms]` footer on stderr.
- **stdout is piped** → raw text only, no footer. So `airouter ask ... | grep` works cleanly.
- `-json` → ask the model for JSON and pretty-print only that JSON.
- `-envelope` → full Response as JSON (text + provider + model + tokens + attempts).
- `-text` → force raw text with no footer.

The HTTP server mirrors this with `?format=text` and the `Accept: text/plain`
header.

## Free-tier catalog

`airouter free-pool` lists the curated catalog. Every entry has a `Category`,
real model ID, RPM/RPD/TPM from the provider's console, and a `VerifiedOn`
date. Limits move — re-check before relying on them.

The catalog groups models by category so the router can pick providers that
match the request shape. Three categories are supported today:

| Category | What it is                                                | Routed?                              |
| -------- | --------------------------------------------------------- | ------------------------------------ |
| `text`   | Chat, OpenAI-compatible `/chat/completions`               | Yes — `r.Generate` / `airouter ask` / `POST /v1/generate` |
| `tts`    | Text-to-speech                                            | Yes — `r.Speak` / `airouter speak` / `POST /v1/speak`     |
| `stt`    | Speech-to-text                                            | Yes — `r.Transcribe` / `airouter transcribe` / `POST /v1/transcribe` (Groq Whisper only — Gemini Transcribe is in catalog but not yet routed) |

### Text models (default bootstrap pool)

```
$ airouter free-pool -category text
CATEGORY  PRESET      MODEL                          RPM  RPD    TPM     PRIO  VERIFIED    NOTES
text      groq        openai/gpt-oss-120b            30   1000   8000    10    2026-11-16  OpenAI's 120B open-weight flagship.
text      groq        openai/gpt-oss-20b             30   1000   8000    15    2026-11-16  Smaller/faster GPT-OSS variant.
text      groq        qwen/qwen3.8-27b               30   1000   8000    20    2026-11-16  Qwen 3.8 27B — strong multilingual + tool use.
text      gemini      gemini-3.5-flash-lite          15   500    250000  25    2026-11-16  Best Gemini free chat model.
text      gemini      gemini-3.1-flash-lite          15   500    250000  30    2026-11-16  Previous-gen Flash-Lite.
text      gemini      gemini-3.8-flash               5    20     250000  35    2026-11-16  Newest Flash. Very tight free-tier RPD (20/day).
text      gemini      gemini-3.6-flash               5    20     250000  40    2026-11-16  Computer-use support.
text      gemini      gemini-3-flash                 5    20     250000  45    2026-11-16  Gemini 3 Flash (preview).
text      gemini      gemini-2.5-flash-lite          10   20     250000  50    2026-11-16  Auto-routes to 3.5-flash-lite Dec 1 2026.
text      gemini      gemini-2.5-flash               5    20     250000  55    2026-11-16  DEPRECATED — auto-routes to 3.5-flash-lite Dec 1.
text      gemini      gemini-4-hydrogen              5    20     250000  60    2026-11-16  Gemini 4 Hydrogen (preview).
text      gemma       gemma-4-31b-it                 30   14400  16000   65    2026-11-16  Gemma 4 31B. No system role, no JSON mode — handled.
text      gemma       gemma-4-26b-it                 30   14400  16000   70    2026-11-16  Smaller Gemma 4.
text      openrouter  meta-llama/llama-3.3-70b:free  20   50     —       75    2026-11-16  RPD rises to 1000 after any payment to OpenRouter.
text      openrouter  google/gemma-3-27b-it:free     20   50     —       80    2026-11-16  Free Gemma via OpenRouter.
text      zai         glm-4.7-flash                  —    —      —       55    2026-11-20  Free. 1 concurrent request, no token cap.
text      zai         glm-4.5-flash                  —    —      —       78    2026-11-20  Free. Z.AI announced retirement.
text      zai         glm-4.6v-flash                 —    —      —       82    2026-11-20  Free. Multimodal model, used for text.
text      sambanova   Meta-Llama-3.1-8B-Instruct     20   —      —       90    2026-11-16  0 = unknown — airouter will discover from 429s.
text      ollama      gemma3:4b                      —    —      —       95    2026-11-16  Local fallback. Requires `ollama serve`.
```

### TTS, STT and other categories (visible but not routed)

```
$ airouter free-pool -category tts,stt
CATEGORY  PRESET      MODEL                            RPM  RPD  TPM    VERIFIED    NOTES
tts       gemini-tts  gemini-3.8-flash-tts             3    10   10000  2026-11-16  Flagship Gemini TTS. 30 prebuilt voices.
tts       gemini-tts  gemini-3.8-flash-lite-tts        3    10   10000  2026-11-16  Fast/cost-efficient Gemini TTS.
tts       gemini-tts  gemini-3.1-flash-tts             3    10   10000  2026-11-16  Legacy Gemini TTS preview.
tts       gemini-tts  gemini-2.5-flash-preview-tts     3    10   10000  2026-11-16  2.5 Flash TTS preview.
tts       groq        canopylabs/orpheus-v1-english    10   100  1200   2026-11-16  Orpheus English TTS via Groq.
tts       groq        canopylabs/orpheus-arabic-saudi  10   100  1200   2026-11-16  Orpheus Arabic (Saudi) TTS via Groq.
stt       gemini-tts  gemini-3.5-transcribe            3    25   10000  2026-11-16  Gemini 3.5 Transcribe (low-latency STT).
stt       groq        whisper-large-v3-turbo           20   —    2000   2026-11-16  Whisper Large v3 Turbo via Groq.
stt       groq        whisper-large-v3                 20   —    2000   2026-11-16  Whisper Large v3 via Groq.
```

Sources:
- Gemini: https://aistudio.google.com/docs/rate-limits and per-model pages
- Gemini TTS: https://aistudio.google.com/docs/speech-generation
- Gemini Transcribe: https://aistudio.google.com/docs/transcribe
- Groq: https://console.groq.com/docs/rate-limits and per-model pages

### Importing categories

```bash
airouter bootstrap                           # default = text only (chat-capable models)
airouter bootstrap -category text            # explicit text only
airouter bootstrap -category tts,stt         # only TTS + STT (will not be routable today)
airouter bootstrap -category all             # everything (visible but most won't work yet)
airouter bootstrap -preset groq              # only Groq entries (respects the category filter)
airouter bootstrap -overwrite                # update existing same-name entries in place
```

HTTP equivalents:

```bash
curl -X POST localhost:8080/v1/bootstrap -H "Authorization: Bearer $ADMIN"             # default = text
curl -X POST "localhost:8080/v1/bootstrap?category=all" -H "Authorization: Bearer $ADMIN"
curl -X POST "localhost:8080/v1/bootstrap?category=tts,stt&overwrite=1" -H "Authorization: Bearer $ADMIN"

curl localhost:8080/v1/presets/free-pool                       # full catalog + summary
curl "localhost:8080/v1/presets/free-pool?category=tts,stt"     # filtered
```

### Adding a provider not in the catalog

```bash
./bin/airouter provider add -preset groq -model openai/gpt-oss-120b -rpm 30 -rpd 1000 -tpm 8000 -priority 10
./bin/airouter provider add -preset ollama -model llama3:8b -priority 90
./bin/airouter provider add -preset gemini -model gemini-3.5-flash-lite -rpd 500 -used-today 496   # 496 already used today
./bin/airouter provider add -preset groq -model canopylabs/orpheus-v1-english -category tts -rpm 10 -rpd 100
./bin/airouter provider reset 3      # lift a pause after fixing a key
```

Model ids must match the provider's console exactly. A wrong id is reported as
`HTTP 404` and the provider is paused for 24h.

## Text-to-speech (TTS) and speech-to-text (STT)

airouter routes TTS and STT the same way it routes chat: pick the best
available provider, send the request, fail over on errors. Provider selection
filters by category so a TTS call only ever hits TTS-capable providers
(`canopylabs/orpheus-*` on Groq, `gemini-*-tts` on Gemini), and an STT call
only ever hits STT providers (`whisper-large-v3*` on Groq,
`gemini-3.5-transcribe` on Gemini).

**Audio-second tracking** is built in: for TTS and STT calls, airouter estimates
the audio duration and tracks it against the provider's ASH (audio seconds per
hour) and ASD (audio seconds per day) limits, with the same 90% safety margin
used for RPM/RPD/TPM. When a provider's audio limit is hit it is skipped until
the window resets (1 hour for ASH, provider-local midnight for ASD) — the same
failover behaviour as chat limits.

### CLI

```bash
# Bootstrap TTS + STT providers (in addition to the default text pool)
./bin/airouter bootstrap -category text,tts,stt

# Text → audio. Use -o to write to a file (recommended — most terminals
# can't play raw WAV bytes from stdout).
./bin/airouter speak -o hello.wav "Hello world"
./bin/airouter speak -o hello.wav -voice Kore -provider gemini-tts-gemini-3.8-flash-tts "Hello from Gemini"
./bin/airouter speak -o arabic.wav -voice hassan "مرحبا بالعالم"

# Audio → text. Pass an audio file; prints the transcribed text to stdout.
./bin/airouter transcribe recording.mp3
./bin/airouter transcribe -language en -provider groq-whisper-large-v3-turbo recording.wav
./bin/airouter transcribe -provider gemini-tts-gemini-3.5-transcribe recording.wav

# See live audio-second usage in the provider list. The AUDIO SEC column shows
# used-today / ASH / ASD for TTS/STT providers.
./bin/airouter provider list
# ID  CAT   NAME                          MODEL                   STATE  USED/RPD  ...  AUDIO SEC (ASH/ASD)
# 21  stt   groq-whisper-large-v3-turbo  whisper-large-v3-turbo  ok     0/-       ...  240/7200/28800
```

### HTTP

```bash
# Text → audio. Default response is raw audio bytes with Content-Type: audio/wav.
# Pass ?format=json (or Accept: application/json) for a JSON envelope with
# base64-encoded audio (useful for browser clients).
curl -s localhost:8080/v1/speak -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"text":"Hello world","voice":"tara","format":"wav"}' \
  --output out.wav

# Audio → text. Multipart form with a "file" field.
curl -s localhost:8080/v1/transcribe -H "Authorization: Bearer $KEY" \
  -F "file=@recording.mp3" -F "language=en"

# Plain text response (instead of JSON) with ?format=text — useful for piping.
curl -s "localhost:8080/v1/transcribe?format=text" -H "Authorization: Bearer $KEY" \
  -F "file=@recording.mp3" | tr '[:lower:]' '[:upper:]'
```

### Go library

```go
// Text → audio
resp, err := r.Speak(ctx, types.SpeakRequest{
    Text:   "Hello world",
    Voice:  "Kore",       // optional; defaults: Kore for Gemini, tara for Groq Orpheus English
    Format: "wav",        // optional; wav is the default
})
// resp.Audio is raw WAV bytes; resp.ContentType is "audio/wav"

// Audio → text
resp, err := r.Transcribe(ctx, types.TranscribeRequest{
    Audio:    audioBytes,       // raw bytes from a file / microphone / etc.
    Filename: "recording.mp3",  // used for MIME-type detection + WAV duration parsing
    Language: "en",             // optional hint
})
// resp.Text is the transcribed text
```

### Provider support

| Category | Groq | Gemini |
| -------- | ---- | ------ |
| TTS      | `canopylabs/orpheus-v1-english`, `canopylabs/orpheus-arabic-saudi` via `/audio/speech` (OpenAI-compatible). Returns WAV. | `gemini-3.8-flash-tts`, `gemini-3.8-flash-lite-tts`, `gemini-3.1-flash-tts`, `gemini-2.5-flash-preview-tts` via native `:generateContent` with audio modality. Returns WAV. 30 prebuilt voices (Kore, Puck, Charon, Aoede, Fenrir, Leda, Orus, Zephyr, ...). |
| STT      | `whisper-large-v3`, `whisper-large-v3-turbo` via `/audio/transcriptions` (OpenAI-compatible, multipart). Returns text. **ASH 7200 / ASD 28800** (2 hours per hour, 8 hours per day) tracked by airouter. | `gemini-3.5-transcribe` via two-step Files API upload + `/v1beta/interactions` POST. Returns text. Word timestamps + speaker diarization available via `Language` and `Prompt` fields. airouter uploads the file, polls until ACTIVE, transcribes, then deletes the file to free storage. |

Default voices when the caller does not pass `-voice`:
- Gemini TTS → `Kore`
- Groq Orpheus English → `tara`
- Groq Orpheus Arabic (Saudi) → `hassan`

### Audio-second tracking details

- **TTS**: output audio duration estimated from input text length
  (~15 chars/sec, the average English speaking rate). Always at least 1 second.
- **STT**: input audio duration parsed from the WAV header when possible
  (exact duration from sample rate + byte rate + data size). For non-WAV
  formats (MP3, M4A, OGG, FLAC, WebM), falls back to a byte-count heuristic
  (~16 KB/sec, assuming 128 kbps compressed audio). Always at least 1 second.
- The 90% safety margin in `acquire()` absorbs estimation error: a provider
  with `ASH=7200` will be skipped at ~6480 seconds of estimated audio used.
- ASH uses a rolling 1-hour window (same shape as RPM's 60-second window).
- ASD resets at the provider's local midnight (same as RPD).
- Gemini does not publish ASH/ASD limits for `gemini-3.5-transcribe` —
  the catalog leaves them at 0, so only RPM/RPD/TPM constrain it.

### Sources

- Groq TTS: https://console.groq.com/docs/text-to-speech
- Groq STT: https://console.groq.com/docs/speech-to-text
- Gemini TTS: https://aistudio.google.com/docs/speech-generation
- Gemini STT: https://aistudio.google.com/docs/transcribe

## HTTP server, web UI and OpenAI-compatible API

```bash
export AIROUTER_ADMIN_TOKEN=change-me
export AIROUTER_SECRET=$(openssl rand -hex 32)      # encrypts provider keys in the DB (keep it safe)
./bin/airouter tenant add acme -limit 2000          # prints the tenant API key once
./bin/airouter serve -addr :8080                    # add -cors https://myapp.com for browser apps
```

- **Web UI:** open `http://localhost:8080/`, paste a tenant key (or the admin
  token). Tabs: **Chat** (shows which provider answered and any failovers),
  **Usage** (live provider status, daily usage, what the provider itself
  reports as remaining), **Connect your app** (base URL + copy-paste
  curl/Python/JS snippets), **Admin** (retry/disable providers, set provider
  keys, create tenants, rotate keys).
- **OpenAI-compatible:** base URL `http://localhost:8080/v1`, API key = your
  airouter tenant key, `model: "auto"`.

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8080/v1", api_key="air_...")
print(client.chat.completions.create(model="auto",
      messages=[{"role": "user", "content": "Hello"}]).choices[0].message.content)
```

Supported: `POST /v1/chat/completions` (incl. `stream: true`, `response_format`
json, `max_tokens`, `temperature`), `GET /v1/models`, `POST /v1/audio/speech`,
`POST /v1/audio/transcriptions`. `model` is `auto` (router chooses and fails
over) or a provider name / exact model id to pin one. **Not supported yet:**
tool/function calling and image input (clear `400`). `stream: true` is
*buffered streaming*: the router needs the whole answer to fail over safely, so
chunks are sent after it completes.

Native API (richer: attempts, provider, latency):

```bash
curl -s localhost:8080/v1/generate -H "Authorization: Bearer air_..." -d '{"prompt":"Say hi","system":"Be brief"}'
curl -s localhost:8080/v1/usage    -H "Authorization: Bearer air_..."      # usage + live provider status
curl -s localhost:8080/v1/whoami   -H "Authorization: Bearer air_..."
```

### Status codes you will see

| Code | Meaning | Retry? |
|------|---------|--------|
| 400 | Your request is invalid (empty messages, every provider rejected it as bad) | No, fix it |
| 401 | Missing/invalid key | No |
| 403 | Tenant disabled | No |
| 429 | **Your tenant's** daily cap is used up (`Retry-After`) | After reset |
| 502 | Providers were tried and all failed (`attempts` lists why) | Yes, soon |
| 503 | Every provider is limited / cooling down right now (`Retry-After`, `reasons`) | Yes, after `Retry-After` |
| 504 | The whole request timed out (default 2 min) | Yes |

OpenAI SDKs retry 429/503 automatically, so they behave well against airouter.

Admin endpoints (`Authorization: Bearer <admin token>`):
`GET/POST /v1/providers`, `DELETE /v1/providers/{id}`,
`POST /v1/providers/{id}/enable|disable|reset`,
`POST /v1/providers/{id}/key` (`{"api_key": "..."}`),
`GET/POST /v1/tenants`, `POST /v1/tenants/{id}/rotate-key`,
`POST /v1/bootstrap?store_keys=1`.

Public endpoints: `GET /` (UI), `GET /healthz`, `GET /v1/presets`, `GET /v1/presets/free-pool`.

## Every user manages their own models, keys and limits

Each tenant (user/app) can choose its own models, paste its own API key and set
its own limits. Their models are private (nobody else sees or uses them) and
are tried **before** the shared pool the operator configured. Three ways, all
scoped to one tenant id:

**Web UI** — sign in with your tenant key, open **My models**: pick a provider
from the list, paste your key, tick models (limits are pre-filled from the
catalog), or choose models installed on the server's **Ollama**, or import a
**JSON file**. Per model: Limits, Change key, Retry now, Turn off, Remove.
The operator (admin token) can pick any tenant at the top of that tab.

**HTTP** (`Authorization: Bearer <tenant key>`; an admin may add `X-Tenant-ID: acme`):

```bash
GET    /v1/me/catalog                  # providers + models you can pick, marks what you already added
POST   /v1/me/catalog/add              {"preset":"groq","api_key":"gsk_...","models":["openai/gpt-oss-20b"]}
GET    /v1/me/ollama                   # models installed on the server's Ollama
POST   /v1/me/ollama/add               {"models":["gemma3:4b"]}
POST   /v1/me/providers                {"preset":"gemini","model":"gemini-2.5-flash","api_key":"...","rpm":10}
GET    /v1/me/providers                # your models + live status (+ read-only list of shared ones)
POST   /v1/me/providers/{id}/limits    {"rpm":30,"rpd":1000,"tpm":6000,"workers":2,"priority":10}
POST   /v1/me/providers/{id}/key       {"api_key":"..."}
POST   /v1/me/providers/{id}/enable|disable|reset
DELETE /v1/me/providers/{id}
GET    /v1/me                          who am I;  PATCH /v1/me {"name","tz"}   (quota is operator-only)
POST   /v1/me/rotate-key               new key, old one stops working
GET    /v1/me/usage                    my usage + model status
POST   /v1/me/bootstrap                {"keys":{"GROQ_API_KEY":"..."}}  add the free-pool models with MY keys
POST   /v1/me/import                   {"providers":[ ... ]}      # a JSON file
GET    /v1/me/export                   # your models as importable JSON (no keys)
```

**CLI for a user, over HTTP** (needs only the server URL and your own key, so it works from any machine; set `AIROUTER_URL` and `AIROUTER_KEY`):

```bash
airouter me catalog                                     # what you can pick
echo "$GROQ_API_KEY" | airouter me pick -preset groq -key-stdin openai/gpt-oss-20b openai/gpt-oss-120b
airouter me add -preset gemini -model gemini-2.5-flash -key-stdin -rpm 10
airouter me list
airouter me limits 7 -rpm 20 -rpd 500
airouter me ollama           # installed on the server;  `me ollama add gemma3:4b` to use one
airouter me import my-models.json     |   airouter me export
airouter me usage     |    airouter me update -name Alice -tz Asia/Karachi    |    airouter me rotate-key
```

**CLI for the operator** (run on the server; on the server; `-tenant` limits every command to that user's models):

```bash
airouter bootstrap -tenant acme                       # free-pool models, keys taken from env and stored for acme
airouter provider add -tenant acme -preset groq -model openai/gpt-oss-20b -key-stdin
airouter provider list -tenant acme
airouter provider limits 7 -tenant acme -rpm 20 -rpd 500
airouter provider set-key 7 -tenant acme
airouter provider import -tenant acme my-models.json
airouter ollama list                                  # what is installed
airouter ollama add -tenant acme gemma3:4b
```

JSON file format (`preset` + `model` + your own `api_key`):

```json
{"providers": [
  {"preset": "groq",   "model": "openai/gpt-oss-20b",  "api_key": "gsk_...", "rpm": 30, "rpd": 1000},
  {"preset": "gemini", "model": "gemini-2.5-flash",    "api_key": "AIza...", "rpm": 10},
  {"preset": "ollama", "model": "gemma3:4b"}
]}
```

Because tenants are not admins, the server enforces: a tenant only ever sees or
changes **its own** models (anything else answers 404); tenants **cannot use
env-var keys** (`api_key_env` is rejected, so they cannot spend the server's
keys); the upstream URL comes from the preset, so a tenant **cannot aim the
server at arbitrary hosts** (turn that on only if you trust all tenants:
`serve -allow-tenant-urls`); workers are capped at 8 and models per tenant at
100 (`-max-tenant-models`); `-block-tenant-ollama` keeps your Ollama private.
Their provider keys are encrypted at rest like everything else.

## Keys and secrets: what is stored how

| Secret | Storage | Why |
|--------|---------|-----|
| Tenant API keys (`air_...`) | **SHA-256 hash only** in the DB | Shown once; a DB leak reveals no working key. Rotate with `airouter tenant rotate NAME`. |
| Provider keys (Groq, Gemini, ...) | **AES-256-GCM encrypted** in the DB | They must be sent upstream, so they cannot be hashed. The DB alone is useless without the master secret. |
| Master secret | `$AIROUTER_SECRET`, else a `0600` file `<db>.key` | Back it up **separately** from the DB. Lose it and stored provider keys cannot be decrypted (re-enter them). |

No JSON key files are needed any more:

```bash
airouter provider add -preset groq -model openai/gpt-oss-120b -key-stdin   # paste key; stored encrypted
airouter provider set-key 3                                                 # replace a key later
airouter bootstrap -store-keys        # copy keys from env vars into the DB, then unset the env vars
```

`provider list` shows where each key comes from (`db` / `env`) without ever
printing it. Databases from older versions with plaintext keys are encrypted
automatically on first open.

## Do providers tell us their limits through the API?

Partly, and airouter uses whatever they send:

- **Groq** sends `x-ratelimit-limit/remaining/reset-requests` (per day) and
  `-tokens` (per minute) on every response, and `retry-after` on 429s.
  airouter reads them: when `remaining` hits 0 it pauses that provider
  *before* the next request earns a 429, and shows "provider reports N left"
  in Usage. ([Groq docs](https://console.groq.com/docs/rate-limits))
- **Gemini** (OpenAI-compatible endpoint) sends no rate-limit headers. Its 429
  body contains a `retryDelay` (e.g. `"34s"`) and a quota name containing
  `PerDay`; airouter uses both. Free-tier numbers still come from the catalog
  (`rpm`/`rpd`), so keep them correct.
- **Others** (Mistral, OpenRouter, ...) use the same
  `x-ratelimit-*` header family or `Retry-After`; airouter parses these
  best-effort. Header formats differ per provider and change over time, so
  treat provider-reported numbers as a bonus, not the source of truth.

What airouter does with the numbers (so the system adapts instead of trusting
a static catalog):

- **Limits are learned.** The provider's reported daily request limit and token
  limit replace the configured `rpd`/`tpm` (Usage shows "limit from provider").
  A different tier or a changed free tier needs no config edit.
- **The daily counter is corrected from the provider.** If another app or
  process uses the same key, airouter's count jumps to the real remaining value
  and the correction is saved in the DB.
- **"0 left" pauses until the provider's own reset time,** not a guess.
- **Tokens left this minute** are checked before each call; the provider is
  skipped until its reported token reset.
- **Gemini** has none of this, so airouter reads the structured 429 body:
  a `...PerDay...` quota id parks it until Pacific midnight (daylight saving
  handled), a `...PerMinute...` one waits the given `retryDelay`.
- Groq's plain `x-ratelimit-*-requests` headers are treated as per-day (hosts in
  `router.DailyRequestHostHints`); other hosts' plain names as per-minute, and
  explicit `-day` / `-minute` header names are always trusted.

Nobody exposes a universal "what are my limits" endpoint for free keys, so
the configured `rpm/rpd/tpm` values remain the starting point until a provider
reports its own.

## As a Go package (in your own projects)

```bash
go get github.com/mrmhthegreat/airouter@latest
# not published yet? in your project's go.mod:
#   require github.com/mrmhthegreat/airouter v0.0.0
#   replace github.com/mrmhthegreat/airouter => ../airouter
```

```go
import "github.com/mrmhthegreat/airouter"

c, err := airouter.Open(airouter.Options{DB: "ai.db"})   // keys encrypted at rest
if err != nil { log.Fatal(err) }
defer c.Close()

// once: put your keys in the DB (encrypted). Afterwards no env vars are needed.
c.AddFreePool(ctx, map[string]string{"GROQ_API_KEY": "gsk_...", "GEMINI_API_KEY": "..."})

text, err := c.Chat(ctx, "Write one sentence about rain")   // auto-failover

// full control
resp, err := c.Ask(ctx, types.Request{System: "Be brief", Prompt: "hi", JSON: true})

// expose OpenAI API + web UI from your own server
http.ListenAndServe(":8080", c.Handler(os.Getenv("AIROUTER_ADMIN_TOKEN")))
```

`airouter.NewClient(store)` takes any `store.Store` (memory, or your own
Postgres) if you do not want SQLite. All `router.Router` methods
(`Generate`, `Speak`, `Transcribe`, `Status`, `Usage`, ...) are on the client.
Share the same DB file with the CLI and server: usage and pauses are shared.
See [`examples/embedded.go`](examples/embedded.go).

## How switching works

Providers are tried by `priority` (lower first; a tenant's own providers go
before shared ones). A provider is skipped when its daily cap is reached
(paused to its local midnight), its per-minute window is full (90% safety
margin), it is busy (`workers`), or it is paused/cooling down.

What happens when a provider fails:

| Upstream result | Reaction |
|-----------------|----------|
| 401, 403, 404 | Pause 24h (bad key / model id), then try the next provider |
| 402 | Pause 1h (out of credits) |
| 429 with daily hint (`PerDay`, "daily", Retry-After > 5 min, or headers say 0 left for hours) | Pause until the provider's local midnight |
| other 429 | Cool down for `Retry-After` / Gemini `retryDelay` (30s if none) |
| 400, 413, 422 | **No penalty** (it is usually your request). If every provider rejects it you get a 400. 3 config-looking 400s in a row (bad model / json mode) pause 24h; any success resets this |
| 408, 5xx, timeout, network error | Exponential backoff 20s, 40s, ... up to 5 min; reset on success |
| 200 but empty / invalid JSON when JSON was requested | Try the next provider, no penalty |

**When limits are hit it does not hammer or loop forever:** each provider is
tried at most once per request. If everything is only briefly limited,
`Generate` waits up to `MaxWait` (20s) for the soonest provider; otherwise it
returns `503` with `Retry-After`. A single call is bounded by
`AttemptTimeout` (60s) and the whole request by `RequestTimeout` (2 min).

## Docker

```bash
docker build -t airouter .
docker run -p 8080:8080 -v $(pwd)/data:/data -e AIROUTER_SECRET=$(openssl rand -hex 32) airouter
```

## Which providers are in the catalog

Only providers you can try **without a payment card**: Groq, Gemini and Gemma (Google AI Studio),
OpenRouter `:free` models, Z.AI GLM Flash, SambaNova, HuggingFace, and local Ollama.
Providers that need a card or prepaid balance before they work (DeepSeek, Together, Fireworks,
DeepInfra, Novita) are not presets, and Cerebras and Cohere were removed. You can still add one yourself as a custom provider if you
run the server with `-allow-tenant-urls`, or as the operator.

## Project layout

```
airouter.go           one-import facade for your own projects (airouter.Open)
cmd/airouter/         CLI entry point and per-command files (ask.go, serve.go, …)
pkg/types/            public data shapes (ProviderConfig, Tenant, Request, …)
pkg/store/            Store interface, MemoryStore, SQLStore
pkg/preset/           built-in provider catalog + curated free-pool model list
pkg/router/           core router: provider selection, failover, error classification
pkg/server/           HTTP handler: native + OpenAI-compatible API, embedded web UI (web/index.html)
pkg/sqlopen/          opens SQLite (cgo or pure Go) with encrypted provider keys
examples/             runnable examples: shell, Go, Python (chat, speak, transcribe, manage), JavaScript
Dockerfile, .github/  container image and CI (tests on both SQLite drivers, cross-compiled releases)
```

Module path is `github.com/mrmhthegreat/airouter`. Publishing under another
GitHub name? Run `make rename MODULE=github.com/yourname/airouter` once.

## Limits and honest notes

- Run **one server process**. Counters live in memory and are loaded from the
  DB at start and at each day change; a second process (or a CLI call while
  the server runs) will not see the other's usage until then.
- Free-tier limits change. The catalog has a `VerifiedOn` date on every entry
  and `CatalogVerifiedOn()` returns the newest one. If it is older than ~30
  days, re-check the provider's console.
- Chat has no true streaming (buffered SSE only) and no tool calling yet.
  Audio-second counters (TTS/STT) reset on restart.
- The tenant daily cap is counted in the providers' day buckets, so around
  midnight it is approximate.
- Don't create extra accounts/projects to multiply a free quota — that breaks
  every provider's terms. Add more *models* or *providers* instead.

## License

MIT — see [LICENSE](LICENSE).
