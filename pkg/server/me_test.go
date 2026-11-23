package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/store"
)

func tenantKey(t *testing.T, srv *httptest.Server, id string) string {
	_, m, b := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"`+id+`"}`)
	k, _ := m["api_key"].(string)
	if k == "" {
		t.Fatalf("create tenant: %s", b)
	}
	return k
}

func ownIDs(t *testing.T, srv *httptest.Server, key string) []float64 {
	_, m, _ := do(t, srv, "GET", "/v1/me/providers", key, "")
	var ids []float64
	for _, p := range m["own"].([]any) {
		ids = append(ids, p.(map[string]any)["id"].(float64))
	}
	return ids
}

// Each tenant manages its own models; nobody can see or change anyone else's.
func TestTenantSelfServiceIsolation(t *testing.T) {
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	alice, bob := tenantKey(t, srv, "alice"), tenantKey(t, srv, "bob")

	// Alice picks Groq models from the list and pastes her own key.
	c, m, b := do(t, srv, "POST", "/v1/me/catalog/add", alice, `{"preset":"groq","api_key":"gsk_alice_key_1234","models":["openai/gpt-oss-20b"]}`)
	if c != 200 || len(m["added"].([]any)) != 1 {
		t.Fatalf("catalog add: %d %s", c, b)
	}
	// Without a key it refuses with a helpful message.
	if c, _, b := do(t, srv, "POST", "/v1/me/catalog/add", alice, `{"preset":"gemini"}`); c != 400 || !strings.Contains(b, "API key") {
		t.Fatalf("missing key: %d %s", c, b)
	}
	ids := ownIDs(t, srv, alice)
	if len(ids) != 1 || len(ownIDs(t, srv, bob)) != 0 {
		t.Fatalf("alice=%v bob=%v", ids, ownIDs(t, srv, bob))
	}
	id := fmt.Sprint(int(ids[0]))

	// Bob cannot touch it in any way: always 404, as if it did not exist.
	for _, call := range [][3]string{
		{"POST", "/v1/me/providers/" + id + "/key", `{"api_key":"stolen"}`},
		{"POST", "/v1/me/providers/" + id + "/disable", ``},
		{"POST", "/v1/me/providers/" + id + "/limits", `{"rpm":1}`},
		{"POST", "/v1/me/providers/" + id + "/reset", ``},
		{"DELETE", "/v1/me/providers/" + id, ``},
	} {
		if c, _, _ := do(t, srv, call[0], call[1], bob, call[2]); c != 404 {
			t.Errorf("bob %s %s -> %d, want 404", call[0], call[1], c)
		}
	}
	// The catalog marks what Alice already has.
	_, cat, _ := do(t, srv, "GET", "/v1/me/catalog", alice, "")
	added := 0
	for _, mm := range cat["models"].([]any) {
		if mm.(map[string]any)["added"] == true {
			added++
		}
	}
	if added != 1 {
		t.Fatalf("catalog added flag: %d", added)
	}
	// Alice edits her limits and key, disables, re-enables.
	if c, _, b := do(t, srv, "POST", "/v1/me/providers/"+id+"/limits", alice, `{"rpm":5,"rpd":50,"workers":99}`); c != 200 {
		t.Fatalf("limits: %d %s", c, b)
	}
	_, lm, _ := do(t, srv, "GET", "/v1/me/providers", alice, "")
	row := lm["own"].([]any)[0].(map[string]any)
	if row["rpm"].(float64) != 5 || row["rpd"].(float64) != 50 || row["workers"].(float64) != 8 || row["key_source"] != "db" {
		t.Fatalf("row: %v", row)
	}
	if strings.Contains(fmt.Sprint(lm), "gsk_alice") {
		t.Fatal("API key leaked in the listing")
	}
	if c, _, _ := do(t, srv, "POST", "/v1/me/providers/"+id+"/key", alice, `{"api_key":"gsk_new_key_5678"}`); c != 200 {
		t.Fatal("set key")
	}
	if c, _, _ := do(t, srv, "POST", "/v1/me/providers/"+id+"/disable", alice, ``); c != 200 {
		t.Fatal("disable")
	}
	// Export is importable and key-free; delete then re-import round-trips.
	_, _, exp := do(t, srv, "GET", "/v1/me/export", alice, "")
	if strings.Contains(exp, "gsk_") || !strings.Contains(exp, "gpt-oss-20b") {
		t.Fatalf("export: %s", exp)
	}
	if c, _, _ := do(t, srv, "DELETE", "/v1/me/providers/"+id, alice, ""); c != 200 || len(ownIDs(t, srv, alice)) != 0 {
		t.Fatal("delete")
	}
	// Admin can act for a tenant with X-Tenant-ID.
	req, _ := http.NewRequest("POST", srv.URL+"/v1/me/catalog/add", strings.NewReader(`{"preset":"groq","api_key":"gsk_admin_for_bob","models":["openai/gpt-oss-20b"]}`))
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("X-Tenant-ID", "bob")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("admin on behalf of bob: %v %v", err, resp)
	}
	if len(ownIDs(t, srv, bob)) != 1 {
		t.Fatal("bob should now have one model")
	}
}

// Tenants cannot use the server's env keys or aim the server at arbitrary URLs.
func TestTenantCannotAbuseServerKeysOrURLs(t *testing.T) {
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	k := tenantKey(t, srv, "mallory")
	t.Setenv("GROQ_API_KEY", "server-owned-key")

	if c, _, b := do(t, srv, "POST", "/v1/me/providers", k, `{"preset":"groq","model":"m","api_key_env":"GROQ_API_KEY"}`); c != 400 || !strings.Contains(b, "api_key_env") {
		t.Fatalf("env key: %d %s", c, b)
	}
	// No key at all must not silently fall back to the server's env var either.
	if c, _, _ := do(t, srv, "POST", "/v1/me/providers", k, `{"preset":"groq","model":"m"}`); c != 400 {
		t.Fatalf("no key: %d", c)
	}
	// Custom URL, no preset: refused.
	if c, _, b := do(t, srv, "POST", "/v1/me/providers", k, `{"name":"x","kind":"openai","base_url":"http://169.254.169.254/v1","model":"m","api_key":"k"}`); c != 400 {
		t.Fatalf("custom url: %d %s", c, b)
	}
	// A preset with a hostile base_url is overridden by the preset's real URL.
	if c, _, b := do(t, srv, "POST", "/v1/me/providers", k, `{"preset":"groq","base_url":"http://evil.local","model":"m","api_key":"k"}`); c != 201 {
		t.Fatalf("preset add: %d %s", c, b)
	}
	_, m, _ := do(t, srv, "GET", "/v1/me/providers", k, "")
	if u := m["own"].([]any)[0].(map[string]any)["base_url"]; u != "https://api.groq.com/openai/v1" {
		t.Fatalf("base url not forced from preset: %v", u)
	}
	// Mallory cannot manage the operator's shared providers via /v1/providers.
	if c, _, _ := do(t, srv, "GET", "/v1/providers", k, ""); c != 403 && c != 401 {
		t.Fatalf("tenant on admin endpoint: %d", c)
	}
}

// A tenant's own model serves that tenant's requests first and is invisible to others.
func TestTenantOwnProviderRoutesOnlyForOwner(t *testing.T) {
	up := fakeProvider(t, "from alice's own key")
	r, _ := router.New(router.Options{Store: store.NewMemoryStore(), MaxWait: 50 * time.Millisecond, TenantPolicy: router.TenantPolicy{AllowCustomBaseURL: true}})
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	alice, bob := tenantKey(t, srv, "alice"), tenantKey(t, srv, "bob")
	if c, _, b := do(t, srv, "POST", "/v1/me/providers", alice, `{"name":"mine","kind":"openai","base_url":"`+up.URL+`","model":"m","api_key":"k","rpm":10}`); c != 201 {
		t.Fatalf("add: %d %s", c, b)
	}
	if c, _, b := do(t, srv, "POST", "/v1/chat/completions", alice, `{"messages":[{"role":"user","content":"hi"}]}`); c != 200 || !strings.Contains(b, "alice's own key") {
		t.Fatalf("alice: %d %s", c, b)
	}
	if c, _, _ := do(t, srv, "POST", "/v1/chat/completions", bob, `{"messages":[{"role":"user","content":"hi"}]}`); c != 503 {
		t.Fatalf("bob has no providers, want 503 got %d", c)
	}
}

func TestTenantOllamaAndJSONImport(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"gemma3:4b","size":3300000000,"details":{"parameter_size":"4B"}},{"name":"llama3.2:3b","size":2000000000}]}`))
	}))
	t.Cleanup(ollama.Close)
	r, _ := router.New(router.Options{Store: store.NewMemoryStore(), OllamaURL: ollama.URL})
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	k := tenantKey(t, srv, "carol")

	_, m, _ := do(t, srv, "GET", "/v1/me/ollama", k, "")
	if m["available"] != true || len(m["models"].([]any)) != 2 {
		t.Fatalf("ollama list: %v", m)
	}
	if c, m, b := do(t, srv, "POST", "/v1/me/ollama/add", k, `{"models":["gemma3:4b"]}`); c != 200 || len(m["added"].([]any)) != 1 {
		t.Fatalf("ollama add: %d %s", c, b)
	}
	// JSON file import: valid entries added, bad ones reported individually.
	c, m, b := do(t, srv, "POST", "/v1/me/import", k, `{"providers":[
	  {"preset":"gemini","model":"gemini-2.5-flash","api_key":"AIza1","rpm":10},
	  {"name":"bad","preset":"nope","model":"x","api_key":"k"},
	  {"preset":"groq","model":"llama-x","api_key_env":"GROQ_API_KEY"}]}`)
	if c != 200 || len(m["added"].([]any)) != 1 || len(m["failed"].(map[string]any)) != 2 {
		t.Fatalf("import: %d %s", c, b)
	}
	if n := len(ownIDs(t, srv, k)); n != 2 {
		t.Fatalf("carol should own 2 (ollama + gemini), has %d", n)
	}
}

func TestTenantProviderCap(t *testing.T) {
	r, _ := router.New(router.Options{Store: store.NewMemoryStore(), TenantPolicy: router.TenantPolicy{MaxProviders: 2}})
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	k := tenantKey(t, srv, "dave")
	for i, want := range []int{201, 201, 400} {
		body := fmt.Sprintf(`{"preset":"groq","model":"m%d","api_key":"k"}`, i)
		if c, _, b := do(t, srv, "POST", "/v1/me/providers", k, body); c != want {
			t.Fatalf("#%d: %d %s", i, c, b)
		}
	}
}
