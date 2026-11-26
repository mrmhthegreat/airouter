package router

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/elevenlabs"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// isElevenLabs reports whether the provider talks to ElevenLabs. Like Gemini,
// it is detected from the base URL, so no new "kind" is needed.
func isElevenLabs(p types.ProviderConfig) bool { return strings.Contains(p.BaseURL, "elevenlabs.io") }

func (r *Router) elClient(p types.ProviderConfig) *elevenlabs.Client {
	return &elevenlabs.Client{BaseURL: p.BaseURL, APIKey: p.ResolveKey(), HTTP: r.client}
}

// voiceNotFoundError means "this voice does not exist for this tenant in this
// account". It is NOT the provider's fault, so it never pauses the provider.
type voiceNotFoundError struct{ voice string }

func (e *voiceNotFoundError) Error() string {
	return fmt.Sprintf("voice %q not found for this provider (use GET /v1/voices to list voices)", e.voice)
}
func (e *voiceNotFoundError) Is(t error) bool { return t == types.ErrVoiceNotFound }

// elErr converts an ElevenLabs API error into the error types the router's
// failure classifier already understands.
func elErr(err error, voice string) error {
	var ae *elevenlabs.APIError
	if errors.As(err, &ae) {
		if ae.IsVoiceNotFound() {
			return &voiceNotFoundError{voice: voice}
		}
		return &types.HTTPError{Code: ae.Status, Body: ae.Body, RetryAfter: ae.RetryAfter}
	}
	return err
}

// ---- voice list cache ---------------------------------------------------------

const voiceCacheTTL = 5 * time.Minute

type voiceCache struct {
	mu sync.Mutex
	m  map[int64]voiceCacheEntry
}
type voiceCacheEntry struct {
	at     time.Time
	voices []elevenlabs.Voice
}

func (c *voiceCache) get(id int64, now time.Time) ([]elevenlabs.Voice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || now.Sub(e.at) > voiceCacheTTL {
		return nil, false
	}
	return e.voices, true
}
func (c *voiceCache) put(id int64, now time.Time, v []elevenlabs.Voice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[int64]voiceCacheEntry{}
	}
	c.m[id] = voiceCacheEntry{at: now, voices: v}
}
func (c *voiceCache) drop(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}

// accountVoices returns every voice in the provider's ElevenLabs account (cached 5 min).
func (r *Router) accountVoices(ctx context.Context, p types.ProviderConfig) ([]elevenlabs.Voice, error) {
	if v, ok := r.vcache.get(p.ID, r.now()); ok {
		return v, nil
	}
	v, err := r.elClient(p).Voices(ctx, "")
	if err != nil {
		return nil, elErr(err, "")
	}
	r.vcache.put(p.ID, r.now(), v)
	return v, nil
}

// sharedRec reports whether a voice record is an operator voice the operator
// has chosen to share (VoiceRecord.Shared). Shared operator voices can be
// listed and spoken by every tenant that can use the provider; everything
// else stays private to the tenant that cloned it.
func sharedRec(rec *types.VoiceRecord) bool {
	return rec != nil && rec.TenantID == types.DefaultTenantID && rec.Shared
}

// visibleVoices is accountVoices minus every voice that another tenant cloned
// (the operator's shared clones stay visible).
func (r *Router) visibleVoices(ctx context.Context, p types.ProviderConfig, tenantID string) ([]elevenlabs.Voice, error) {
	all, err := r.accountVoices(ctx, p)
	if err != nil {
		return nil, err
	}
	recs, err := r.store.ListVoices(ctx, "")
	if err != nil {
		return nil, err
	}
	hidden := map[string]bool{}
	for _, rec := range recs {
		if rec.TenantID != tenantID && !sharedRec(&rec) {
			hidden[rec.VoiceID] = true
		}
	}
	out := make([]elevenlabs.Voice, 0, len(all))
	for _, v := range all {
		if !hidden[v.VoiceID] {
			out = append(out, v)
		}
	}
	return out, nil
}

var voiceIDRe = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// resolveVoice turns what the caller wrote (empty, a voice id, or a voice name)
// into a voice id this tenant may use on this provider.
func (r *Router) resolveVoice(ctx context.Context, p types.ProviderConfig, tenantID, voice string) (id, name string, err error) {
	voice = strings.TrimSpace(voice)
	if voiceIDRe.MatchString(voice) {
		rec, err := r.store.VoiceByID(ctx, voice)
		if err != nil {
			return "", "", err
		}
		if rec != nil && ((rec.TenantID != tenantID && !sharedRec(rec)) || rec.ProviderID != p.ID) {
			return "", "", &voiceNotFoundError{voice: voice} // somebody else's clone
		}
		return voice, voice, nil
	}
	vs, err := r.visibleVoices(ctx, p, tenantID)
	if err != nil {
		return "", "", err
	}
	if voice == "" { // default: the first ready-made voice, else the first voice
		for _, v := range vs {
			if v.Category == "premade" {
				return v.VoiceID, v.Name, nil
			}
		}
		if len(vs) > 0 {
			return vs[0].VoiceID, vs[0].Name, nil
		}
		return "", "", &voiceNotFoundError{voice: "(default: this account has no voices)"}
	}
	want := strings.ToLower(voice)
	var contains []elevenlabs.Voice
	for _, v := range vs {
		n := strings.ToLower(v.Name)
		if n == want {
			return v.VoiceID, v.Name, nil
		}
		if strings.Contains(n, want) {
			contains = append(contains, v)
		}
	}
	if len(contains) == 1 { // "rachel" finds "Rachel - Calm", but only when it is unambiguous
		return contains[0].VoiceID, contains[0].Name, nil
	}
	return "", "", &voiceNotFoundError{voice: voice}
}

// pinCloneProvider: when the voice is one of this tenant's own clones, only the
// provider (account) that holds it can speak it, so failover must not wander.
func (r *Router) pinCloneProvider(ctx context.Context, tenantID, voice string) (providerID int64, ok bool) {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return 0, false
	}
	if rec, _ := r.store.VoiceByID(ctx, voice); rec != nil {
		if rec.TenantID == tenantID || sharedRec(rec) {
			return rec.ProviderID, true
		}
		return 0, false
	}
	recs, _ := r.store.ListVoices(ctx, tenantID) // the tenant's own clones first
	for _, rec := range recs {
		if strings.EqualFold(rec.Name, voice) {
			return rec.ProviderID, true
		}
	}
	all, _ := r.store.ListVoices(ctx, "") // then the operator's shared voices, by name
	for i := range all {
		if sharedRec(&all[i]) && strings.EqualFold(all[i].Name, voice) {
			return all[i].ProviderID, true
		}
	}
	return 0, false
}

// voiceCandidates narrows the TTS candidates to the providers that can really
// speak req.Voice, BEFORE any request is sent.
//
//   - A voice the tenant (or the operator, when shared) cloned lives in one
//     ElevenLabs account: only that provider may speak it.
//   - Any other ElevenLabs voice (a 20-character id, or a name one of the visible
//     ElevenLabs accounts knows, like "Rachel") can only be spoken by ElevenLabs
//     providers. Without this, a provider with a better priority (Groq, Gemini)
//     would be asked for it first, answer HTTP 400, and three such answers in a
//     row pause that provider for 24 hours for every tenant.
//
// Other voices ("tara", "Kore", empty) are left alone.
func (r *Router) voiceCandidates(ctx context.Context, req types.SpeakRequest, cands []candidate) ([]candidate, error) {
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		return cands, nil
	}
	if pid, ok := r.pinCloneProvider(ctx, req.TenantID, voice); ok {
		var pinned []candidate
		for _, c := range cands {
			if c.cfg.ID == pid {
				pinned = append(pinned, c)
			}
		}
		if len(pinned) == 0 {
			return nil, types.ErrVoiceNotFound
		}
		return pinned, nil
	}
	var el []candidate
	for _, c := range cands {
		if isElevenLabs(c.cfg) {
			el = append(el, c)
		}
	}
	if len(el) == 0 || len(el) == len(cands) {
		return cands, nil
	}
	if voiceIDRe.MatchString(voice) {
		return el, nil
	}
	for _, c := range el {
		if _, _, err := r.resolveVoice(ctx, c.cfg, req.TenantID, voice); err == nil {
			return el, nil
		}
	}
	return cands, nil
}

// ---- text to speech -------------------------------------------------------------

// elFormat maps airouter's audio format names to ElevenLabs output formats.
// WAV is built from 24 kHz PCM here (works on every plan) instead of asking for
// wav_*, and mp3/opus are native, so no ffmpeg is needed for ElevenLabs.
func elFormat(format string) (outputFormat, contentType, actual string) {
	switch strings.ToLower(format) {
	case "mp3":
		return "mp3_44100_128", "audio/mpeg", "mp3"
	case "opus", "ogg":
		return "opus_48000_64", "audio/ogg", "opus"
	case "pcm":
		return "pcm_24000", "audio/L16;codec=pcm;rate=24000", "pcm"
	default: // "", "wav" and anything ElevenLabs cannot make
		return "pcm_24000", "audio/wav", "wav"
	}
}

func (r *Router) callSpeakElevenLabs(ctx context.Context, p types.ProviderConfig, req types.SpeakRequest, format string) (audio []byte, contentType, outFormat, voice string, err error) {
	id, name, err := r.resolveVoice(ctx, p, req.TenantID, req.Voice)
	if err != nil {
		return nil, "", "", req.Voice, err
	}
	if name == "" {
		name = id
	}
	of, ct, actual := elFormat(format)
	model := p.Model
	if strings.TrimSpace(req.Model) != "" {
		model = strings.TrimSpace(req.Model)
	}
	var vs *elevenlabs.VoiceSettings
	if s := req.VoiceSettings; s != nil {
		vs = &elevenlabs.VoiceSettings{Stability: s.Stability, SimilarityBoost: s.SimilarityBoost, Style: s.Style, Speed: s.Speed, UseSpeakerBoost: s.UseSpeakerBoost}
	}
	data, gotCT, err := r.elClient(p).Speak(ctx, elevenlabs.SpeakOptions{
		VoiceID: id, Text: req.Text, ModelID: model, OutputFormat: of, LanguageCode: req.Language, Settings: vs,
	})
	if err != nil {
		return nil, "", "", name, elErr(err, name)
	}
	switch {
	case actual == "wav":
		data = pcmToWAV(data, 24000, 1, 16)
	case actual == "opus" && !strings.HasPrefix(string(data[:min(4, len(data))]), "OggS"):
		ct = gotCT // not an Ogg file: report what the server said
	}
	return data, ct, actual, name, nil
}

// ---- speech to text --------------------------------------------------------------

func (r *Router) callTranscribeElevenLabs(ctx context.Context, p types.ProviderConfig, req types.TranscribeRequest) (string, string, error) {
	filename := req.Filename
	mime := req.MimeType
	if mime == "" {
		mime = mimeTypeFromFilename(filename)
	}
	text, lang, err := r.elClient(p).Transcribe(ctx, req.Audio, filename, mime, p.Model, req.Language)
	if err != nil {
		return "", "", elErr(err, "")
	}
	return text, lang, nil
}
