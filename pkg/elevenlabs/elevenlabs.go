// Package elevenlabs is a small, dependency-free client for the ElevenLabs
// REST API: text to speech, speech to text, listing voices, and Instant Voice
// Cloning. It works on its own, and the airouter router uses it for the
// "elevenlabs" provider.
//
// API reference: https://elevenlabs.io/docs/api-reference
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the global API host. Accounts with data residency use
// https://api.us.elevenlabs.io, https://api.eu.residency.elevenlabs.io, etc.
const DefaultBaseURL = "https://api.elevenlabs.io"

// Client talks to one ElevenLabs account (one API key).
type Client struct {
	BaseURL string       // default DefaultBaseURL, no trailing slash, no /v1
	APIKey  string       // sent as the xi-api-key header
	HTTP    *http.Client // default: 90 second timeout
}

// APIError is a non-2xx answer from ElevenLabs.
type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("elevenlabs: HTTP %d: %s", e.Status, truncate(e.Body, 300))
}

// IsVoiceNotFound reports whether the error means the voice id does not exist
// in this account (as opposed to a bad key or a bad model).
func (e *APIError) IsVoiceNotFound() bool {
	b := strings.ToLower(e.Body)
	return e.Status == 404 && strings.Contains(b, "voice")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (c *Client) base() string {
	b := strings.TrimRight(c.BaseURL, "/")
	if b == "" {
		b = DefaultBaseURL
	}
	return b
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, limit int64) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("xi-api-key", c.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode/100 != 2 {
		ra, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return nil, nil, &APIError{Status: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second}
	}
	return data, resp.Header, nil
}

// ---- Text to speech ---------------------------------------------------------

// VoiceSettings tunes a voice for one request. Nil fields keep the voice's saved value.
type VoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
}

// SpeakOptions is one text-to-speech request.
type SpeakOptions struct {
	VoiceID      string         // required
	Text         string         // required
	ModelID      string         // e.g. eleven_multilingual_v2 (default of the API), eleven_flash_v2_5
	OutputFormat string         // codec_rate_bitrate, e.g. mp3_44100_128, opus_48000_64, wav_24000, pcm_24000. Default mp3_44100_128
	LanguageCode string         // ISO 639-1, optional
	Settings     *VoiceSettings // optional
}

// Speak converts text to audio. It returns the audio bytes and their Content-Type.
func (c *Client) Speak(ctx context.Context, o SpeakOptions) ([]byte, string, error) {
	if o.VoiceID == "" || strings.TrimSpace(o.Text) == "" {
		return nil, "", fmt.Errorf("elevenlabs: VoiceID and Text are required")
	}
	body := map[string]any{"text": o.Text}
	if o.ModelID != "" {
		body["model_id"] = o.ModelID
	}
	if o.LanguageCode != "" {
		body["language_code"] = o.LanguageCode
	}
	if o.Settings != nil {
		body["voice_settings"] = o.Settings
	}
	raw, _ := json.Marshal(body)
	path := "/v1/text-to-speech/" + url.PathEscape(o.VoiceID)
	if o.OutputFormat != "" {
		path += "?output_format=" + url.QueryEscape(o.OutputFormat)
	}
	data, hdr, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json", 32<<20)
	if err != nil {
		return nil, "", err
	}
	return data, hdr.Get("Content-Type"), nil
}

// ---- Voices ------------------------------------------------------------------

// Voice is one voice in the account.
type Voice struct {
	VoiceID     string            `json:"voice_id"`
	Name        string            `json:"name"`
	Category    string            `json:"category"`
	Description string            `json:"description"`
	PreviewURL  string            `json:"preview_url"`
	Labels      map[string]string `json:"labels"`
}

// Voices lists the voices the account can use. search filters by name, label or category.
func (c *Client) Voices(ctx context.Context, search string) ([]Voice, error) {
	var all []Voice
	token := ""
	for page := 0; page < 20; page++ { // hard stop, in case the API loops
		q := url.Values{"page_size": {"100"}}
		if search != "" {
			q.Set("search", search)
		}
		if token != "" {
			q.Set("next_page_token", token)
		}
		data, _, err := c.do(ctx, http.MethodGet, "/v2/voices?"+q.Encode(), nil, "", 8<<20)
		if err != nil {
			return nil, err
		}
		var out struct {
			Voices        []Voice `json:"voices"`
			HasMore       bool    `json:"has_more"`
			NextPageToken string  `json:"next_page_token"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("elevenlabs: bad voices response: %w", err)
		}
		all = append(all, out.Voices...)
		if !out.HasMore || out.NextPageToken == "" {
			break
		}
		token = out.NextPageToken
	}
	return all, nil
}

// Sample is one audio recording for cloning.
type Sample struct {
	Filename string
	Data     []byte
}

// CloneOptions describes an Instant Voice Clone.
type CloneOptions struct {
	Name                  string
	Description           string
	Samples               []Sample // 1..25 recordings
	Labels                map[string]string
	RemoveBackgroundNoise bool
}

// CloneResult is the answer to a clone request.
type CloneResult struct {
	VoiceID              string `json:"voice_id"`
	RequiresVerification bool   `json:"requires_verification"`
}

// CloneVoice creates an Instant Voice Clone (POST /v1/voices/add). It needs a
// paid ElevenLabs plan that includes instant voice cloning.
func (c *Client) CloneVoice(ctx context.Context, o CloneOptions) (*CloneResult, error) {
	if strings.TrimSpace(o.Name) == "" || len(o.Samples) == 0 {
		return nil, fmt.Errorf("elevenlabs: Name and at least one sample are required")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", o.Name)
	if o.Description != "" {
		_ = mw.WriteField("description", o.Description)
	}
	if o.RemoveBackgroundNoise {
		_ = mw.WriteField("remove_background_noise", "true")
	}
	if len(o.Labels) > 0 {
		lb, _ := json.Marshal(o.Labels)
		_ = mw.WriteField("labels", string(lb))
	}
	for i, s := range o.Samples {
		name := s.Filename
		if name == "" {
			name = "sample" + strconv.Itoa(i+1) + ".mp3"
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename=%q`, name))
		h.Set("Content-Type", "application/octet-stream")
		fw, err := mw.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(s.Data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	data, _, err := c.do(ctx, http.MethodPost, "/v1/voices/add", &buf, mw.FormDataContentType(), 1<<20)
	if err != nil {
		return nil, err
	}
	var res CloneResult
	if err := json.Unmarshal(data, &res); err != nil || res.VoiceID == "" {
		return nil, fmt.Errorf("elevenlabs: bad clone response: %s", truncate(string(data), 200))
	}
	return &res, nil
}

// DeleteVoice removes a voice from the account.
func (c *Client) DeleteVoice(ctx context.Context, voiceID string) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/v1/voices/"+url.PathEscape(voiceID), nil, "", 1<<20)
	return err
}

// ---- Speech to text ------------------------------------------------------------

// Transcribe converts audio to text with ElevenLabs Scribe. modelID defaults to scribe_v1.
// It returns the text and the detected (or given) language code.
func (c *Client) Transcribe(ctx context.Context, audio []byte, filename, mimeType, modelID, languageCode string) (text, language string, err error) {
	if modelID == "" {
		modelID = "scribe_v1"
	}
	if filename == "" {
		filename = "audio.bin"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model_id", modelID)
	if languageCode != "" {
		_ = mw.WriteField("language_code", languageCode)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	h.Set("Content-Type", mimeType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		return "", "", err
	}
	if _, err := fw.Write(audio); err != nil {
		return "", "", err
	}
	if err := mw.Close(); err != nil {
		return "", "", err
	}
	data, _, err := c.do(ctx, http.MethodPost, "/v1/speech-to-text", &buf, mw.FormDataContentType(), 8<<20)
	if err != nil {
		return "", "", err
	}
	var out struct {
		Text         string `json:"text"`
		LanguageCode string `json:"language_code"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", fmt.Errorf("elevenlabs: bad transcription response: %w", err)
	}
	return out.Text, out.LanguageCode, nil
}
