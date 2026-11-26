package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeEmbedUpstream(t *testing.T, dim int) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		d := dim
		var items []string
		for i := range b.Input {
			vec := make([]string, d)
			for k := range vec {
				vec[k] = "0.5"
			}
			items = append(items, fmt.Sprintf(`{"index":%d,"embedding":[%s]}`, i, strings.Join(vec, ",")))
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestEmbeddingsEndpoint(t *testing.T) {
	up := fakeEmbedUpstream(t, 4)
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	_, m, _ := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"acme"}`)
	key := m["api_key"].(string)
	if c, _, b := do(t, srv, "POST", "/v1/providers", "admin",
		`{"preset":"openai","name":"emb","base_url":"`+up.URL+`","api_key":"k","model":"embed-m","category":"embed"}`); c != 201 {
		t.Fatalf("provider: %d %s", c, b)
	}

	// one string, "auto"
	c, m, b := do(t, srv, "POST", "/v1/embeddings", key, `{"model":"auto","input":"hello"}`)
	if c != 200 || m["object"] != "list" || len(m["data"].([]any)) != 1 {
		t.Fatalf("single: %d %s", c, b)
	}
	d0 := m["data"].([]any)[0].(map[string]any)
	if d0["object"] != "embedding" || len(d0["embedding"].([]any)) != 4 || m["model"] != "embed-m" {
		t.Fatalf("shape: %s", b)
	}
	if ai := m["airouter"].(map[string]any); ai["dimensions"].(float64) != 4 || ai["provider"] != "emb" {
		t.Fatalf("airouter block: %v", ai)
	}
	if u := m["usage"].(map[string]any); u["total_tokens"].(float64) <= 0 {
		t.Fatalf("usage: %v", u)
	}
	// a list, pinned by model name, with dimensions
	c, m, b = do(t, srv, "POST", "/v1/embeddings", key, `{"model":"embed-m","input":["a","bb","ccc"],"dimensions":4}`)
	if c != 200 || len(m["data"].([]any)) != 3 {
		t.Fatalf("list: %d %s", c, b)
	}
	// wrong size requested: every provider is skipped, error says why (not "limited")
	c, _, b = do(t, srv, "POST", "/v1/embeddings", key, `{"input":"x","dimensions":768}`)
	if c != 502 || strings.Contains(b, "limited") {
		t.Fatalf("dimension mismatch: %d %s", c, b)
	}
	// errors
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"unknown model":  {`{"model":"nope","input":"x"}`, 404},
		"bad input type": {`{"input":123}`, 400},
		"blank text":     {`{"input":["ok","  "]}`, 400},
		"base64":         {`{"input":"x","encoding_format":"base64"}`, 400},
		"bad json":       {`{`, 400},
	} {
		if c, _, b := do(t, srv, "POST", "/v1/embeddings", key, tc.body); c != tc.code {
			t.Errorf("%s: got %d want %d (%s)", name, c, tc.code, b)
		}
	}
	if c, _, _ := do(t, srv, "POST", "/v1/embeddings", "", `{"input":"x"}`); c != 401 {
		t.Fatalf("no key: %d", c)
	}
}
