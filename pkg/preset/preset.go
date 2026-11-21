// Package preset contains the built-in provider catalog: connection defaults
// (base URL, env var name, timezone) for every OpenAI-compatible and Ollama
// provider we know about, plus the "free pool" — a curated list of known-free
// model IDs with their current RPM/RPD/TPM limits and a Category so callers
// can filter (e.g. only chat-capable models) without inspecting each row.
//
// Presets deliberately contain ONLY connection defaults and behaviour flags,
// never model ids or limits: those live in FreePool because they change more
// often. Limits in FreePool carry a "verified" date and a source — when in
// doubt, re-check the provider's console.
package preset

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Definition fills in base_url / key env var / sensible flags for one provider.
// Limits are NOT prefilled on purpose — free tiers change often. Use FreePool
// for known-good (provider, model, limits) tuples.
type Definition struct {
	Kind       string // "openai" | "ollama"
	BaseURL    string
	APIKeyEnv  string // env var name (empty for ollama, which needs no key)
	ResetTZ    string // IANA zone where the daily quota resets
	JSONMode   *bool  // nil = default true; false for models that reject response_format
	SystemRole *bool  // nil = default true; false for models that reject system role
	Category   string // "text" (default) | "tts" | "stt" — which request shape this preset's models expect
}

// Definitions is the catalog of provider connection defaults.
//
// To add a new OpenAI-compatible provider, add one entry here and users can
// immediately do `provider add -preset yourname -model ...`.
var Definitions = map[string]Definition{
	"groq": {
		Kind:      "openai",
		BaseURL:   "https://api.groq.com/openai/v1",
		APIKeyEnv: "GROQ_API_KEY",
		ResetTZ:   "UTC",
	},
	"gemini": {
		Kind:      "openai",
		BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKeyEnv: "GEMINI_API_KEY",
		ResetTZ:   "America/Los_Angeles", // Google's free tier resets at PT midnight
	},
	"gemma": { // Gemma via Gemini API: no JSON mode, no system role
		Kind:       "openai",
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKeyEnv:  "GEMINI_API_KEY",
		ResetTZ:    "America/Los_Angeles",
		JSONMode:   boolPtr(false),
		SystemRole: boolPtr(false),
	},
	"gemini-tts": { // Gemini native generateContent endpoint for TTS + STT (transcribe).
		// Different request shape from OpenAI-compat /chat/completions: r.Speak uses
		// POST {base}/models/{model}:generateContent with audio modality config.
		// r.Transcribe on Gemini is NOT yet implemented (needs Files API upload).
		Kind:       "openai",
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta",
		APIKeyEnv:  "GEMINI_API_KEY",
		ResetTZ:    "America/Los_Angeles",
		JSONMode:   boolPtr(false),
		SystemRole: boolPtr(false),
		Category:   CategoryTTS, // default for this preset; individual FreePool entries can override
	},
	"openrouter": {
		Kind:      "openai",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
		ResetTZ:   "UTC",
	},
	"mistral": {
		Kind:      "openai",
		BaseURL:   "https://api.mistral.ai/v1",
		APIKeyEnv: "MISTRAL_API_KEY",
		ResetTZ:   "UTC",
	},
	// Z.AI (Zhipu) GLM. The Flash models are free; they are limited by concurrency
	// (1 request at a time per model), not by tokens or requests per minute.
	"zai": {
		Kind:      "openai",
		BaseURL:   "https://api.z.ai/api/paas/v4",
		APIKeyEnv: "ZAI_API_KEY",
		ResetTZ:   "UTC",
	},
	"sambanova": {
		Kind:      "openai",
		BaseURL:   "https://api.sambanova.ai/v1",
		APIKeyEnv: "SAMBANOVA_API_KEY",
		ResetTZ:   "UTC",
	},
	// HuggingFace — Router endpoint that dispatches to many models.
	// Free tier has a small monthly quota; OpenAI-compatible.
	"huggingface": {
		Kind:      "openai",
		BaseURL:   "https://router.huggingface.co/v1",
		APIKeyEnv: "HF_TOKEN",
		ResetTZ:   "UTC",
	},
	// Ollama — local. No key needed by default; the api_key_env below is
	// OPTIONAL: if $OLLAMA_API_KEY is set, airouter will send it as a Bearer
	// token (useful when Ollama is behind a proxy / Ollama Cloud).
	"ollama": {
		Kind:      "ollama",
		BaseURL:   "http://localhost:11434",
		APIKeyEnv: "OLLAMA_API_KEY", // optional — leave env var unset for local no-auth Ollama
		ResetTZ:   "UTC",
	},
	// ollama-remote — same as ollama but points at a remote URL. Use this when
	// you self-host Ollama on a different machine and (optionally) put a token
	// in front of it.
	"ollama-remote": {
		Kind:      "ollama",
		BaseURL:   "",               // must be set with -base-url
		APIKeyEnv: "OLLAMA_API_KEY", // optional
		ResetTZ:   "UTC",
	},
	"openai": { // or any other OpenAI-compatible URL
		Kind:      "openai",
		BaseURL:   "https://api.openai.com/v1",
		APIKeyEnv: "OPENAI_API_KEY",
		ResetTZ:   "UTC",
	},
}

func boolPtr(b bool) *bool { return &b }

// Names returns the sorted list of preset names.
func Names() []string {
	n := make([]string, 0, len(Definitions))
	for k := range Definitions {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Apply fills preset defaults into p for every field p left empty. It also
// fills p.Name from preset+model when no name was given.
func Apply(p *types.ProviderConfig) error {
	pre, ok := Definitions[p.Preset]
	if !ok {
		return fmt.Errorf("unknown preset %q (have: %v)", p.Preset, Names())
	}
	if p.Kind == "" {
		p.Kind = pre.Kind
	}
	if p.BaseURL == "" {
		p.BaseURL = pre.BaseURL
	}
	if p.APIKeyEnv == "" && p.APIKey == "" {
		p.APIKeyEnv = pre.APIKeyEnv
	}
	if p.ResetTZ == "" {
		p.ResetTZ = pre.ResetTZ
	}
	if p.JSONMode == nil {
		p.JSONMode = pre.JSONMode
	}
	if p.SystemRole == nil {
		p.SystemRole = pre.SystemRole
	}
	if p.Category == "" {
		p.Category = pre.Category
	}
	if p.Name == "" && p.Model != "" {
		p.Name = p.Preset + "-" + p.Model
	}
	return nil
}

// ----- categories -----------------------------------------------------------
//
// Categories let callers filter the catalog and let the router pick only the
// providers that match the request shape (chat vs TTS vs STT). The default
// bootstrap imports only "text" entries because most users want chat; pass
// `-category tts,stt` (or `all`) to import the audio ones too.

const (
	// CategoryText = chat-capable model, OpenAI-compatible /chat/completions.
	CategoryText = "text"
	// CategoryTTS = text-to-speech. Routed by airouter via r.Speak / `airouter speak` / POST /v1/speak.
	CategoryTTS = "tts"
	// CategorySTT = speech-to-text / transcribe. Routed via r.Transcribe / `airouter transcribe` / POST /v1/transcribe.
	CategorySTT = "stt"
)

// Categories is the full list of categories used in FreePool, in display order.
var Categories = []string{
	CategoryText, CategoryTTS, CategorySTT,
}

// ----- the free-tier catalog ------------------------------------------------

// FreeModel is one row of the curated free-pool catalog: a known-free model on
// a known provider, with the limits that were true on VerifiedOn, a Category,
// and free-form Notes (typically a link to the source page and any caveats).
type FreeModel struct {
	Preset     string // key into Definitions
	Model      string // exact model id from the console
	Category   string // one of the Category* constants
	RPM        int    // requests per minute (0 = unlimited or unknown)
	RPD        int    // requests per day    (0 = unlimited or unknown)
	TPM        int    // tokens per minute   (0 = unlimited or unknown)
	ASH        int    // audio seconds per hour (TTS/STT only, 0 = no cap published)
	ASD        int    // audio seconds per day  (TTS/STT only, 0 = no cap published)
	Priority   int    // suggested priority (lower = tried first within its category)
	Workers    int    // suggested concurrent requests
	Notes      string // free-form hints + source link
	VerifiedOn string // YYYY-MM-DD when the limits were last checked
}

// FreePool is a curated, ordered list of free-tier (provider, model, limits)
// tuples spanning every category Google AI Studio and Groq expose on the free
// tier. Models are grouped by category and ordered by priority within each
// group; the priority is just a suggestion for the router when callers do
// `provider add -preset ... -model ...` by hand.
//
// Limits move. Every entry has a VerifiedOn date — if it is older than ~30 days
// when you read this, please re-check the provider's console and update both
// the numbers and the date.
//
// Sources:
//   - Gemini: https://aistudio.google.com/docs/rate-limits and per-model pages
//     under https://aistudio.google.com/docs/models/<model-id>
//   - Groq:   https://console.groq.com/docs/rate-limits and per-model pages
//     under https://console.groq.com/docs/model/<model-id>
var FreePool = []FreeModel{
	// ----- text chat -------------------------------------------------------
	// Sources: aistudio.google.com/docs/models (Nov 2026), console.groq.com/docs/rate-limits (Nov 2026).

	// Groq — generous free tier, very fast inference. All chat models share
	// the same per-key RPM/RPD/TPM/TPD envelope (RPM 30, RPD 1K, TPM 8K, TPD 200K
	// on the free plan as of Nov 2026).
	{Preset: "groq", Model: "openai/gpt-oss-120b", Category: CategoryText, RPM: 30, RPD: 1000, TPM: 8000, Priority: 10, Workers: 3,
		Notes:      "OpenAI's 120B open-weight flagship. Best Groq free model for general reasoning.",
		VerifiedOn: "2026-11-17"},
	{Preset: "groq", Model: "openai/gpt-oss-20b", Category: CategoryText, RPM: 30, RPD: 1000, TPM: 8000, Priority: 15, Workers: 3,
		Notes:      "Smaller/faster GPT-OSS variant. Good second-line model.",
		VerifiedOn: "2026-11-17"},
	{Preset: "groq", Model: "qwen/qwen3.8-27b", Category: CategoryText, RPM: 30, RPD: 1000, TPM: 8000, Priority: 20, Workers: 3,
		Notes:      "Qwen 3.8 27B — strong multilingual + tool use.",
		VerifiedOn: "2026-11-17"},

	// Gemini — Google AI Studio free tier. Resets at PT midnight.
	// Gemini 3.5 Flash-Lite is the recommended primary (fast, capable, 1M TPM).
	{Preset: "gemini", Model: "gemini-3.5-flash-lite", Category: CategoryText, RPM: 15, RPD: 500, TPM: 250000, Priority: 25, Workers: 2,
		Notes:      "Best Gemini free chat model. Note: sampling params (temp/top_p/top_k) are deprecated on 3.x.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-3.1-flash-lite", Category: CategoryText, RPM: 15, RPD: 500, TPM: 250000, Priority: 30, Workers: 2,
		Notes:      "Previous-gen Flash-Lite. Still solid; 3.5 is preferred.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-3.8-flash", Category: CategoryText, RPM: 5, RPD: 20, TPM: 250000, Priority: 35, Workers: 1,
		Notes:      "Newest Flash. Very tight free-tier RPD (20/day) — use as burst, not baseline.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-3.6-flash", Category: CategoryText, RPM: 5, RPD: 20, TPM: 250000, Priority: 40, Workers: 1,
		Notes:      "Previous-gen Flash with computer-use support. Same tight free RPD.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-3-flash", Category: CategoryText, RPM: 5, RPD: 20, TPM: 250000, Priority: 45, Workers: 1,
		Notes:      "Gemini 3 Flash (preview).",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-2.5-flash-lite", Category: CategoryText, RPM: 10, RPD: 20, TPM: 250000, Priority: 50, Workers: 1,
		Notes:      "Auto-routes to gemini-3.5-flash-lite on Dec 1 2026 — add the latter instead.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-2.5-flash", Category: CategoryText, RPM: 5, RPD: 20, TPM: 250000, Priority: 55, Workers: 1,
		Notes:      "DEPRECATED — auto-routes to gemini-3.5-flash-lite on Dec 1 2026. Avoid.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini", Model: "gemini-4-hydrogen", Category: CategoryText, RPM: 5, RPD: 20, TPM: 250000, Priority: 60, Workers: 1,
		Notes:      "Gemini 4 Hydrogen (preview). Free tier is very tight.",
		VerifiedOn: "2026-11-17"},

	// Gemma via Gemini API — no JSON mode, no system role. airouter folds system
	// into the user turn and asks for JSON in words.
	{Preset: "gemma", Model: "gemma-4-31b-it", Category: CategoryText, RPM: 30, RPD: 14400, TPM: 16000, Priority: 65, Workers: 2,
		Notes:      "Gemma 4 31B via Gemini API. No system role, no response_format — airouter handles both.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemma", Model: "gemma-4-26b-a4b-it", Category: CategoryText, RPM: 30, RPD: 14400, TPM: 16000, Priority: 70, Workers: 2,
		Notes:      "Smaller Gemma 4. Same Gemini-API quirks as 31B.",
		VerifiedOn: "2026-11-17"},

	// OpenRouter — free models cap at 20 RPM / 50 RPD by default
	// (1,000 RPD if you've ever paid OpenRouter anything).
	// Source: openrouter.ai/docs/api_reference/limits, Nov 2026.
	{Preset: "openrouter", Model: "meta-llama/llama-3.3-70b-instruct:free", Category: CategoryText, RPM: 20, RPD: 50, TPM: 0, Priority: 75, Workers: 2,
		Notes:      ":free suffix = OpenRouter's free variant. RPD rises to 1000 after any payment.",
		VerifiedOn: "2026-11-17"},
	{Preset: "openrouter", Model: "google/gemma-3-27b-it:free", Category: CategoryText, RPM: 20, RPD: 50, TPM: 0, Priority: 80, Workers: 2,
		Notes:      "Free Gemma via OpenRouter — different route from Gemini direct.",
		VerifiedOn: "2026-11-17"},

	// Z.AI GLM Flash — free, no daily token cap and no fixed RPM. The only limit is
	// ONE concurrent request per model (a second one gets HTTP 429, code 1302), so
	// Workers is 1 and the router queues instead of failing. Source: Z.AI docs as
	// relayed by the project owner; not re-verified against the live console.
	{Preset: "zai", Model: "glm-4.7-flash", Category: CategoryText, RPM: 0, RPD: 0, TPM: 0, Priority: 55, Workers: 1,
		Notes:      "Free. 1 concurrent request. 200K context, 128K output. Reasoning model.",
		VerifiedOn: "2026-11-20"},
	{Preset: "zai", Model: "glm-4.5-flash", Category: CategoryText, RPM: 0, RPD: 0, TPM: 0, Priority: 78, Workers: 1,
		Notes:      "Free. 1 concurrent request. 128K context, 96K output. Z.AI announced its retirement.",
		VerifiedOn: "2026-11-20"},
	{Preset: "zai", Model: "glm-4.6v-flash", Category: CategoryText, RPM: 0, RPD: 0, TPM: 0, Priority: 82, Workers: 1,
		Notes:      "Free. 1 concurrent request. 128K context, 32K output. Multimodal model; airouter sends text only.",
		VerifiedOn: "2026-11-20"},

	// SambaNova — dev tier has 20 RPM per the community thread.
	{Preset: "sambanova", Model: "Meta-Llama-3.1-8B-Instruct", Category: CategoryText, RPM: 20, RPD: 0, TPM: 0, Priority: 90, Workers: 2,
		Notes:      "0 = unknown. airouter will discover real limits from 429s.",
		VerifiedOn: "2026-11-17"},

	// Ollama local — no quota, but limited by your hardware. Last-resort fallback.
	{Preset: "ollama", Model: "gemma3:4b", Category: CategoryText, RPM: 0, RPD: 0, TPM: 0, Priority: 95, Workers: 1,
		Notes:      "Local fallback. Requires `ollama serve` on localhost:11434.",
		VerifiedOn: "2026-11-17"},

	// ----- text-to-speech (routed by r.Speak / `airouter speak` / POST /v1/speak) ----
	// Sources:
	//   Groq:   https://console.groq.com/docs/text-to-speech (POST /openai/v1/audio/speech)
	//   Gemini: https://aistudio.google.com/docs/speech-generation (POST /v1beta/models/{model}:generateContent)
	// Groq returns WAV bytes directly. Gemini returns base64-encoded WAV inline.
	// Default voices: Groq Orpheus English = "tara"; Gemini = "Kore".
	{Preset: "gemini-tts", Model: "gemini-3.8-flash-tts", Category: CategoryTTS, RPM: 3, RPD: 10, TPM: 10000, Priority: 100, Workers: 1,
		Notes:      "Flagship Gemini TTS. 30 prebuilt voices (Kore, Puck, Charon, Aoede, Fenrir, Leda, Orus, Zephyr, ...). Returns WAV.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini-tts", Model: "gemini-3.8-flash-lite-tts", Category: CategoryTTS, RPM: 3, RPD: 10, TPM: 10000, Priority: 105, Workers: 1,
		Notes:      "Fast/cost-efficient Gemini TTS for high-volume production. Same voice set.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini-tts", Model: "gemini-3.1-flash-tts", Category: CategoryTTS, RPM: 3, RPD: 10, TPM: 10000, Priority: 110, Workers: 1,
		Notes:      "Legacy Gemini TTS preview. Update to 3.8 Flash TTS if possible.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini-tts", Model: "gemini-2.5-flash-preview-tts", Category: CategoryTTS, RPM: 3, RPD: 10, TPM: 10000, Priority: 115, Workers: 1,
		Notes:      "Gemini 2.5 Flash TTS preview. Same prebuilt voices as 3.x.",
		VerifiedOn: "2026-11-17"},
	{Preset: "groq", Model: "canopylabs/orpheus-v1-english", Category: CategoryTTS, RPM: 10, RPD: 100, TPM: 1200, Priority: 120, Workers: 1,
		Notes:      "Orpheus English TTS via Groq. Voices: troy, hannah, austin, alex, bryan, eli, emma, jess, julia, tara. Returns WAV.",
		VerifiedOn: "2026-11-17"},
	{Preset: "groq", Model: "canopylabs/orpheus-arabic-saudi", Category: CategoryTTS, RPM: 10, RPD: 100, TPM: 1200, Priority: 125, Workers: 1,
		Notes:      "Orpheus Arabic (Saudi) TTS via Groq. Same envelope as English variant.",
		VerifiedOn: "2026-11-17"},

	// ----- speech-to-text (routed by r.Transcribe / `airouter transcribe` / POST /v1/transcribe) ----
	// Sources:
	//   Groq:   https://console.groq.com/docs/speech-to-text (POST /openai/v1/audio/transcriptions, multipart)
	//   Gemini: https://aistudio.google.com/docs/transcribe (Files API upload + interactions endpoint)
	// Both providers are fully routed. Audio seconds are tracked for ASH/ASD limits.
	{Preset: "groq", Model: "whisper-large-v3-turbo", Category: CategorySTT, RPM: 20, RPD: 0, TPM: 2000, ASH: 7200, ASD: 28800, Priority: 200, Workers: 2,
		Notes:      "Whisper Large v3 Turbo via Groq. 7.2K audio-sec/hour (2h), 28.8K audio-sec/day (8h). Audio seconds tracked by airouter.",
		VerifiedOn: "2026-11-17"},
	{Preset: "groq", Model: "whisper-large-v3", Category: CategorySTT, RPM: 20, RPD: 0, TPM: 2000, ASH: 7200, ASD: 28800, Priority: 205, Workers: 2,
		Notes:      "Whisper Large v3 via Groq. Same audio-sec envelope as turbo variant.",
		VerifiedOn: "2026-11-17"},
	{Preset: "gemini-tts", Model: "gemini-3.5-transcribe", Category: CategorySTT, RPM: 3, RPD: 25, TPM: 10000, Priority: 210, Workers: 1,
		Notes:      "Gemini 3.5 Transcribe. Two-step: upload file via Files API, then POST to /v1beta/interactions. Word timestamps + speaker diarization available.",
		VerifiedOn: "2026-11-17"},
}

// FreePoolFor returns the entries of FreePool whose Preset matches. Empty
// preset returns the whole list.
func FreePoolFor(preset string) []FreeModel {
	if preset == "" {
		return append([]FreeModel(nil), FreePool...)
	}
	var out []FreeModel
	for _, m := range FreePool {
		if m.Preset == preset {
			out = append(out, m)
		}
	}
	return out
}

// FreePoolByCategory returns the entries whose Category is in `categories`.
// Pass nil/empty to get the whole list. Category comparison is case-insensitive.
func FreePoolByCategory(categories []string) []FreeModel {
	if len(categories) == 0 {
		return append([]FreeModel(nil), FreePool...)
	}
	want := make(map[string]bool, len(categories))
	for _, c := range categories {
		want[strings.ToLower(strings.TrimSpace(c))] = true
	}
	var out []FreeModel
	for _, m := range FreePool {
		if want[strings.ToLower(m.Category)] {
			out = append(out, m)
		}
	}
	return out
}

// DefaultBootstrapCategories is the list of categories that BootstrapProviders
// imports by default — only chat-capable text models, because airouter's
// /chat/completions request shape only works for those. Callers can pass a
// different list to BootstrapProviders if they want to seed e.g. embeddings
// for a future feature.
var DefaultBootstrapCategories = []string{CategoryText}

// ToProviderConfig converts one free-pool entry into a ProviderConfig ready to
// be saved. The FreeModel's Category always wins over the preset default (so
// an STT entry on the gemini-tts preset still ends up with Category=stt).
func (m FreeModel) ToProviderConfig() types.ProviderConfig {
	p := types.ProviderConfig{Preset: m.Preset, Model: m.Model, Category: m.Category,
		RPM: m.RPM, RPD: m.RPD, TPM: m.TPM, ASH: m.ASH, ASD: m.ASD,
		Priority: m.Priority, Workers: m.Workers}
	_ = Apply(&p)
	if m.Category != "" {
		p.Category = m.Category
	}
	if p.Priority == 0 {
		p.Priority = 100
	}
	if p.Workers == 0 {
		p.Workers = 2
	}
	if p.Category == "" {
		p.Category = "text"
	}
	return p
}

// CatalogVerifiedOn returns the newest VerifiedOn date in the catalog, or the
// zero time if the catalog is empty. The CLI surfaces this in `bootstrap` so
// users know how stale the limits might be.
func CatalogVerifiedOn() time.Time {
	var latest time.Time
	for _, m := range FreePool {
		if t, err := time.Parse("2006-01-02", m.VerifiedOn); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// CatalogSummary returns a per-category count of catalog entries. Useful for
// `airouter free-pool -summary` and the README.
func CatalogSummary() map[string]int {
	out := make(map[string]int, len(Categories))
	for _, m := range FreePool {
		out[m.Category]++
	}
	return out
}
