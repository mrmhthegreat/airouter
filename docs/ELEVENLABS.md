# ElevenLabs: voices, voice cloning and dynamic voices

airouter can use ElevenLabs for **text to speech**, **speech to text** (Scribe), and **voice cloning**. It is a normal provider, so it also gets failover, usage counting and per-tenant keys.

| Need | How |
|------|-----|
| Pick a voice **per request** | `voice` = a voice id **or name** (case-insensitive) |
| Tune the voice per request | `voice_settings`: `stability`, `similarity_boost`, `style`, `speed`, `use_speaker_boost` |
| Pick a model per request | `model`, for example `eleven_flash_v2_5` (fast) or `eleven_multilingual_v2` |
| List what a user can use | `GET /v1/voices`, `airouter voice list` |
| Clone a voice | `POST /v1/voices/clone`, `airouter voice clone` |
| WhatsApp / Telegram voice note | `format: "opus"` (mono Ogg/Opus, made natively by ElevenLabs) |

## 1. Add ElevenLabs

```bash
# speech
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model eleven_multilingual_v2 -key-stdin
# speech to text (optional)
echo "$ELEVENLABS_API_KEY" | airouter provider add -preset elevenlabs -model scribe_v1 -category stt -key-stdin
```

The preset uses `https://api.elevenlabs.io` and the env var `ELEVENLABS_API_KEY`. If your account uses data residency, pass `-base-url https://api.us.elevenlabs.io` (or your regional host). A tenant adds its own the same way: `airouter me add -preset elevenlabs -model eleven_multilingual_v2 -key-stdin`.

ElevenLabs is **not** in the free-pool catalog (`bootstrap` skips it) because its free allowance and limits depend on your plan. Set `-rpm`, `-rpd` and `-workers` to what your plan allows. ElevenLabs limits concurrent requests per plan, so keep `-workers` low (2 to 4).

## 2. Speak with a voice

```bash
airouter voice list
airouter speak -voice Rachel -format mp3 -o hello.mp3 "Hello!"
airouter speak -voice Rachel -stability 0.4 -similarity 0.8 -speed 1.05 -model eleven_flash_v2_5 -o hello.wav "Hello!"
```

```bash
curl -s http://localhost:8080/v1/speak -H "Authorization: Bearer air_..." \
  -d '{"text":"Hello!","voice":"Rachel","format":"opus","voice_settings":{"stability":0.4,"speed":1.05}}' --output note.ogg
```

- `voice` empty: the first ready-made voice of the account.
- `voice` as a name: exact match first, then a partial match **only if exactly one voice matches** (`rachel` finds `Rachel - Calm`).
- A wrong voice answers `404 voice_not_found` and **never pauses the provider**, so one typo cannot take speech down for everyone.
- Formats: `wav` (built from 24 kHz PCM, works on every plan), `mp3`, `opus`, `pcm`.

## 3. Clone a voice

```bash
airouter voice clone -name Ali -consent -label gender=male sample1.mp3 sample2.wav
airouter speak -voice Ali -format opus -o note.ogg "Hi, this is Ali"
airouter voice rm VOICE_ID
```

Over HTTP (multipart): see [API.md](API.md#voices-elevenlabs). In Go: `client.CloneVoice(ctx, types.CloneVoiceRequest{...})`.

**Consent is required.** `-consent` (CLI), `consent=true` (HTTP) or `ConsentConfirmed: true` (Go) means: *I have the speaker's permission, or the rights, to clone this voice.* Without it the call fails with `consent_required`. airouter does not check who the speaker is, so the confirmation is the caller's responsibility; build a real consent step into your app.

Good samples: 1 to 25 clean recordings of one person, no music, no other voices, about 1 to 5 minutes in total. Max 12 MB each and 50 MB together. `-remove-noise` can make clean audio worse, so leave it off unless the recordings are noisy. Instant cloning needs an ElevenLabs plan that includes it; if it does not, you get a clear `400` with ElevenLabs' reason.

## 4. Who can see and use a clone (tenant isolation)

A clone lives in the ElevenLabs account of the key that created it. airouter also records **which tenant** cloned it, and enforces:

| Rule | Behaviour |
|------|-----------|
| Listing | A tenant sees ready-made voices, voices of its own ElevenLabs account, and **its own clones**. Other tenants' clones are filtered out even when they sit in the same shared account. |
| Using | Speaking with another tenant's voice (by id **or** by name) answers `404 voice_not_found`. |
| Deleting | Only the tenant that cloned a voice can delete it. |
| Where tenants clone | In their **own** ElevenLabs account. With no ElevenLabs key of their own: `403`. The operator can allow cloning in the shared account with `TenantPolicy{AllowSharedVoiceClone: true}`. The `default` tenant (CLI and embedded use) is always allowed. |
| How many | 10 per tenant by default (`TenantPolicy.MaxClonedVoices`). |
| Failover | A clone exists in only one account, so requests using it are pinned to that provider and never sent to another account. |
| Sharing | The operator can share a voice **it cloned** with all tenants: `airouter voice share VOICE_ID` or `POST /v1/voices/{id}/share`. Tenants see it with `shared: true` and can speak with it; they cannot share, unshare or delete it. Tenants' clones can never be shared. The usage is billed to the account the voice lives in. |

**Voice names never leak to the wrong provider.** An ElevenLabs voice (an id, or a name such as `Rachel` or a shared/cloned voice) is only sent to ElevenLabs providers. Without this, a Groq or Gemini voice provider with a better priority would be asked for it first, answer HTTP 400, and three such answers in a row pause that provider for 24 hours for every tenant. Voices of other providers (`tara`, `Kore`, or none) still go to their own provider.

These rules are covered by tests (`TestVoiceIsolationBetweenTenants`, `TestTenantClonesOnlyWithOwnKey`).

Things airouter does **not** do: it cannot see inside a shared ElevenLabs account, so voices created there **outside** airouter (in the ElevenLabs dashboard) are visible to every tenant using that shared provider. Keep private voices in the tenant's own account.

## 5. WhatsApp voice notes

WhatsApp voice notes are Ogg/Opus. With ElevenLabs, ask for `format: "opus"` and send the bytes as an `audio/ogg; codecs=opus` voice message. For Groq and Gemini voices airouter converts WAV to Opus with `ffmpeg` (install it on the server: `apt install ffmpeg`); if `ffmpeg` is missing you get WAV back and the response `format` says `wav`, so check it:

```go
resp, _ := c.Speak(ctx, types.SpeakRequest{Text: text, Format: "opus"})
if resp.Format != "opus" { /* ffmpeg missing: convert elsewhere or send as a normal audio file */ }
```

Converting is limited to 4 `ffmpeg` processes at once so a burst of requests cannot use up the CPU.

## 6. Limits and cost

- Every character you synthesize uses ElevenLabs credits on the account of the key that answered. A tenant's **own** key bills the tenant; the shared key bills you. Use `pool_only_cap` (see below) so tenants with their own key are not capped by your pool quota.
- airouter counts **requests**, not characters or credits. Set the cap and `-rpd` with that in mind, and watch usage in the ElevenLabs dashboard.
- Voice lists are cached for 5 minutes per provider, and refreshed right after a clone or delete.
