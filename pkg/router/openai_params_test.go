package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// OpenAI's own API wants max_completion_tokens, and its reasoning models reject
// a custom temperature. Other OpenAI-compatible providers keep max_tokens.
func TestOpenAIParameterNames(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	temp := 0.2
	cases := []struct {
		name, base, model   string
		wantMaxKey          string
		wantTemperatureSent bool
	}{
		{"openai gpt-4o", "/api.openai.com/v1", "gpt-4o-mini", "max_completion_tokens", true},
		{"openai gpt-5", "/api.openai.com/v1", "gpt-5-mini", "max_completion_tokens", false},
		{"openai o3", "/api.openai.com/v1", "o3-mini", "max_completion_tokens", false},
		{"groq", "/api.groq.com/openai/v1", "llama", "max_tokens", true},
		{"other", "", "m", "max_tokens", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRouter(t, nil)
			add(t, r, "p", srv.URL+c.base, 1, 0, func(p *types.ProviderConfig) { p.Model = c.model })
			if _, err := r.Generate(context.Background(), types.Request{Prompt: "hi", MaxTokens: 50, Temperature: &temp}); err != nil {
				t.Fatal(err)
			}
			if _, ok := got[c.wantMaxKey]; !ok {
				t.Fatalf("want %s in body, got %v", c.wantMaxKey, got)
			}
			other := "max_tokens"
			if c.wantMaxKey == "max_tokens" {
				other = "max_completion_tokens"
			}
			if _, bad := got[other]; bad {
				t.Fatalf("%s must not be sent: %v", other, got)
			}
			if _, sent := got["temperature"]; sent != c.wantTemperatureSent {
				t.Fatalf("temperature sent=%v, want %v", sent, c.wantTemperatureSent)
			}
		})
	}
}
