# airouter

> One Go binary + library that routes your prompt across many free-tier AI
> providers (Groq, Gemini, Gemma, OpenRouter, Cerebras, Mistral, SambaNova,
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
git clone https://github.com/you/airouter && cd airouter
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
whose env var is set. Entries without a key (Cerebras, OpenRouter, etc.) are
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
text      cerebras    gpt-oss-120b                   5    —      30000   85    2026-11-16  Free trial only — 5 RPM.
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

## HTTP server

```bash
export AIROUTER_ADMIN_TOKEN=change-me
./bin/airouter tenant add acme -limit 2000 -tz Asia/Karachi    # prints the tenant API key once
./bin/airouter serve -addr :8080
```

```bash
# Generate (JSON in, JSON out by default; text with ?format=text)
curl -s localhost:8080/v1/generate -H "Authorization: Bearer air_..." \
  -d '{"prompt":"Say hi","system":"Be brief","json":false}'

# Plain text out
curl -s "localhost:8080/v1/generate?format=text" -H "Authorization: Bearer air_..." -d '{"prompt":"Say hi"}'

# Usage + live provider status
curl -s localhost:8080/v1/usage -H "Authorization: Bearer air_..."

# Bootstrap the DB over HTTP (admin token)
curl -s -X POST localhost:8080/v1/bootstrap -H "Authorization: Bearer change-me"
```

Request body fields: `prompt` or `messages`, `system`, `json`, `max_tokens`,
`temperature`, `provider` (pin one by name), `strategy` (`priority` | `spread`).

Error codes: `401` bad key, `429` tenant cap (with `retry_after_seconds`),
`503` all providers limited (with per-provider `reasons` + `retry_after_seconds`),
`502` every provider failed (with `attempts`), `400` bad request body.

Admin endpoints (`Authorization: Bearer <admin token>`):
`GET/POST /v1/providers`, `DELETE /v1/providers/{id}`,
`POST /v1/providers/{id}/enable|disable|reset`,
`GET/POST /v1/tenants`, `POST /v1/bootstrap`.

Public endpoints: `GET /healthz`, `GET /v1/presets`, `GET /v1/presets/free-pool`.

## As a Go package

```go
import (
    "database/sql"
    _ "github.com/mattn/go-sqlite3"

    "github.com/you/airouter/pkg/router"
    "github.com/you/airouter/pkg/server"
    "github.com/you/airouter/pkg/store"
    "github.com/you/airouter/pkg/types"
)

db, _ := sql.Open("sqlite3", "file:airouter.db?_busy_timeout=5000&_journal_mode=WAL")
st, _ := store.NewSQLStore(db)              // or store.NewMemoryStore()
r, _  := router.New(router.Options{Store: st})

// Seed from the free-pool catalog (skips providers whose env var is unset)
added, skipped, _ := r.BootstrapProviders(ctx, false, "")

resp, err := r.Generate(ctx, types.Request{Prompt: "hello", JSON: false})
// resp.Text, resp.JSON, resp.Provider, resp.Model, resp.Tokens, resp.Attempts

http.ListenAndServe(":8080", server.NewHandler(r, adminToken))
```

See [`examples/embedded.go`](examples/embedded.go) for a complete runnable
example, and [`examples/http-client.sh`](examples/http-client.sh) for the
curl version.

## How switching works

Providers are tried by `priority` (lower first; a tenant's own providers go
before shared ones). A provider is skipped when:

- its daily request cap is reached → paused until the provider's local midnight
- its per-minute request/token window is full at the 90% safety margin → wait
  for the oldest hit to age out of the 60s window
- it is busy (concurrent requests ≥ `workers`) → 250ms retry
- it is paused (long cooldown) → skip until the deadline

A `429` whose body mentions "per day" / "daily" pauses the provider until its
local midnight. Other `429`s cool it for 30s or the upstream `Retry-After`.
`401/403/404` pause 24h (bad key or model id). Repeated `400` (3 in a row)
pauses 24h. If everything is only briefly limited, `Generate` waits up to
`MaxWait` (20s); otherwise it returns `ExhaustedError` with the retry time.

## Project layout

```
cmd/airouter/         CLI entry point and per-command files (ask.go, serve.go, …)
pkg/types/            public data shapes (ProviderConfig, Tenant, Request, …)
pkg/store/            Store interface, MemoryStore, SQLStore
pkg/preset/           built-in provider catalog + curated free-pool model list
pkg/router/           core router: provider selection, failover, error classification
pkg/server/           HTTP handler
examples/             runnable usage examples (shell + Go)
```

Module path is `github.com/you/airouter` — replace `you` with your username
before publishing.

## Limits and honest notes

- Run **one server process**. Counters live in memory and are loaded from the
  DB at start and at each day change; a second process (or a CLI call while
  the server runs) will not see the other's usage until then.
- Free-tier limits change. The catalog has a `VerifiedOn` date on every entry
  and `CatalogVerifiedOn()` returns the newest one. If it is older than ~30
  days, re-check the provider's console.
- Text only, no streaming. API keys given with `-key` are stored in plaintext
  in the DB; prefer `-key-env` (the default for presets).
- The tenant daily cap is counted in the providers' day buckets, so around
  midnight it is approximate.
- Don't create extra accounts/projects to multiply a free quota — that breaks
  every provider's terms. Add more *models* or *providers* instead.

## License

MIT — see [LICENSE](LICENSE).
