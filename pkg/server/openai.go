package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// ---- OpenAI-compatible API -------------------------------------------------
//
// Base URL for SDKs:  http://HOST:PORT/v1     API key: an airouter tenant key.
//
//   POST /v1/chat/completions      (stream=true is supported; see below)
//   GET  /v1/models
//   POST /v1/audio/speech
//   POST /v1/audio/transcriptions
//
// model: "auto" (or empty) lets airouter choose and fail over. A provider name
// or an exact model id pins that provider. Not supported (yet): tool/function
// calling and image inputs — requests that use them get a clear 400.
//
// stream=true is "buffered streaming": airouter must see the whole answer to
// fail over safely, so it sends the finished text as SSE chunks. Clients that
// require the streaming wire format work; time-to-first-token does not improve.

type oaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type oaiChatRequest struct {
	Model               string       `json:"model"`
	Messages            []oaiMessage `json:"messages"`
	Temperature         *float64     `json:"temperature"`
	MaxTokens           int          `json:"max_tokens"`
	MaxCompletionTokens int          `json:"max_completion_tokens"`
	Stream              bool         `json:"stream"`
	Tools               []any        `json:"tools"`
	Functions           []any        `json:"functions"`
	ResponseFormat      *struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

// flattenContent accepts a string or an array of {type:"text",text:"..."} parts.
func flattenContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content must be a string or a list of text parts")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "" && p.Type != "text" {
			return "", fmt.Errorf("content part type %q is not supported (text only)", p.Type)
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

func writeOAIErr(w http.ResponseWriter, code int, typ, msg string, extra map[string]any) {
	if ra, ok := extra["retry_after_seconds"].(int64); ok && ra > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(ra, 10))
	}
	body := map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code, "param": nil}}
	if extra != nil {
		body["airouter"] = extra
	}
	writeJSON(w, code, body)
}

func writeOAIRouterErr(w http.ResponseWriter, err error) {
	code, typ, extra := mapErr(err)
	writeOAIErr(w, code, typ, err.Error(), extra)
}

func randID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-air-" + hex.EncodeToString(b)
}

// resolveModel maps an OpenAI "model" string to a provider name to pin.
func (s *apiServer) resolveModel(r *http.Request, tenantID, model, category string) (provider string, found bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "auto", "airouter", "default":
		return "", true
	}
	for _, st := range s.r.Status(r.Context(), tenantID) {
		cat := st.Category
		if cat == "" {
			cat = types.CategoryText
		}
		if cat == category && (st.Name == model || st.Model == model) {
			return st.Name, true
		}
	}
	return "", false
}

func (s *apiServer) chatCompletions(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenantOAI(w, r)
	if !ok {
		return
	}
	var in oaiChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		writeOAIErr(w, 400, "bad_request", "invalid JSON body: "+err.Error(), nil)
		return
	}
	if len(in.Tools) > 0 || len(in.Functions) > 0 {
		writeOAIErr(w, 400, "bad_request", "tool / function calling is not supported by airouter yet; send plain chat messages", nil)
		return
	}
	req := types.Request{TenantID: t.ID, Temperature: in.Temperature, MaxTokens: in.MaxTokens}
	if in.MaxCompletionTokens > 0 {
		req.MaxTokens = in.MaxCompletionTokens
	}
	if in.ResponseFormat != nil && (in.ResponseFormat.Type == "json_object" || in.ResponseFormat.Type == "json_schema") {
		req.JSON = true
	}
	for _, m := range in.Messages {
		text, err := flattenContent(m.Content)
		if err != nil {
			writeOAIErr(w, 400, "bad_request", err.Error(), nil)
			return
		}
		role := m.Role
		switch role {
		case "developer":
			role = "system"
		case "system", "user", "assistant":
		default:
			writeOAIErr(w, 400, "bad_request", fmt.Sprintf("message role %q is not supported", m.Role), nil)
			return
		}
		req.Messages = append(req.Messages, types.Message{Role: role, Content: text})
	}
	prov, found := s.resolveModel(r, t.ID, in.Model, types.CategoryText)
	if !found {
		writeOAIErr(w, 404, "model_not_found", fmt.Sprintf("model %q not found; use \"auto\" or see GET /v1/models", in.Model), nil)
		return
	}
	req.Provider = prov

	resp, err := s.r.Generate(r.Context(), req)
	if err != nil {
		writeOAIRouterErr(w, err)
		return
	}
	content := resp.Text
	if req.JSON && len(resp.JSON) > 0 {
		content = string(resp.JSON)
	}
	w.Header().Set("X-AIRouter-Provider", resp.Provider)
	w.Header().Set("X-AIRouter-Model", resp.Model)
	id, created := randID(), time.Now().Unix()

	if !in.Stream {
		writeJSON(w, 200, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": resp.Model,
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content},
			}},
			"usage":    map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": resp.Tokens},
			"airouter": map[string]any{"provider": resp.Provider, "latency_ms": resp.LatencyMS, "attempts": resp.Attempts},
		})
		return
	}

	// Buffered streaming (see header comment).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	send := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": resp.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	send(map[string]any{"role": "assistant", "content": ""}, nil)
	for _, chunk := range splitRunes(content, 48) {
		send(map[string]any{"content": chunk}, nil)
	}
	send(map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// splitRunes cuts s into pieces of at most n runes (never splitting a rune).
func splitRunes(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		cut, count := 0, 0
		for cut < len(s) && count < n {
			_, size := utf8.DecodeRuneInString(s[cut:])
			cut += size
			count++
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

func (s *apiServer) models(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenantOAI(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()
	data := []any{map[string]any{"id": "auto", "object": "model", "created": now, "owned_by": "airouter"}}
	seen := map[string]bool{"auto": true}
	for _, st := range s.r.Status(r.Context(), t.ID) {
		if (st.Category != "" && st.Category != types.CategoryText) || seen[st.Model] {
			continue
		}
		seen[st.Model] = true
		owner := st.Preset
		if owner == "" {
			owner = st.Kind
		}
		data = append(data, map[string]any{"id": st.Model, "object": "model", "created": now, "owned_by": owner})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// audioSpeech adapts OpenAI's {model,input,voice,response_format} to Speak.
func (s *apiServer) audioSpeech(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenantOAI(w, r)
	if !ok {
		return
	}
	var in struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		Voice          string `json:"voice"`
		ResponseFormat string `json:"response_format"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		writeOAIErr(w, 400, "bad_request", "invalid JSON body: "+err.Error(), nil)
		return
	}
	prov, found := s.resolveModel(r, t.ID, in.Model, types.CategoryTTS)
	if !found {
		writeOAIErr(w, 404, "model_not_found", fmt.Sprintf("tts model %q not found", in.Model), nil)
		return
	}
	resp, err := s.r.Speak(r.Context(), types.SpeakRequest{TenantID: t.ID, Text: in.Input, Voice: in.Voice, Format: in.ResponseFormat, Provider: prov})
	if err != nil {
		writeOAIRouterErr(w, err)
		return
	}
	w.Header().Set("Content-Type", resp.ContentType)
	w.Header().Set("X-AIRouter-Provider", resp.Provider)
	_, _ = w.Write(resp.Audio)
}

// resolveTenantOAI is resolveTenant with OpenAI-shaped 401/403 errors.
func (s *apiServer) resolveTenantOAI(w http.ResponseWriter, r *http.Request) (*types.Tenant, bool) {
	if s.isAdmin(r) {
		return s.resolveTenant(w, r)
	}
	key := bearer(r)
	if key == "" {
		writeOAIErr(w, 401, "unauthorized", "missing API key (Authorization: Bearer <airouter key>)", nil)
		return nil, false
	}
	t, err := s.r.Authenticate(r.Context(), key)
	if err != nil {
		writeOAIRouterErr(w, err)
		return nil, false
	}
	return t, true
}

// ---- web UI -----------------------------------------------------------------

//go:embed web/index.html
var webFS embed.FS

func uiHandler() http.Handler {
	page, _ := webFS.ReadFile("web/index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		_, _ = w.Write(page)
	})
}

// WithCORS lets browser apps on other origins call the API. origins is a
// comma-separated allow-list ("https://app.example.com") or "*". Empty = off.
func WithCORS(next http.Handler, origins string) http.Handler {
	origins = strings.TrimSpace(origins)
	if origins == "" {
		return next
	}
	allow := map[string]bool{}
	for _, o := range strings.Split(origins, ",") {
		allow[strings.TrimSpace(o)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o != "" && (allow["*"] || allow[o]) {
			if allow["*"] {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", o)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-Tenant-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-AIRouter-Provider, X-AIRouter-Model, Retry-After")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
