# airouter CLI reference

```bash
make build                      # CGO + mattn/go-sqlite3 (fastest)
CGO_ENABLED=0 make build        # pure-Go SQLite (no C compiler needed)
./bin/airouter <command> [flags]
```

Or install from source: `go install github.com/mrmhthegreat/airouter/cmd/airouter@latest`.
Pre-built binaries for Linux, macOS and Windows (amd64 + arm64): `make release` (output in `dist/`).

## Contents

[Global options](#global-options) · [ask](#ask) · [speak](#speak) · [transcribe](#transcribe) · [voice](#voice) · [serve](#serve) · [bootstrap](#bootstrap) · [provider](#provider) · [tenant](#tenant) · [usage](#usage) · [presets](#presets) · [free-pool](#free-pool) · [ollama](#ollama) · [me](#me) · [Environment variables](#environment-variables) · [Exit codes](#exit-codes)

## Global options

For `ask`, `speak` and `transcribe` put flags **before** the text or file: `airouter ask -json "prompt"`. For commands that take an id or a name (`tenant`, `provider`, `voice`, `me`) flags work before **or after** it: `airouter tenant add acme -limit 2000` and `airouter tenant add -limit 2000 acme` both work.

| Option | Default | Description |
|--------|---------|-------------|
| `-db FILE` | `$AIROUTER_DB` or `./airouter.db` | SQLite database. Every command accepts it (except `me`, which uses HTTP). |
| `-h` | | Help for any command. |

The CLI, the HTTP server and the Go library share the same DB, so usage and pauses are shared. Do not run a CLI request while the server runs if you need exact counters (see "Limits" in the README).

Top-level help: `airouter help`.

---

## ask

Send a prompt. Reads from stdin if no prompt argument is given.

```
airouter ask [flags] PROMPT...
```

| Flag | Default | Description |
|------|---------|-------------|
| `-system TEXT` | | System prompt. |
| `-json` | false | Ask the model for JSON and print only that JSON (pretty-printed). |
| `-envelope` | false | Print the full response as JSON (`text, provider, model, tokens, latency_ms, attempts`). |
| `-text` | false | Raw text only, no footer. |
| `-provider NAME` | | Pin one provider by name. |
| `-strategy S` | `priority` | `priority` or `spread`. |
| `-max-tokens N` | 0 | Max output tokens. |
| `-tenant ID` | `default` | Use this tenant's quota and private models. |

Output: if stdout is a terminal you get the text plus a footer on stderr (`[provider / model, N tokens, Xms]`). If stdout is piped you get raw text only.

```bash
airouter ask "Write one sentence about rain"
airouter ask -system "Be terse." "What is 2+2?"
airouter ask -json -system 'Reply as {"emotion":"..."} only.' "I failed my exam today"
echo "long text..." | airouter ask -envelope
airouter ask -provider groq-openai/gpt-oss-120b "Hello"
```

## speak

Text to speech. Pass `-o FILE` to save (recommended: terminals cannot play raw WAV).

```
airouter speak [flags] TEXT
```

| Flag | Default | Description |
|------|---------|-------------|
| `-o FILE` | stdout | Write audio to this file. |
| `-voice V` | provider default | Voice id or **name**. `Kore` (Gemini), `tara` (Groq Orpheus English), `hassan` (Orpheus Arabic), or any ElevenLabs voice you can use, including your clones. |
| `-format F` | `wav` | `wav`, `mp3`, `opus` (mono Ogg/Opus for WhatsApp and Telegram voice notes) or `pcm`. ElevenLabs makes mp3/opus itself. For Groq and Gemini airouter converts with `ffmpeg`; without it you get WAV. |
| `-model M` | | ElevenLabs only: model for this request, e.g. `eleven_flash_v2_5`. |
| `-stability X` `-similarity X` `-style X` `-speed X` | | ElevenLabs only: voice tuning (stability, similarity, style 0..1; speed about 0.7..1.2). Omit to keep the voice's saved value. |
| `-max-wait D` | `0` | Wait up to this long (e.g. `5s`) for a briefly rate-limited provider. `0` = default. |
| `-language L` | | Language hint, e.g. `en`, `ar`. |
| `-provider NAME` | | Pin one TTS provider. |
| `-envelope` | false | Print metadata (provider, model, voice, size, latency) on stderr. |
| `-tenant ID` | `default` | |

Needs TTS models: `airouter bootstrap -category text,tts,stt`.

```bash
airouter speak -o hello.wav "Hello world"
airouter speak -o hello.wav -voice Kore -provider gemini-tts-gemini-3.8-flash-tts "Hello from Gemini"
airouter speak -o arabic.wav -voice hassan "مرحبا بالعالم"

# WhatsApp voice note (mono Ogg/Opus), ElevenLabs voice, tuned for this request
airouter speak -o note.ogg -format opus -voice Rachel -stability 0.4 -speed 1.05 "Your order is ready"
```

## voice

ElevenLabs voices: list them, clone one from recordings, delete one you cloned. Needs an ElevenLabs provider:

```bash
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model eleven_multilingual_v2 -key-stdin
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model scribe_v1 -category stt -key-stdin   # optional: speech to text
```

```
airouter voice list   [-search S] [-provider P] [-tenant ID] [-json]
airouter voice clone  -name NAME -consent [-desc D] [-label k=v ...] [-remove-noise] [-provider P] [-tenant ID] FILE...
airouter voice share  VOICE_ID        # operator: let every tenant use a voice you cloned
airouter voice unshare VOICE_ID       # make it private again
airouter voice rm     VOICE_ID [-tenant ID]
```

| Flag | Description |
|------|-------------|
| `-consent` | **Required for `clone`.** You confirm you have the speaker's permission (or the rights) to clone this voice. |
| `-name N` | Name of the new voice (max 100 characters). Use it afterwards as `speak -voice N`. |
| `-label k=v` | Repeatable, e.g. `-label gender=female -label accent=british`. |
| `-remove-noise` | Remove background noise from the samples (can make clean recordings worse). |
| `-search S` | (`list`) filter by name, description, category or label. |

```bash
airouter voice list -search female
airouter voice clone -name Ali -consent -label gender=male sample1.mp3 sample2.wav
airouter speak -voice ali -format mp3 -o hello.mp3 "Hello from my cloned voice"
airouter voice rm VOICE_ID
```

Cloning uses one ElevenLabs voice slot and needs a plan with Instant Voice Cloning. A clone lives in the account of the key that made it, and only the tenant that cloned it can see or use it. Details: [ELEVENLABS.md](ELEVENLABS.md).

## transcribe

Audio file to text. Prints the transcript to stdout.

```
airouter transcribe [flags] AUDIO_FILE
```

| Flag | Description |
|------|-------------|
| `-language L` | ISO-639-1 hint, e.g. `en`. |
| `-prompt TEXT` | Spelling/style hint (about 224 tokens max). |
| `-provider NAME` | Pin one STT provider. |
| `-envelope` | Print metadata (provider, model, language, latency) on stderr. |
| `-tenant ID` | |

```bash
airouter transcribe recording.mp3
airouter transcribe -language en -provider groq-whisper-large-v3-turbo recording.wav
```

## serve

Run the HTTP API, OpenAI-compatible API and web UI.

```
airouter serve [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-addr ADDR` | `:8080` | Listen address. |
| `-cors ORIGINS` | `$AIROUTER_CORS` (off) | Allowed browser origins, comma-separated, or `*`. Needed when a website (for example GitHub Pages) calls your server. |
| `-allow-tenant-urls` | off (`AIROUTER_ALLOW_TENANT_URLS=1`) | Let tenants add models with **any** base URL. Only if you trust all tenants. |
| `-block-tenant-ollama` | off | Do not let tenants use this server's Ollama. |
| `-max-tenant-models N` | 100 | Most models one tenant may add. |

```bash
export AIROUTER_ADMIN_TOKEN=change-me
export AIROUTER_SECRET=$(openssl rand -hex 32)
airouter serve -addr :8080 -cors https://myname.github.io
```

Web UI: `http://localhost:8080/`. OpenAI base URL: `http://localhost:8080/v1`. Run **one** server process per database.

## bootstrap

Seed the DB from the curated free-pool catalog. Entries whose key env var is missing are skipped with a message.

```
airouter bootstrap [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-category C` | `text` | `text`, `tts`, `stt`, `embed`, a comma list like `text,tts,stt`, or `all`. |
| `-preset NAME` | all | Only this preset (e.g. `groq`). Respects `-category`. |
| `-overwrite` | false | Update existing same-name entries in place. |
| `-store-keys` | false | Copy keys found in env vars into the encrypted DB; afterwards the env vars are not needed. |
| `-tenant ID` | | Import the models as **private** to this tenant. |

```bash
export GROQ_API_KEY=gsk_... GEMINI_API_KEY=...
airouter bootstrap
airouter bootstrap -category text,tts,stt
airouter bootstrap -preset groq -overwrite
airouter bootstrap -store-keys
airouter bootstrap -tenant acme
```

## provider

Manage models. Subcommands: `add`, `list`, `rm`, `enable`, `disable`, `reset`, `set-key`, `limits`, `import`.

### provider add

```
airouter provider add -preset NAME -model ID [flags]
```

| Flag | Description |
|------|-------------|
| `-preset NAME` | `groq, gemini, gemma, gemini-tts, elevenlabs, openrouter, mistral, zai, sambanova, huggingface, ollama, ollama-remote, openai` |
| `-model ID` | **Required.** Exact model id from the provider console. |
| `-name NAME` | Unique name (default `preset-model`). |
| `-kind K` | `openai` or `ollama`. |
| `-base-url URL` | API base URL (needed for custom providers). |
| `-key-env VAR` | Env var holding the key. |
| `-key KEY` | Key to store encrypted (appears in shell history, prefer `-key-stdin`). |
| `-key-stdin` | Read the key from stdin. |
| `-tenant ID` | Restrict to one tenant (default: shared by all). |
| `-tz ZONE` | IANA zone of the daily reset, e.g. `America/Los_Angeles`. |
| `-category C` | `text` (default), `tts`, `stt`, `embed` (embeddings). |
| `-priority N` | Lower is tried first. |
| `-rpm N` `-rpd N` `-tpm N` | Requests/min, requests/day, tokens/min. `0` = no local cap. |
| `-ash N` `-asd N` | Audio seconds per hour / day (TTS and STT). |
| `-workers N` | Max concurrent requests. |
| `-used-today N` | Requests already used today. |
| `-no-json-mode` | Model rejects `response_format` (e.g. Gemma). |
| `-no-system-role` | Model rejects system messages (e.g. Gemma). |

```bash
airouter provider add -preset groq -model openai/gpt-oss-120b -rpm 30 -rpd 1000 -tpm 8000 -priority 10
echo "$GROQ_API_KEY" | airouter provider add -preset groq -model openai/gpt-oss-20b -key-stdin
airouter provider add -preset ollama -model llama3:8b -priority 90
airouter provider add -preset gemini -model gemini-3.5-flash-lite -rpd 500 -used-today 496
airouter provider add -preset groq -model canopylabs/orpheus-v1-english -category tts -rpm 10 -rpd 100
```

### Other provider subcommands

| Command | What it does |
|---------|--------------|
| `provider list [-tenant ID]` | Live table: ID, OWNER, CAT, NAME, MODEL, STATE, KEY, USED/RPD, RPM, TPM, AUDIO SEC, RESETS IN, TZ. |
| `provider rm ID [-tenant ID]` | Delete. |
| `provider enable ID` / `disable ID` | Turn on / off. |
| `provider reset ID` | Lift a pause (after fixing a key or model id). |
| `provider set-key ID [-key KEY]` | Replace the key (asks on the terminal if `-key` omitted). Lifts any pause. |
| `provider limits ID [-rpm N] [-rpd N] [-tpm N] [-workers N] [-priority N]` | Change limits of a tenant-owned model. Shared models: use `provider import` or the admin API. |
| `provider import [-tenant ID] FILE.json` | Import from a JSON file. |

Import file format:

```json
{"providers": [
  {"preset": "groq",   "model": "openai/gpt-oss-20b", "api_key": "gsk_...", "rpm": 30, "rpd": 1000},
  {"preset": "gemini", "model": "gemini-2.5-flash",   "api_key": "AIza...", "rpm": 10},
  {"preset": "ollama", "model": "gemma3:4b"}
]}
```

## tenant

```
airouter tenant add NAME [-limit N] [-pool-only] [-tz ZONE]   # prints the API key ONCE
airouter tenant cap NAME [-limit N] [-pool-only[=false]] [-disable | -enable]   # change a tenant later
airouter tenant list
airouter tenant rotate NAME                                    # new key; old one stops working
```

| Flag | Default | Description |
|------|---------|-------------|
| `-limit N` | 0 | Tenant daily request cap (0 = unlimited). |
| `-pool-only` | false | The cap counts **only answers from the shared pool**. Answers from the tenant's own keys are free and still work after the cap is reached. Use `-pool-only=false` with `tenant cap` to turn it off. |
| `-disable` / `-enable` | | (`cap`) turn the tenant off or on. |

`tenant cap` changes only what you pass: `tenant cap acme -limit 500` keeps the pool-only setting as it was. `tenant list` shows each tenant's cap type (`all requests` or `pool only`) and state.
| `-tz ZONE` | `UTC` | Tenant timezone (its day boundary). |

## usage

```
airouter usage [-tenant ID] [-day YYYY-MM-DD]
```

Shows requests, tokens, errors per provider, how many all tenants used (the quota is shared), the daily limit and when it resets.

## presets

`airouter presets` lists built-in provider presets (connection defaults).

## free-pool

```
airouter free-pool [-category X] [-json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-category C` | `all` | `text, tts, stt, embed, all` (comma list allowed). |
| `-json` | false | JSON instead of a table. |

## ollama

```
airouter ollama list                           # models installed on the local Ollama
airouter ollama add [-tenant ID] MODEL...      # add as shared (or private to a tenant)
```

## me

Self-service for **one user, over HTTP** with their own key. It never opens the local DB, so it works from any machine.

```
airouter me [-url URL] [-key KEY] <command> [args]
```

| Flag (before the command) | Default |
|------|---------|
| `-url` | `$AIROUTER_URL` or `http://localhost:8080` |
| `-key` | `$AIROUTER_KEY` |

| Command | Description |
|---------|-------------|
| `whoami` | Who am I. |
| `list` | My models (plus shared ones, read-only). |
| `catalog [-preset NAME]` | Providers and models I can pick from. |
| `add -preset P -model M [-key-stdin] [-rpm N -rpd N -tpm N -workers N]` | Add one model. |
| `pick -preset P [-key-stdin] MODEL...` | Add several catalog models with one key. |
| `ollama [add MODEL...]` | List the server's Ollama models, or add some. |
| `limits ID [-rpm N -rpd N -tpm N -workers N -priority N]` | Change limits. |
| `set-key ID [-key-stdin]` | Replace a model's key. |
| `enable ID` / `disable ID` / `reset ID` / `rm ID` | Manage a model. |
| `import FILE.json` / `export` | JSON file in / out (export has no keys). |
| `usage [-day YYYY-MM-DD]` | My usage. |
| `update [-name X] [-tz Z]` | My profile. |
| `rotate-key` | New API key (old one stops working). |

```bash
export AIROUTER_URL=https://my-airouter.example.com
export AIROUTER_KEY=air_...
airouter me catalog
echo "$GROQ_API_KEY" | airouter me pick -preset groq -key-stdin openai/gpt-oss-20b openai/gpt-oss-120b
airouter me limits 7 -rpm 20 -rpd 500
airouter me update -name Alice -tz Asia/Karachi
```

---

## Environment variables

| Variable | Used by | Description |
|----------|---------|-------------|
| `AIROUTER_DB` | all | DB path (default `./airouter.db`). |
| `AIROUTER_SECRET` | all | Master secret that encrypts provider keys (AES-256-GCM). If unset, a `0600` file `<db>.key` is created. **Back it up separately from the DB.** |
| `AIROUTER_ADMIN_TOKEN` | `serve` | Enables admin endpoints. Unset = admin API disabled. |
| `AIROUTER_CORS` | `serve` | Default for `-cors`. |
| `AIROUTER_ALLOW_TENANT_URLS` | `serve` | `1` = same as `-allow-tenant-urls`. |
| `AIROUTER_URL`, `AIROUTER_KEY` | `me` | Server URL and tenant key. |
| `GROQ_API_KEY`, `GEMINI_API_KEY`, `ELEVENLABS_API_KEY`, `OPENROUTER_API_KEY`, `ZAI_API_KEY`, `MISTRAL_API_KEY`, `SAMBANOVA_API_KEY`, `OPENAI_API_KEY`, `HF_TOKEN` | `bootstrap`, providers | Provider keys (optional if stored in the DB). |
| `OLLAMA_HOST` | Ollama | Ollama endpoint (default `http://localhost:11434`). |

## Exit codes

`0` success. `1` an error (message on stderr; router errors list which providers failed and when to retry). `2` unknown command or no command (prints help).
