package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Transcribe converts speech (audio bytes) to text. It picks an STT-capable
// provider (category="stt"), sends the audio, and on failure fails over to the
// next provider. Returns the transcribed text plus metadata.
//
// Supports Groq Whisper (OpenAI-compatible /audio/transcriptions), Gemini and ElevenLabs Scribe.
// Gemini transcription is routed too: see callTranscribeGemini (Files API upload).
func (r *Router) Transcribe(ctx context.Context, req types.TranscribeRequest) (*types.TranscribeResponse, error) {
	start := r.now()
	if req.TenantID == "" {
		req.TenantID = types.DefaultTenantID
	}
	if len(req.Audio) == 0 {
		return nil, fmt.Errorf("%w: TranscribeRequest.Audio is required (raw audio bytes)", types.ErrBadRequest)
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

	cands := r.candidates(ctx, types.Request{TenantID: req.TenantID, Provider: req.Provider, Category: types.CategorySTT})
	if ownOnly {
		if cands = onlyOwn(cands); len(cands) == 0 {
			return nil, capErr
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("airouter: no STT providers configured (run `airouter bootstrap -category stt`)")
	}

	var attempts []types.Attempt
	failed := map[int64]bool{}
	wait := r.maxWait
	if req.MaxWait > 0 {
		wait = req.MaxWait
	}
	deadline := start.Add(wait)

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
			if remaining == 0 && len(attempts) > 0 { // every provider was tried and failed: say why (400 vs 502)
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

		text, lang, err := r.callTranscribe(ctx, picked.cfg, req)
		picked.state.release(0)
		// Estimate input audio duration from the byte count + filename mime.
		// For WAV we parse the header for exact duration; for everything else
		// we fall back to a bytes-per-second heuristic. This is rough but
		// keeps us under the ASH/ASD caps with the 90% safety margin.
		audioSecs := estimateSTTAudioSeconds(req.Audio, req.Filename, req.MimeType)
		if err == nil && audioSecs > 0 {
			picked.state.releaseAudio(audioSecs, r.now())
		}
		errs := 0
		if err != nil {
			errs = 1
		}
		_ = r.store.AddUsage(ctx, req.TenantID, picked.cfg.ID, dayOf(picked.cfg, now), 1, 0, errs)
		if err == nil {
			out := &types.TranscribeResponse{
				Text:      text,
				Provider:  picked.cfg.Name,
				Own:       picked.cfg.TenantID != "",
				Model:     picked.cfg.Model,
				Language:  lang,
				LatencyMS: r.now().Sub(start).Milliseconds(),
				Attempts:  attempts,
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f := r.classify(ctx, picked, err)
		attempts = append(attempts, types.Attempt{Provider: picked.cfg.Name, Error: err.Error(), Status: f.status, Kind: f.kind})
		failed[picked.cfg.ID] = true
	}
}

// callTranscribe dispatches by provider type. Groq uses the OpenAI-compatible
// multipart endpoint. Gemini uses a two-step protocol: upload the file via the
// Files API, then POST the file URI to the /v1beta/interactions endpoint.
// Detection: BaseURL contains "generativelanguage.googleapis.com" → Gemini.
func (r *Router) callTranscribe(ctx context.Context, p types.ProviderConfig, req types.TranscribeRequest) (text, language string, err error) {
	if isElevenLabs(p) {
		return r.callTranscribeElevenLabs(ctx, p, req)
	}
	if strings.Contains(p.BaseURL, "generativelanguage.googleapis.com") {
		return r.callTranscribeGemini(ctx, p, req)
	}
	return r.callTranscribeOpenAI(ctx, p, req)
}

// callTranscribeOpenAI is the Groq Whisper path: POST {base}/audio/transcriptions
// with multipart/form-data. The provider returns JSON {"text": "..."}.
func (r *Router) callTranscribeOpenAI(ctx context.Context, p types.ProviderConfig, req types.TranscribeRequest) (string, string, error) {
	url := p.BaseURL + "/audio/transcriptions"

	// Build multipart body: file (audio bytes) + model + optional language + prompt.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	filename := req.Filename
	if filename == "" {
		filename = "audio.bin"
	}
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = mimeTypeFromFilename(filename)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", "", err
	}
	if _, err := fw.Write(req.Audio); err != nil {
		return "", "", err
	}
	if err := mw.WriteField("model", p.Model); err != nil {
		return "", "", err
	}
	if err := mw.WriteField("response_format", "json"); err != nil {
		return "", "", err
	}
	if req.Language != "" {
		if err := mw.WriteField("language", req.Language); err != nil {
			return "", "", err
		}
	}
	if req.Prompt != "" {
		if err := mw.WriteField("prompt", req.Prompt); err != nil {
			return "", "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second) // audio transcription can take a while
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, &buf)
	if err != nil {
		return "", "", err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return "", "", &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	// Response shape: {"text": "..."} (default json format).
	var parsed struct {
		Text     string `json:"text"`
		Language string `json:"language,omitempty"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", "", fmt.Errorf("bad transcription response: %s", types.Truncate(string(data), 200))
	}
	if strings.TrimSpace(parsed.Text) == "" {
		return "", "", errors.New("empty transcription (no speech detected?)")
	}
	return parsed.Text, parsed.Language, nil
}

// mimeTypeFromFilename guesses the audio MIME type from the extension. Used
// when the caller passes a filename but no explicit MimeType.
func mimeTypeFromFilename(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(lower, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(lower, ".m4a"):
		return "audio/mp4"
	case strings.HasSuffix(lower, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(lower, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(lower, ".webm"):
		return "audio/webm"
	default:
		return "application/octet-stream"
	}
}

// ----- Gemini STT (Files API upload + interactions endpoint) ---------------

// callTranscribeGemini implements the two-step Gemini Transcribe protocol:
//
//  1. Upload the audio file to the Files API:
//     POST {base}/upload/v1beta/files?uploadType=multipart
//     with a multipart/related body: metadata JSON + raw audio bytes.
//     Response: {"file": {"name": "files/abc", "uri": "...", "state": "PROCESSING"|"ACTIVE"}}
//
//  2. Poll the file state until ACTIVE (usually immediate for small files):
//     GET {base}/v1beta/files/{name}
//
//  3. POST the file URI to the interactions endpoint:
//     POST {base}/v1beta/interactions
//     body: {"model": "models/{model}", "input": [{"type": "file", "file_uri": "..."}]}
//     Response: {"output_text": "..."}
//
//  4. Clean up: DELETE the uploaded file so the user's Files storage doesn't fill.
//
// Auth: x-goog-api-key header (same as Gemini TTS).
func (r *Router) callTranscribeGemini(ctx context.Context, p types.ProviderConfig, req types.TranscribeRequest) (string, string, error) {
	filename := req.Filename
	if filename == "" {
		filename = "audio.bin"
	}
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = mimeTypeFromFilename(filename)
	}

	// Step 1: upload the file.
	fileName, fileURI, err := r.geminiUploadFile(ctx, p, req.Audio, mimeType, filename)
	if err != nil {
		return "", "", fmt.Errorf("gemini files upload: %w", err)
	}
	// Always clean up the uploaded file when we're done (best-effort).
	defer func() {
		// Use a fresh short context for the delete — don't fail the call if it errors.
		dCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.geminiDeleteFile(dCtx, p, fileName)
	}()

	// Step 2: poll until state == ACTIVE (or failed). Small files are usually
	// ACTIVE immediately, so this loop typically runs once.
	if err := r.geminiWaitForFile(ctx, p, fileName); err != nil {
		return "", "", fmt.Errorf("gemini files wait: %w", err)
	}

	// Step 3: POST the file URI to the interactions endpoint.
	text, err := r.geminiTranscribeInteractions(ctx, p, fileURI, req)
	if err != nil {
		return "", "", err
	}
	return text, "", nil
}

// geminiUploadFile uploads raw audio bytes to Gemini's Files API and returns
// the file's name (e.g. "files/abc123") and URI.
func (r *Router) geminiUploadFile(ctx context.Context, p types.ProviderConfig, audio []byte, mimeType, filename string) (fileName, fileURI string, err error) {
	url := p.BaseURL + "/upload/v1beta/files?uploadType=multipart"

	// Build the multipart/related body manually (mime/multipart produces
	// multipart/form-data, which is a different format).
	boundary := "-----airouter-gemini-" + fmt.Sprintf("%d", time.Now().UnixNano())
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: application/json\r\n\r\n")
	meta := map[string]any{"file": map[string]string{"display_name": filename}}
	mb, _ := jsonMarshal(meta)
	buf.Write(mb)
	buf.WriteString("\r\n")
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: %s\r\n\r\n", mimeType)
	buf.Write(audio)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, &buf)
	if err != nil {
		return "", "", err
	}
	httpReq.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("x-goog-api-key", k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return "", "", &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	var parsed struct {
		File struct {
			Name     string `json:"name"`
			URI      string `json:"uri"`
			State    string `json:"state"`
			MimeType string `json:"mimeType"`
		} `json:"file"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || parsed.File.Name == "" {
		return "", "", fmt.Errorf("bad upload response: %s", types.Truncate(string(data), 200))
	}
	return parsed.File.Name, parsed.File.URI, nil
}

// geminiWaitForFile polls GET /v1beta/files/{name} until state == ACTIVE or
// FAILED. Returns an error if the file fails to process or the context expires.
func (r *Router) geminiWaitForFile(ctx context.Context, p types.ProviderConfig, fileName string) error {
	url := p.BaseURL + "/v1beta/" + fileName // fileName is "files/abc123"
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		httpReq, _ := http.NewRequestWithContext(callCtx, http.MethodGet, url, nil)
		if k := p.ResolveKey(); k != "" {
			httpReq.Header.Set("x-goog-api-key", k)
		}
		resp, err := r.client.Do(httpReq)
		if err != nil {
			cancel()
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("poll file status HTTP %d: %s", resp.StatusCode, types.Truncate(string(data), 200))
		}
		var parsed struct {
			State string `json:"state"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &parsed)
		switch parsed.State {
		case "ACTIVE":
			return nil
		case "FAILED":
			if parsed.Error.Message != "" {
				return fmt.Errorf("file processing failed: %s", parsed.Error.Message)
			}
			return errors.New("file processing failed (state=FAILED)")
		}
		// Still PROCESSING — wait briefly and retry.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("timed out waiting for file to become ACTIVE")
}

// geminiTranscribeInteractions POSTs the file URI to /v1beta/interactions and
// returns the transcribed text from output_text.
func (r *Router) geminiTranscribeInteractions(ctx context.Context, p types.ProviderConfig, fileURI string, req types.TranscribeRequest) (string, error) {
	url := p.BaseURL + "/v1beta/interactions"
	body := map[string]any{
		"model": "models/" + p.Model,
		"input": []map[string]any{
			{"type": "file", "file_uri": fileURI},
		},
	}
	// Optional transcription config — only set fields the caller provided.
	transcriptionConfig := map[string]any{}
	if req.Language != "" {
		transcriptionConfig["language_codes"] = []string{req.Language}
	}
	if req.Prompt != "" {
		transcriptionConfig["custom_vocabulary"] = strings.Split(req.Prompt, ",")
	}
	if len(transcriptionConfig) > 0 {
		body["generation_config"] = map[string]any{"transcription_config": transcriptionConfig}
	}

	raw, _ := jsonMarshal(body)
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second) // transcribing long audio can take a while
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytesReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("x-goog-api-key", k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return "", &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	var parsed struct {
		OutputText string `json:"output_text"`
		Error      struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("bad transcribe response: %s", types.Truncate(string(data), 200))
	}
	if parsed.Error.Message != "" {
		return "", fmt.Errorf("gemini transcribe error: %s", parsed.Error.Message)
	}
	if strings.TrimSpace(parsed.OutputText) == "" {
		return "", errors.New("empty transcription (no speech detected?)")
	}
	return parsed.OutputText, nil
}

// geminiDeleteFile removes an uploaded file from the Files API. Best-effort —
// errors are ignored by the caller (it runs in a defer).
func (r *Router) geminiDeleteFile(ctx context.Context, p types.ProviderConfig, fileName string) error {
	if fileName == "" {
		return nil
	}
	url := p.BaseURL + "/v1beta/" + fileName
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("x-goog-api-key", k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ----- audio seconds estimation --------------------------------------------

// estimateSTTAudioSeconds estimates the input audio duration in seconds from
// the byte count and filename/MIME type. Used for ASH/ASD tracking because
// Groq's default json response format does not include the audio duration.
//
// For WAV files we parse the RIFF header for an exact duration. For everything
// else (MP3, M4A, OGG, FLAC, WebM) we fall back to a bytes-per-second
// heuristic based on a typical compressed-audio bitrate (16 KB/s ≈ 128 kbps).
//
// The 90% safety margin in acquire absorbs the error.
func estimateSTTAudioSeconds(audio []byte, filename, mimeType string) int {
	if len(audio) == 0 {
		return 0
	}
	// Try WAV header parsing first.
	if secs := wavDurationSeconds(audio); secs > 0 {
		return secs
	}
	// Fall back to byte-count heuristic: assume ~16 KB/s (128 kbps compressed).
	secs := len(audio) / 16000
	if secs < 1 {
		secs = 1
	}
	return secs
}

// wavDurationSeconds parses a RIFF/WAV header and returns the audio duration
// in seconds. Returns 0 if the bytes don't look like a WAV file or the header
// is incomplete. WAV header layout (little-endian):
//
//	0-3:   "RIFF"
//	4-7:   file size - 8
//	8-11:  "WAVE"
//	12-15: "fmt "
//	16-19: fmt chunk size (16 for PCM)
//	20-21: audio format (1 = PCM)
//	22-23: num channels
//	24-27: sample rate
//	28-31: byte rate (sample_rate * channels * bits/8)
//	32-33: block align
//	34-35: bits per sample
//	… then "data" chunk: 36-39: "data", 40-43: data size
//
// duration = data_size / byte_rate
func wavDurationSeconds(audio []byte) int {
	if len(audio) < 44 {
		return 0
	}
	if string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return 0
	}
	// Find the "data" chunk — it may not be at offset 36 if there are other chunks.
	dataStart := -1
	for i := 12; i+8 <= len(audio); {
		chunkID := string(audio[i : i+4])
		chunkSize := int(audio[i+4]) | int(audio[i+5])<<8 | int(audio[i+6])<<16 | int(audio[i+7])<<24
		if chunkID == "data" {
			dataStart = i + 8
			break
		}
		// Chunks are word-aligned: skip chunkSize bytes, plus a pad byte if odd.
		i += 8 + chunkSize
		if chunkSize&1 == 1 {
			i++
		}
	}
	if dataStart < 0 {
		return 0
	}
	// sample rate is at offset 24, byte rate at offset 28, both little-endian.
	sampleRate := int(audio[24]) | int(audio[25])<<8 | int(audio[26])<<16 | int(audio[27])<<24
	byteRate := int(audio[28]) | int(audio[29])<<8 | int(audio[30])<<16 | int(audio[31])<<24
	if byteRate <= 0 || sampleRate <= 0 {
		return 0
	}
	// Data size is the 4 bytes just before dataStart.
	dataSizeOffset := dataStart - 4
	if dataSizeOffset+4 > len(audio) {
		return 0
	}
	dataSize := int(audio[dataSizeOffset]) | int(audio[dataSizeOffset+1])<<8 | int(audio[dataSizeOffset+2])<<16 | int(audio[dataSizeOffset+3])<<24
	if dataSize <= 0 {
		// Maybe the data size was not set (some encoders write 0). Fall back to
		// "rest of file from dataStart".
		dataSize = len(audio) - dataStart
	}
	secs := dataSize / byteRate
	if secs < 1 {
		secs = 1
	}
	return secs
}

// jsonMarshal wraps json.Marshal so we don't add a second import line.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
