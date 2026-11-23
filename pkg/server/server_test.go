package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// okBody returns an OpenAI-compatible success body.
func okBody(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"content": content}}},
		"usage":   map[string]int{"total_tokens": 100},
	})
	return string(b)
}

// fakeProvider returns an httptest.Server that always replies with okBody(content).
func fakeProvider(t *testing.T, content string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(okBody(content)))
	}))
	t.Cleanup(s.Close)
	return s
}

func newTestRouter(t *testing.T) *router.Router {
	r, err := router.New(router.Options{
		Store:   store.NewMemoryStore(),
		MaxWait: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func do(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, map[string]any, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(buf.Bytes(), &m)
	return resp.StatusCode, m, buf.String()
}

func TestHTTPAPI(t *testing.T) {
	up := fakeProvider(t, "hello")
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)

	// No auth → 401.
	if c, _, _ := do(t, srv, "POST", "/v1/generate", "", `{"prompt":"x"}`); c != 401 {
		t.Fatalf("no key: %d", c)
	}
	// Bad admin → 401.
	if c, _, _ := do(t, srv, "GET", "/v1/providers", "wrong", ""); c != 401 {
		t.Fatalf("bad admin: %d", c)
	}
	// Create a tenant.
	c, m, _ := do(t, srv, "POST", "/v1/tenants", "admin-secret", `{"id":"acme","daily_request_limit":50}`)
	key, _ := m["api_key"].(string)
	if c != 201 || !strings.HasPrefix(key, "air_") {
		t.Fatalf("tenant: %d %v", c, m)
	}
	// Add a provider via admin.
	if c, _, body := do(t, srv, "POST", "/v1/providers", "admin-secret",
		`{"preset":"openai","name":"up","base_url":"`+up.URL+`","api_key":"k","model":"m","rpd":10,"used_today_hint":3}`); c != 201 || strings.Contains(body, `"api_key": "k"`) {
		t.Fatalf("provider: %d %s", c, body)
	}
	// Generate as the tenant.
	c, m, _ = do(t, srv, "POST", "/v1/generate", key, `{"prompt":"hi"}`)
	if c != 200 || m["text"] != "hello" || m["provider"] != "up" {
		t.Fatalf("generate: %d %v", c, m)
	}
	// Plain text format query string.
	if c, _, body := do(t, srv, "POST", "/v1/generate?format=text", key, `{"prompt":"hi"}`); c != 200 || body != "hello" {
		t.Fatalf("text: %d %q", c, body)
	}
	// Usage.
	c, m, _ = do(t, srv, "GET", "/v1/usage", key, "")
	u := m["usage"].(map[string]any)
	row := u["providers"].([]any)[0].(map[string]any)
	if c != 200 || u["requests_today"].(float64) != 2 || row["all_tenants_requests"].(float64) != 5 || row["limit_per_day"].(float64) != 10 {
		t.Fatalf("usage: %d %v", c, m)
	}
	// Tenant key must not reach admin endpoints.
	if c, _, _ := do(t, srv, "GET", "/v1/providers", key, ""); c != 401 {
		t.Fatalf("tenant key must not reach admin API: %d", c)
	}
}

func TestPresetsEndpoint(t *testing.T) {
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)
	c, m, _ := do(t, srv, "GET", "/v1/presets", "", "")
	if c != 200 {
		t.Fatalf("presets: %d", c)
	}
	if _, ok := m["groq"].(map[string]any); !ok {
		t.Fatalf("presets missing groq: %v", m)
	}
}

func TestFreePoolEndpoint(t *testing.T) {
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)
	c, m, _ := do(t, srv, "GET", "/v1/presets/free-pool", "", "")
	if c != 200 {
		t.Fatalf("free-pool: %d", c)
	}
	models := m["models"].([]any)
	if len(models) == 0 {
		t.Fatalf("free-pool empty")
	}
}

func TestHealthz(t *testing.T) {
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, ""))
	t.Cleanup(srv.Close)
	c, m, _ := do(t, srv, "GET", "/healthz", "", "")
	if c != 200 || m["status"] != "ok" {
		t.Fatalf("healthz: %d %v", c, m)
	}
}

// TestBootstrapEndpoint checks that POST /v1/bootstrap seeds the catalog.
func TestBootstrapEndpoint(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "fake-key-for-test")
	t.Setenv("GEMINI_API_KEY", "fake-key-for-test")
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)

	// Default bootstrap = text only.
	c, m, _ := do(t, srv, "POST", "/v1/bootstrap", "admin-secret", "")
	if c != 200 {
		t.Fatalf("bootstrap: %d %v", c, m)
	}
	added := m["added"].([]any)
	if len(added) == 0 {
		t.Fatalf("bootstrap added nothing: %v", m)
	}
	if m["categories_used"] != "text (default)" {
		t.Fatalf("categories_used: %v", m["categories_used"])
	}
	summary := m["summary"].(map[string]any)
	if summary["text"] == nil {
		t.Fatalf("summary missing text count: %v", summary)
	}
}

// TestSpeakEndpoint verifies POST /v1/speak returns audio bytes with the right
// Content-Type header.
func TestSpeakEndpoint(t *testing.T) {
	up := fakeTTSProvider(t, "AUDIO-BYTES-FROM-HTTP")
	r := newTestRouter(t)
	p := types.ProviderConfig{
		Name: "tts", Kind: "openai", BaseURL: up.URL, APIKey: "k",
		Model: "orpheus", Category: types.CategoryTTS, Priority: 10, Workers: 1,
	}
	if err := r.SaveProvider(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)

	// Create a tenant.
	c, m, _ := do(t, srv, "POST", "/v1/tenants", "admin-secret", `{"id":"acme"}`)
	key, _ := m["api_key"].(string)
	if c != 201 || !strings.HasPrefix(key, "air_") {
		t.Fatalf("tenant: %d %v", c, m)
	}

	// Speak as the tenant — raw audio bytes back by default.
	req, _ := http.NewRequest("POST", srv.URL+"/v1/speak", bytes.NewBufferString(`{"text":"hi","voice":"tara"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("speak: %d %s", resp.StatusCode, string(body))
	}
	if resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("Content-Type: %q", resp.Header.Get("Content-Type"))
	}
	if string(body) != "AUDIO-BYTES-FROM-HTTP" {
		t.Fatalf("audio: %q", string(body))
	}
	if resp.Header.Get("X-AIRouter-Provider") != "tts" {
		t.Fatalf("X-AIRouter-Provider: %q", resp.Header.Get("X-AIRouter-Provider"))
	}

	// Speak with ?format=json → JSON envelope with base64 audio.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/speak?format=json", bytes.NewBufferString(`{"text":"hi"}`))
	req2.Header.Set("Authorization", "Bearer "+key)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var env struct {
		Provider    string `json:"provider"`
		AudioB64    string `json:"audio_base64"`
		ContentType string `json:"content_type"`
		Format      string `json:"format"`
		Voice       string `json:"voice"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&env); err != nil {
		t.Fatalf("decode JSON envelope: %v", err)
	}
	if env.Provider != "tts" {
		t.Fatalf("provider: %s", env.Provider)
	}
	if env.AudioB64 == "" {
		t.Fatalf("audio_base64 empty")
	}
	if env.Format != "wav" {
		t.Fatalf("format: %s", env.Format)
	}
}

// TestTranscribeEndpoint verifies POST /v1/transcribe accepts a multipart
// upload and returns JSON with the transcribed text.
func TestTranscribeEndpoint(t *testing.T) {
	up := fakeSTTProvider(t, "Hello from the HTTP transcribe test.")
	r := newTestRouter(t)
	p := types.ProviderConfig{
		Name: "stt", Kind: "openai", BaseURL: up.URL, APIKey: "k",
		Model: "whisper", Category: types.CategorySTT, Priority: 10, Workers: 1,
	}
	if err := r.SaveProvider(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(r, "admin-secret"))
	t.Cleanup(srv.Close)

	// Create a tenant.
	c, m, _ := do(t, srv, "POST", "/v1/tenants", "admin-secret", `{"id":"acme"}`)
	key, _ := m["api_key"].(string)
	if c != 201 {
		t.Fatalf("tenant: %d", c)
	}

	// Build multipart form.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "audio.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("FAKE-AUDIO")); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("model", "whisper"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("language", "en"); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/v1/transcribe", &buf)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Text     string `json:"text"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("transcribe: %d", resp.StatusCode)
	}
	if out.Text != "Hello from the HTTP transcribe test." {
		t.Fatalf("text: %q", out.Text)
	}
	if out.Provider != "stt" {
		t.Fatalf("provider: %s", out.Provider)
	}
}

// fakeTTSProvider mimics a Groq /audio/speech endpoint for HTTP tests.
// Returns the given audio bytes for any request.
func fakeTTSProvider(t *testing.T, audio string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte(audio))
	}))
	t.Cleanup(s.Close)
	return s
}

// fakeSTTProvider mimics a Groq /audio/transcriptions endpoint for HTTP tests.
func fakeSTTProvider(t *testing.T, wantText string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(8 << 20)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": wantText})
	}))
	t.Cleanup(s.Close)
	return s
}

func TestOpenAICompatibleAPI(t *testing.T) {
	up := fakeProvider(t, "hello from upstream")
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	_, m, _ := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"app"}`)
	key := m["api_key"].(string)
	do(t, srv, "POST", "/v1/providers", "admin", `{"preset":"openai","name":"up","base_url":"`+up.URL+`","api_key":"k","model":"gpt-x"}`)

	c, m, body := do(t, srv, "POST", "/v1/chat/completions", key,
		`{"model":"auto","messages":[{"role":"developer","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	ch := m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if c != 200 || ch["content"] != "hello from upstream" || m["object"] != "chat.completion" {
		t.Fatalf("chat: %d %s", c, body)
	}
	// Pin by exact model id; unknown model -> 404 in OpenAI error shape.
	if c, _, _ := do(t, srv, "POST", "/v1/chat/completions", key, `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`); c != 200 {
		t.Fatalf("pin by model id: %d", c)
	}
	c, m, _ = do(t, srv, "POST", "/v1/chat/completions", key, `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	if c != 404 || m["error"].(map[string]any)["type"] != "model_not_found" {
		t.Fatalf("unknown model: %d %v", c, m)
	}
	// Unsupported features say so instead of silently misbehaving.
	if c, _, _ := do(t, srv, "POST", "/v1/chat/completions", key, `{"tools":[{}],"messages":[{"role":"user","content":"x"}]}`); c != 400 {
		t.Fatalf("tools: %d", c)
	}
	// No messages is the caller's error: 400, not 500/502.
	if c, _, _ := do(t, srv, "POST", "/v1/chat/completions", key, `{"messages":[]}`); c != 400 {
		t.Fatalf("empty: %d", c)
	}
	if c, _, _ := do(t, srv, "POST", "/v1/chat/completions", "bad", `{}`); c != 401 {
		t.Fatalf("auth: %d", c)
	}
	// Streaming wire format.
	_, _, sse := do(t, srv, "POST", "/v1/chat/completions", key, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(sse, "chat.completion.chunk") || !strings.HasSuffix(strings.TrimSpace(sse), "data: [DONE]") {
		t.Fatalf("sse: %q", sse)
	}
	// /v1/models lists auto + the model.
	if _, _, b := do(t, srv, "GET", "/v1/models", key, ""); !strings.Contains(b, `"auto"`) || !strings.Contains(b, "gpt-x") {
		t.Fatalf("models: %s", b)
	}
	// Rotate: old key dies, new works.
	_, m, _ = do(t, srv, "POST", "/v1/tenants/app/rotate-key", "admin", "")
	if c, _, _ := do(t, srv, "GET", "/v1/models", key, ""); c != 401 {
		t.Fatalf("old key still works: %d", c)
	}
	if c, _, _ := do(t, srv, "GET", "/v1/models", m["api_key"].(string), ""); c != 200 {
		t.Fatalf("new key: %d", c)
	}
	// The UI is served.
	if c, _, b := do(t, srv, "GET", "/", "", ""); c != 200 || !strings.Contains(b, "<title>airouter</title>") {
		t.Fatalf("ui: %d", c)
	}
}

func TestErrorMapping(t *testing.T) {
	for err, want := range map[error]int{
		types.ErrTenantDisabled: 403, types.ErrUnknownTenant: 401, types.ErrUnauthorized: 401,
		types.ErrNoProviders: 503, types.ErrTimeout: 504, types.ErrBadRequest: 400,
		&types.ExhaustedError{}: 503, &types.TenantLimitError{}: 429, &types.AllFailedError{}: 502,
		&types.BadRequestError{Message: "x"}: 400,
	} {
		if got, _, _ := mapErr(err); got != want {
			t.Errorf("%v: got %d want %d", err, got, want)
		}
	}
}
