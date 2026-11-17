// Package types holds the public data shapes used across the airouter packages:
// provider configs, tenants, requests, responses and the error types returned by
// the router. Keeping them in one small package lets pkg/store, pkg/router,
// pkg/preset and pkg/server share them without circular imports.
package types

import (
        "encoding/json"
        "errors"
        "fmt"
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
        Category   string `json:"category,omitempty"`    // "text" (default) | "tts" | "stt" — which request shape this model expects
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
        TZ                string    `json:"tz"`                  // tenant's own day boundary for its daily cap
        DailyRequestLimit int       `json:"daily_request_limit"` // 0 = unlimited
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
        TenantID    string    `json:"tenant_id,omitempty"`
        System      string    `json:"system,omitempty"`
        Prompt      string    `json:"prompt,omitempty"`
        Messages    []Message `json:"messages,omitempty"` // alternative to Prompt
        JSON        bool      `json:"json,omitempty"`     // ask for (and validate) a JSON answer
        MaxTokens   int       `json:"max_tokens,omitempty"`
        Temperature *float64  `json:"temperature,omitempty"`
        Provider    string    `json:"provider,omitempty"` // pin one provider by name
        Strategy    string    `json:"strategy,omitempty"` // "priority" (default) | "spread"
        Category    string    `json:"category,omitempty"` // internal: filter providers by category; defaults to "text". Callers usually leave this empty.
}

// Attempt records one provider that was tried and failed before the successful one.
type Attempt struct {
        Provider string `json:"provider"`
        Error    string `json:"error"`
}

// Response is what the router returns on success.
type Response struct {
        Text      string          `json:"text"`
        JSON      json.RawMessage `json:"json,omitempty"`
        Provider  string          `json:"provider"`
        Model     string          `json:"model"`
        Tokens    int             `json:"tokens"`
        LatencyMS int64           `json:"latency_ms"`
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
)

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
        TenantID string `json:"tenant_id,omitempty"`
        Text     string `json:"text"`           // the text to synthesize
        Voice    string `json:"voice,omitempty"` // provider-specific voice id (e.g. "Kore" for Gemini, "tara" for Groq Orpheus)
        Format   string `json:"format,omitempty"`// audio format: "wav" (default), "mp3", "pcm" — provider may not support all
        Language string `json:"language,omitempty"` // optional language hint (e.g. "en", "ar")
        Provider string `json:"provider,omitempty"` // pin one provider by name
}

// SpeakResponse is the output of Router.Speak. Audio is the raw audio bytes
// (WAV by default); ContentType is the MIME type to send with the bytes.
type SpeakResponse struct {
        Audio      []byte    `json:"-"`
        AudioB64   string    `json:"audio_base64,omitempty"` // base64 of Audio, only set by the HTTP JSON response shape
        ContentType string   `json:"content_type"`            // e.g. "audio/wav", "audio/mpeg"
        Format     string    `json:"format"`                  // "wav" | "mp3" | "pcm"
        Provider   string    `json:"provider"`
        Model      string    `json:"model"`
        Voice      string    `json:"voice,omitempty"`
        LatencyMS  int64     `json:"latency_ms"`
        Attempts   []Attempt `json:"attempts,omitempty"`
}

// ----- transcribe (speech-to-text) ------------------------------------------

// TranscribeRequest is the input to Router.Transcribe. Audio bytes plus a
// filename (used for MIME detection) are required.
type TranscribeRequest struct {
        TenantID string `json:"tenant_id,omitempty"`
        Audio    []byte `json:"-"`                 // raw audio bytes
        Filename string `json:"filename,omitempty"`// e.g. "audio.mp3" — used to detect MIME type
        MimeType string `json:"mime_type,omitempty"`// optional explicit override
        Language string `json:"language,omitempty"` // optional ISO-639-1 language hint (e.g. "en")
        Prompt   string `json:"prompt,omitempty"`   // optional style/spelling prompt (max 224 tokens)
        Provider string `json:"provider,omitempty"` // pin one provider by name
}

// TranscribeResponse is the output of Router.Transcribe.
type TranscribeResponse struct {
        Text      string    `json:"text"`
        Provider  string    `json:"provider"`
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
)

// ErrNotFound is returned by Store/Router helpers when an id does not match anything.
var ErrNotFound = errors.New("not found")
