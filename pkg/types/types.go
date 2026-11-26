// Package types holds the public data shapes used across the airouter packages:
// provider configs, tenants, requests, responses and the error types returned by
// the router. Keeping them in one small package lets pkg/store, pkg/router,
// pkg/preset and pkg/server share them without circular imports.
package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultTenantID is the tenant used when no tenant is specified (CLI / admin use).
const DefaultTenantID = "default"

// ProviderConfig describes one model on one provider. Every entry has its OWN
// limits, so "gemini-2.5-flash" and "gemma-3-27b" on the same Gemini key are
// two entries with two quotas.
type ProviderConfig struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	TenantID string `json:"tenant_id,omitempty"` // "" = shared by every tenant
	Preset   string `json:"preset,omitempty"`    // input only: fills defaults from the catalog

	// Upstream connection ----------------------------------------------------
	Kind      string `json:"kind"`                  // "openai" (any OpenAI-compatible API) | "ollama"
	BaseURL   string `json:"base_url"`              // e.g. https://api.groq.com/openai/v1
	APIKeyEnv string `json:"api_key_env,omitempty"` // env var holding the key (preferred)
	APIKey    string `json:"api_key,omitempty"`     // or stored in the DB (never returned by the API)
	Model     string `json:"model"`                 // exact model id from the provider's console

	// Local limits -----------------------------------------------------------
	Priority int `json:"priority"` // lower = tried first
	RPM      int `json:"rpm"`      // requests per minute (0 = no local cap)
	RPD      int `json:"rpd"`      // requests per day    (0 = no local cap)
	TPM      int `json:"tpm"`      // tokens per minute   (0 = no local cap)
	ASH      int `json:"ash"`      // audio seconds per hour (TTS/STT only, 0 = no local cap)
	ASD      int `json:"asd"`      // audio seconds per day  (TTS/STT only, 0 = no local cap)
	Workers  int `json:"workers"`  // max concurrent requests to this model

	// Behaviour flags --------------------------------------------------------
	ResetTZ    string `json:"reset_tz"`              // IANA zone where the daily quota resets
	JSONMode   *bool  `json:"json_mode,omitempty"`   // nil/true = send response_format json; false for e.g. Gemma
	SystemRole *bool  `json:"system_role,omitempty"` // nil/true = send system messages; false folds them into the user turn
	Category   string `json:"category,omitempty"`    // "text" (default) | "tts" | "stt" | "embed" — which request shape this model expects
	Disabled   bool   `json:"disabled,omitempty"`

	// Input-only seed --------------------------------------------------------
	UsedTodayHint int `json:"used_today_hint,omitempty"` // requests already used today (only used when adding)
}

// ResolveKey returns the key for this provider, preferring the env var named
// in APIKeyEnv (so keys do not have to live in the DB) and falling back to the
// literal APIKey field. Returns "" for providers that need no key (e.g. Ollama).
func (p ProviderConfig) ResolveKey() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

// NeedsKey reports whether calls to this provider must carry an API key.
// Ollama (local) does not.
func (p ProviderConfig) NeedsKey() bool { return p.Kind != "ollama" }

// HasKey reports whether the provider is usable from a credentials point of
// view: either it needs no key, or a key is available (env var or DB).
func (p ProviderConfig) HasKey() bool { return !p.NeedsKey() || p.ResolveKey() != "" }

// KeySource says where the key comes from: "env", "db", "none" (missing) or
// "not needed". It never reveals the key itself.
func (p ProviderConfig) KeySource() string {
	switch {
	case p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) != "":
		return "env"
	case p.APIKey != "":
		return "db"
	case !p.NeedsKey():
		return "not needed"
	}
	return "none"
}

// KeyHint is a masked fingerprint ("…a1b2") safe to show in UIs and logs.
func (p ProviderConfig) KeyHint() string {
	k := p.ResolveKey()
	if len(k) < 8 {
		return ""
	}
	return "…" + k[len(k)-4:]
}

// UseJSONMode reports whether the provider supports OpenAI-style response_format json.
func (p ProviderConfig) UseJSONMode() bool { return p.JSONMode == nil || *p.JSONMode }

// UseSystemRole reports whether the provider accepts a dedicated system role.
func (p ProviderConfig) UseSystemRole() bool { return p.SystemRole == nil || *p.SystemRole }

// Validate normalises fields and returns an error if anything required is missing.
func (p *ProviderConfig) Validate() error {
	p.Name, p.Kind = strings.TrimSpace(p.Name), strings.ToLower(strings.TrimSpace(p.Kind))
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Kind != "openai" && p.Kind != "ollama" {
		return fmt.Errorf("kind must be openai or ollama, got %q", p.Kind)
	}
	if p.BaseURL == "" && p.Kind == "ollama" {
		p.BaseURL = "http://localhost:11434"
	}
	if p.BaseURL == "" {
		return errors.New("base_url is required")
	}
	if p.Model == "" {
		return errors.New("model is required (copy the exact id from the provider's console)")
	}
	if p.ResetTZ == "" {
		p.ResetTZ = "UTC"
	}
	if _, err := time.LoadLocation(p.ResetTZ); err != nil {
		return fmt.Errorf("unknown reset_tz %q", p.ResetTZ)
	}
	if p.Workers <= 0 {
		p.Workers = 2
	}
	if p.Priority == 0 {
		p.Priority = 100
	}
	if p.Category == "" {
		p.Category = CategoryText
	}
	p.BaseURL = strings.TrimRight(p.BaseURL, "/")
	return nil
}

// Tenant is one user of the router. Each tenant has its own API key, optional
// daily request cap and its own usage numbers. Provider keys are shared.
type Tenant struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	TZ                string    `json:"tz"`                      // tenant's own day boundary for its daily cap
	DailyRequestLimit int       `json:"daily_request_limit"`     // 0 = unlimited
	PoolOnlyCap       bool      `json:"pool_only_cap,omitempty"` // true: the cap counts only requests served by the shared pool; the tenant's own provider keys are never counted
	Disabled          bool      `json:"disabled,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// Message is one chat message sent to a provider.
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// Request is what callers send. Prompt and Messages are alternatives; System is optional.
type Request struct {
	TenantID    string        `json:"tenant_id,omitempty"`
	System      string        `json:"system,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`
	Messages    []Message     `json:"messages,omitempty"` // alternative to Prompt
	JSON        bool          `json:"json,omitempty"`     // ask for (and validate) a JSON answer
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Provider    string        `json:"provider,omitempty"` // pin one provider by name
	Strategy    string        `json:"strategy,omitempty"` // "priority" (default) | "spread"
	MaxWait     time.Duration `json:"-"`                  // how long to wait for a briefly rate-limited provider before giving up; 0 = the router default
	Category    string        `json:"category,omitempty"` // internal: filter providers by category; defaults to "text". Callers usually leave this empty.
}

// Attempt records one provider that was tried and failed before the successful one.
type Attempt struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
	Status   int    `json:"status,omitempty"` // upstream HTTP status, when there was one
	Kind     string `json:"kind,omitempty"`   // auth | rate_limit | daily_limit | billing | bad_request | timeout | transient | bad_response
}

// Response is what the router returns on success.
type Response struct {
	Text      string          `json:"text"`
	JSON      json.RawMessage `json:"json,omitempty"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Tokens    int             `json:"tokens"`
	LatencyMS int64           `json:"latency_ms"`
	Own       bool            `json:"own"` // true when the tenant's own provider key answered; false = the shared pool
	Attempts  []Attempt       `json:"attempts,omitempty"`
}

// ----- errors ---------------------------------------------------------------

var (
	// ErrNoProviders is returned when no enabled provider matches the request.
	ErrNoProviders = errors.New("airouter: no enabled providers for this tenant")
	// ErrUnknownTenant is returned when the request names a tenant that does not exist.
	ErrUnknownTenant = errors.New("airouter: unknown tenant")
	// ErrTenantDisabled is returned when the tenant exists but is disabled.
	ErrTenantDisabled = errors.New("airouter: tenant disabled")
	// ErrUnauthorized is returned when the API key does not match any tenant.
	ErrUnauthorized = errors.New("airouter: invalid api key")
	// ErrBadRequest marks a request the caller must fix (empty prompt, ...).
	// Test with errors.Is.
	ErrBadRequest = errors.New("airouter: bad request")
	// ErrTimeout means the whole request ran out of time (Options.RequestTimeout).
	ErrTimeout = errors.New("airouter: request timed out")
)

// BadRequestError is returned when every provider that was tried rejected the
// request itself (HTTP 400/413/422). That is the caller's problem, not a
// provider outage, so it is never retried and never held against a provider.
type BadRequestError struct {
	Message  string
	Attempts []Attempt
}

func (e *BadRequestError) Error() string   { return "airouter: bad request: " + e.Message }
func (e *BadRequestError) Is(t error) bool { return t == ErrBadRequest }

// ExhaustedError means every provider is out of quota / cooling down right now.
// RetryAfter is how long the caller should wait before trying again.
type ExhaustedError struct {
	RetryAfter time.Duration
	Reasons    map[string]string
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("airouter: all providers are limited right now (retry in %s)", e.RetryAfter.Round(time.Second))
}

// AllFailedError means providers were tried and each one failed (network error,
// bad response, etc.) — not a quota problem.
type AllFailedError struct{ Attempts []Attempt }

func (e *AllFailedError) Error() string {
	parts := make([]string, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		parts = append(parts, a.Provider+": "+a.Error)
	}
	return "airouter: all providers failed: " + strings.Join(parts, "; ")
}

// TenantLimitError means the tenant's own daily request cap is used up.
type TenantLimitError struct{ RetryAfter time.Duration }

func (e *TenantLimitError) Error() string { return "airouter: tenant daily request limit reached" }

// HTTPError is an upstream (provider) HTTP error, surfaced for inspection.
type HTTPError struct {
	Code       int
	Body       string
	RetryAfter time.Duration
	Header     http.Header // upstream response headers (rate-limit info lives here)
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Code, Truncate(e.Body, 200))
}

// Truncate trims s to n characters, appending "..." when it was longer.
// Exported because the CLI and server use it to format error bodies.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ----- speak (text-to-speech) -----------------------------------------------

// SpeakRequest is the input to Router.Speak. Text is required; Voice and
// Format are optional with provider-specific defaults.
type SpeakRequest struct {
	TenantID string        `json:"tenant_id,omitempty"`
	Text     string        `json:"text"`               // the text to synthesize
	Voice    string        `json:"voice,omitempty"`    // provider-specific voice id (e.g. "Kore" for Gemini, "tara" for Groq Orpheus)
	Format   string        `json:"format,omitempty"`   // audio format: "wav" (default), "mp3", "pcm" — provider may not support all
	Language string        `json:"language,omitempty"` // optional language hint (e.g. "en", "ar")
	Provider string        `json:"provider,omitempty"` // pin one provider by name
	MaxWait  time.Duration `json:"-"`                  // wait limit for a briefly rate-limited provider; 0 = router default

	// ElevenLabs only (ignored by other providers).
	// Voice may be a voice id OR a voice name (looked up case-insensitively among
	// the voices this tenant can use, including its own clones).
	Model         string         `json:"model,omitempty"`          // override the provider's model per request, e.g. "eleven_flash_v2_5"
	VoiceSettings *VoiceSettings `json:"voice_settings,omitempty"` // per-request tuning of the voice
}

// VoiceSettings tunes an ElevenLabs voice for one request. Leave a field nil
// to keep the voice's saved value.
type VoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`        // 0..1, lower = more expressive
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"` // 0..1, higher = closer to the original voice
	Style           *float64 `json:"style,omitempty"`            // 0..1
	Speed           *float64 `json:"speed,omitempty"`            // about 0.7..1.2
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
}

// EmbedRequest asks for one embedding vector per input text, from an
// embedding-capable provider (category "embed", OpenAI-compatible /embeddings).
type EmbedRequest struct {
	TenantID string        `json:"tenant_id,omitempty"`
	Texts    []string      `json:"texts"`
	Provider string        `json:"provider,omitempty"` // pin one provider by name
	MaxWait  time.Duration `json:"-"`                  // wait limit for a briefly rate-limited provider; 0 = router default
	// Dimensions asks for vectors of exactly this size (sent as "dimensions") and
	// REJECTS any provider that answers with a different size, failing over to the
	// next one. Set it whenever the vectors go into an index: vectors of different
	// sizes, or from different models, cannot be compared with each other.
	Dimensions int `json:"dimensions,omitempty"`
}

// EmbedResponse holds Vectors in the same order as EmbedRequest.Texts.
type EmbedResponse struct {
	Vectors    [][]float32 `json:"vectors"`
	Dimensions int         `json:"dimensions"` // size of every vector; store it with the index
	Tokens     int         `json:"tokens"`     // estimated input tokens (about 4 characters each)
	Provider   string      `json:"provider"`
	Model      string      `json:"model"`         // store this with the index: only compare vectors from the same model
	Own        bool        `json:"own,omitempty"` // the tenant's own key served it
	LatencyMS  int64       `json:"latency_ms"`
	Attempts   []Attempt   `json:"attempts,omitempty"`
}

// SpeakResponse is the output of Router.Speak. Audio is the raw audio bytes
// (WAV by default); ContentType is the MIME type to send with the bytes.
type SpeakResponse struct {
	Audio       []byte    `json:"-"`
	AudioB64    string    `json:"audio_base64,omitempty"` // base64 of Audio, only set by the HTTP JSON response shape
	ContentType string    `json:"content_type"`           // e.g. "audio/wav", "audio/mpeg"
	Format      string    `json:"format"`                 // "wav" | "mp3" | "pcm"
	Provider    string    `json:"provider"`
	Own         bool      `json:"own"` // the tenant's own key answered (not the shared pool)
	Model       string    `json:"model"`
	Voice       string    `json:"voice,omitempty"`
	LatencyMS   int64     `json:"latency_ms"`
	Attempts    []Attempt `json:"attempts,omitempty"`
}

// ----- transcribe (speech-to-text) ------------------------------------------

// TranscribeRequest is the input to Router.Transcribe. Audio bytes plus a
// filename (used for MIME detection) are required.
type TranscribeRequest struct {
	TenantID string        `json:"tenant_id,omitempty"`
	Audio    []byte        `json:"-"`                   // raw audio bytes
	Filename string        `json:"filename,omitempty"`  // e.g. "audio.mp3" — used to detect MIME type
	MimeType string        `json:"mime_type,omitempty"` // optional explicit override
	Language string        `json:"language,omitempty"`  // optional ISO-639-1 language hint (e.g. "en")
	Prompt   string        `json:"prompt,omitempty"`    // optional style/spelling prompt (max 224 tokens)
	Provider string        `json:"provider,omitempty"`  // pin one provider by name
	MaxWait  time.Duration `json:"-"`                   // wait limit for a briefly rate-limited provider; 0 = router default
}

// TranscribeResponse is the output of Router.Transcribe.
type TranscribeResponse struct {
	Text      string    `json:"text"`
	Provider  string    `json:"provider"`
	Own       bool      `json:"own"` // the tenant's own key answered (not the shared pool)
	Model     string    `json:"model"`
	Language  string    `json:"language,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
	Attempts  []Attempt `json:"attempts,omitempty"`
}

// Category constants — kept here (as well as in pkg/preset) so the router and
// callers can filter without importing preset.
const (
	CategoryText = "text"
	CategoryTTS  = "tts"
	CategorySTT  = "stt"
	// CategoryEmbed = text embeddings (OpenAI-compatible /embeddings). Routed via r.Embed.
	CategoryEmbed = "embed"
)

// ErrNotFound is returned by Store/Router helpers when an id does not match anything.
var ErrNotFound = errors.New("not found")

// ---- Voices (ElevenLabs) -----------------------------------------------------

// Voice is one usable voice.
type Voice struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Category    string            `json:"category,omitempty"` // premade | cloned | generated | professional ...
	Description string            `json:"description,omitempty"`
	PreviewURL  string            `json:"preview_url,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"` // gender, accent, age, language ...
	Provider    string            `json:"provider"`         // the provider (and so the account) the voice lives in
	Own         bool              `json:"own"`              // lives in the tenant's own ElevenLabs account
	Cloned      bool              `json:"cloned"`           // cloned through airouter by this tenant
	Shared      bool              `json:"shared,omitempty"` // an operator voice the operator shares with every tenant
	// RequiresVerification: ElevenLabs wants a verification step before this voice can be used.
	RequiresVerification bool `json:"requires_verification,omitempty"`
}

// VoiceSample is one audio recording used to clone a voice.
type VoiceSample struct {
	Filename string
	Data     []byte
}

// CloneVoiceRequest asks for a cloned voice (ElevenLabs Instant Voice Cloning).
type CloneVoiceRequest struct {
	TenantID    string
	Name        string
	Description string
	Samples     []VoiceSample // 1 to 25 clean recordings of the speaker
	Labels      map[string]string
	Provider    string // pin one ElevenLabs provider by name
	// RemoveBackgroundNoise runs ElevenLabs' audio isolation on the samples.
	// It can make clean recordings worse.
	RemoveBackgroundNoise bool
	// ConsentConfirmed must be true: the caller states they have the speaker's
	// permission (or the rights) to clone this voice.
	ConsentConfirmed bool
}

// VoiceRecord remembers who cloned a voice, so tenants never see or use each
// other's clones even when the clone lives in a shared ElevenLabs account.
type VoiceRecord struct {
	VoiceID     string    `json:"voice_id"`
	TenantID    string    `json:"tenant_id"`
	ProviderID  int64     `json:"provider_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Shared: only meaningful for the operator (default tenant). A shared
	// operator voice can be listed and spoken by every tenant; the default is
	// private to the operator. Tenants' own clones are never shared.
	Shared bool `json:"shared,omitempty"`
}

var (
	// ErrConsentRequired: cloning needs CloneVoiceRequest.ConsentConfirmed.
	ErrConsentRequired = errors.New("airouter: voice cloning needs your confirmation that you have the speaker's permission (CLI: -consent, HTTP: consent=true, Go: ConsentConfirmed=true)")
	// ErrVoiceCloneNotAllowed: the tenant has no ElevenLabs key of its own and the operator did not allow cloning on the shared account.
	ErrVoiceCloneNotAllowed = errors.New("airouter: voice cloning needs your own ElevenLabs key (add one with provider add -preset elevenlabs)")
	// ErrVoiceNotFound: no such voice for this tenant (also returned for another tenant's voice).
	ErrVoiceNotFound = errors.New("airouter: voice not found")
)
