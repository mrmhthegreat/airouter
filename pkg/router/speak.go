package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Speak synthesizes speech from text. It picks a TTS-capable provider
// (category="tts"), sends the request, and on failure fails over to the next
// provider — same failover shape as Generate. Returns the audio bytes plus
// metadata (provider, model, voice, format).
//
// Defaults:
//   - Voice:   "Kore" for Gemini, "tara" for Groq Orpheus English, "hassan" for
//     Groq Orpheus Arabic. Override with req.Voice.
//   - Format:  "wav" (both providers return WAV).
//
// Audio bytes are in SpeakResponse.Audio; the HTTP layer base64-encodes them
// into SpeakResponse.AudioB64 when responding as JSON.
func (r *Router) Speak(ctx context.Context, req types.SpeakRequest) (*types.SpeakResponse, error) {
	start := r.now()
	if req.TenantID == "" {
		req.TenantID = types.DefaultTenantID
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("%w: SpeakRequest.Text is required", types.ErrBadRequest)
	}
	tenant, err := r.store.TenantByID(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, types.ErrUnknownTenant
	}
	if tenant.Disabled {
		return nil, types.ErrTenantDisabled
	}
	ownOnly, capErr, err := r.capGate(ctx, tenant, start)
	if err != nil {
		return nil, err
	}

	// Use the existing candidates() by constructing an internal Request with
	// Category=tts. The Provider field is reused for pinning.
	cands := r.candidates(ctx, types.Request{TenantID: req.TenantID, Provider: req.Provider, Category: types.CategoryTTS})
	if ownOnly {
		if cands = onlyOwn(cands); len(cands) == 0 {
			return nil, capErr
		}
	}
	var verr error
	if cands, verr = r.voiceCandidates(ctx, req, cands); verr != nil {
		return nil, verr
	}
	if len(cands) == 0 {
		return nil, errors.New("airouter: no TTS providers configured (run `airouter bootstrap -category tts`)")
	}

	var attempts []types.Attempt
	failed := map[int64]bool{}
	wait := r.maxWait
	if req.MaxWait > 0 {
		wait = req.MaxWait
	}
	deadline := start.Add(wait)
	var lastVoiceErr *voiceNotFoundError // set while every failure so far was "voice not found"
	otherFailure := false

	for {
		now := r.now()
		var picked *candidate
		minWait := time.Duration(1<<62 - 1)
		reasons := map[string]string{}
		remaining := 0 // candidates not yet tried

		for i := range cands {
			c := &cands[i]
			if failed[c.cfg.ID] {
				continue
			}
			remaining++
			if !c.cfg.HasKey() {
				reasons[c.cfg.Name] = "no API key (set $" + c.cfg.APIKeyEnv + ")"
				continue
			}
			ok, wait, _, why := r.acquire(ctx, c.cfg, c.state, now)
			if ok {
				picked = c
				break
			}
			reasons[c.cfg.Name] = why
			if wait < minWait {
				minWait = wait
			}
		}

		if picked == nil {
			if lastVoiceErr != nil && !otherFailure && len(attempts) > 0 {
				return nil, lastVoiceErr // the voice is wrong; waiting would not help
			}
			if remaining == 0 && len(attempts) > 0 { // every provider was tried and failed: say why (400 vs 502), do not claim "limited"
				return nil, r.allFailed(attempts)
			}
			if now.Add(minWait).After(deadline) || ctx.Err() != nil {
				if minWait == time.Duration(1<<62-1) {
					minWait = time.Hour
				}
				return nil, &types.ExhaustedError{RetryAfter: minWait, Reasons: reasons}
			}
			select {
			case <-time.After(minWait + 20*time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}

		audio, contentType, format, voice, err := r.callSpeak(ctx, picked.cfg, req)
		picked.state.release(0) // TTS does not return tokens; release the slot
		// Estimate output audio duration from the input text length. Average
		// speaking rate is ~150 wpm = ~15 chars/sec. This is rough but keeps
		// us under the ASH/ASD caps with the 90% safety margin in acquire.
		audioSecs := estimateTTSAudioSeconds(req.Text)
		if err == nil && audioSecs > 0 {
			picked.state.releaseAudio(audioSecs, r.now())
		}
		errs := 0
		if err != nil {
			errs = 1
		}
		_ = r.store.AddUsage(ctx, req.TenantID, picked.cfg.ID, dayOf(picked.cfg, now), 1, 0, errs)
		if err == nil {
			// The caller may want mp3 or opus while the provider returns WAV.
			audio, contentType, format = maybeConvert(ctx, audio, contentType, format, req.Format)
			out := &types.SpeakResponse{
				Audio:       audio,
				ContentType: contentType,
				Format:      format,
				Provider:    picked.cfg.Name,
				Own:         picked.cfg.TenantID != "",
				Model:       picked.cfg.Model,
				Voice:       voice,
				LatencyMS:   r.now().Sub(start).Milliseconds(),
				Attempts:    attempts,
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var vnf *voiceNotFoundError
		var f failure
		if errors.As(err, &vnf) { // a wrong voice is the caller's mistake, not the provider's
			lastVoiceErr = vnf
		} else {
			f = r.classify(ctx, picked, err)
			if f.kind != kindBadRequest { // a 400 is the request's fault (e.g. a voice this provider does not know), not an outage
				otherFailure = true
			}
		}
		attempts = append(attempts, types.Attempt{Provider: picked.cfg.Name, Error: err.Error(), Status: f.status, Kind: f.kind})
		failed[picked.cfg.ID] = true
	}
}

// callSpeak sends one TTS request to one provider and returns the audio bytes.
// It dispatches between the two API shapes:
//   - Groq-style (OpenAI-compatible):   POST {base}/audio/speech with JSON body.
//   - Gemini-style (native):           POST {base}/models/{model}:generateContent
//     with audio modality in generationConfig.
//
// Detection: if BaseURL contains "generativelanguage.googleapis.com", use the
// Gemini shape. Otherwise use the OpenAI shape.
func (r *Router) callSpeak(ctx context.Context, p types.ProviderConfig, req types.SpeakRequest) (audio []byte, contentType, format, voice string, err error) {
	voice = req.Voice
	if voice == "" {
		voice = defaultVoice(p)
	}
	format = strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "wav"
	}
	// Groq's TTS only returns WAV. If the caller wants mp3/opus we ask for WAV
	// and Speak converts it afterwards (needs ffmpeg). Sending "mp3" to Groq
	// would be rejected with HTTP 400.
	if isGroq(p) {
		format = "wav"
	}

	if isElevenLabs(p) {
		return r.callSpeakElevenLabs(ctx, p, req, format)
	}
	if strings.Contains(p.BaseURL, "generativelanguage.googleapis.com") {
		return r.callSpeakGemini(ctx, p, req, voice, format)
	}
	return r.callSpeakOpenAI(ctx, p, req, voice, format)
}

// defaultVoice picks a sensible voice id for the provider/model when the caller
// did not specify one. Override by setting SpeakRequest.Voice.
func defaultVoice(p types.ProviderConfig) string {
	switch {
	case strings.Contains(p.BaseURL, "generativelanguage.googleapis.com"):
		return "Kore" // one of Gemini's 30 prebuilt voices
	case strings.Contains(p.Model, "orpheus-v1-english"):
		return "tara" // natural female English voice
	case strings.Contains(p.Model, "orpheus-arabic-saudi"):
		return "hassan" // male Saudi Arabic voice
	}
	return "tara"
}

// callSpeakOpenAI is the Groq path: POST {base}/audio/speech with JSON body.
// The provider returns raw audio bytes directly (binary response).
func (r *Router) callSpeakOpenAI(ctx context.Context, p types.ProviderConfig, req types.SpeakRequest, voice, format string) ([]byte, string, string, string, error) {
	url := p.BaseURL + "/audio/speech"
	body := map[string]any{
		"model":           p.Model,
		"input":           req.Text,
		"voice":           voice,
		"response_format": format,
	}
	raw, _ := json.Marshal(body)
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytesReader(raw))
	if err != nil {
		return nil, "", "", voice, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, "", "", voice, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return nil, "", "", voice, &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MB cap on audio
	if err != nil {
		return nil, "", "", voice, err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = contentTypeForFormat(format)
	}
	return data, ct, format, voice, nil
}

// callSpeakGemini is the native Gemini path:
// POST {base}/models/{model}:generateContent with audio modality in
// generationConfig. The response JSON contains base64-encoded WAV bytes in
// candidates[0].content.parts[0].inlineData.data.
func (r *Router) callSpeakGemini(ctx context.Context, p types.ProviderConfig, req types.SpeakRequest, voice, format string) ([]byte, string, string, string, error) {
	url := p.BaseURL + "/models/" + p.Model + ":generateContent"
	body := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": req.Text}}},
		},
		"generationConfig": map[string]any{
			"response_modalities": []string{"AUDIO"},
			"speech_config": map[string]any{
				"voice_config": map[string]any{
					"prebuilt_voice_config": map[string]any{
						"voice_name": voice,
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(body)
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytesReader(raw))
	if err != nil {
		return nil, "", "", voice, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Gemini's native endpoint uses x-goog-api-key, not Bearer.
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("x-goog-api-key", k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, "", "", voice, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return nil, "", "", voice, &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	// Parse candidates[0].content.parts[0].inlineData.data
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return nil, "", "", voice, fmt.Errorf("bad gemini TTS response: %s", types.Truncate(string(data), 200))
	}
	part := parsed.Candidates[0].Content.Parts[0].InlineData
	audioBytes, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil {
		return nil, "", "", voice, fmt.Errorf("bad base64 in gemini TTS response: %w", err)
	}
	ct := part.MimeType
	if ct == "" {
		ct = "audio/wav"
	}
	// Gemini returns raw 16-bit PCM (mime "audio/L16;codec=pcm;rate=24000") with
	// no header. Wrap it so callers and ffmpeg get a real WAV file.
	audioBytes, ct = ensureWAV(audioBytes, ct)
	return audioBytes, ct, "wav", voice, nil
}

// contentTypeForFormat maps an audio format name to its MIME type.
func contentTypeForFormat(format string) string {
	switch strings.ToLower(format) {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/L16;rate=24000"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/ogg"
	default:
		return "audio/wav"
	}
}

// atoiSafe parses a string to int, returning 0 on error. Used for Retry-After.
func atoiSafe(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an int")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// bytesReader is a tiny wrapper to avoid importing bytes here just for
// bytes.NewReader — keeps the import list small.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct{ b []byte }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// estimateTTSAudioSeconds estimates the output audio duration from the input
// text length. Average English speaking rate is ~150 wpm ≈ 2.5 words/sec ≈ 15
// chars/sec. Used for ASH/ASD tracking since TTS responses don't include
// duration metadata. Always at least 1 second.
//
// This is intentionally rough — the 90% safety margin in acquire absorbs the
// error. For non-Latin scripts (Arabic, Chinese, etc.) the chars/sec rate is
// similar because those scripts are denser per character.
func estimateTTSAudioSeconds(text string) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	secs := n / 15
	if secs < 1 {
		secs = 1
	}
	return secs
}

// isGroq reports whether the provider is Groq's API.
func isGroq(p types.ProviderConfig) bool { return strings.Contains(p.BaseURL, "api.groq.com") }
