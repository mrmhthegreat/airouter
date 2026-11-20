//go:build ignore

// Example: standalone Go program that calls a running airouter HTTP server
// with a tenant API key. Stdlib only — copy into any module and `go run` it.
//
//	./bin/airouter serve -addr :8080
//	export AIROUTER_KEY=$(cat .airouter-key)
//	go run examples/http-client.go
//
// Demonstrates POST /v1/chat/completions, POST /v1/audio/speech,
// GET /v1/me/providers, POST /v1/me/providers.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	key := os.Getenv("AIROUTER_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "set AIROUTER_KEY (air_...). Get one: ./bin/airouter serve  (prints + saves ./.airouter-key)")
		os.Exit(1)
	}
	base := os.Getenv("AIROUTER_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	c := &http.Client{Timeout: 60 * time.Second}
	for _, s := range []struct {
		name string
		fn   func(*http.Client, string, string) error
	}{
		{"chat", chatExample},
		{"speak", speakExample},
		{"list providers", listMyProviders},
		{"add provider", addMyProvider},
	} {
		if err := s.fn(c, key, base); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", s.name, err)
		}
	}
}

// chatExample calls POST /v1/chat/completions with model "auto".
func chatExample(c *http.Client, key, base string) error {
	raw, err := doJSON(c, key, "POST", base+"/v1/chat/completions", map[string]any{
		"model":    "auto",
		"messages": []map[string]string{{"role": "user", "content": "Say one short sentence about the ocean."}},
	})
	if err != nil {
		return err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, trunc(string(raw)))
	}
	if len(out.Choices) == 0 {
		return fmt.Errorf("no choices: %s", trunc(string(raw)))
	}
	fmt.Println("chat reply:", out.Choices[0].Message.Content)
	return nil
}

// speakExample calls POST /v1/audio/speech and writes the WAV to /tmp/airouter-hello.wav.
func speakExample(c *http.Client, key, base string) error {
	body, _ := json.Marshal(map[string]any{
		"model": "auto", "input": "Hello from the airouter HTTP client.",
		"voice": "Kore", "response_format": "wav",
	})
	req, _ := http.NewRequest("POST", base+"/v1/audio/speech", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if err := os.WriteFile("/tmp/airouter-hello.wav", audio, 0o644); err != nil {
		return err
	}
	fmt.Printf("speak: wrote %d bytes to /tmp/airouter-hello.wav (provider=%s)\n", len(audio), resp.Header.Get("X-AIRouter-Provider"))
	return nil
}

// listMyProviders calls GET /v1/me/providers and prints one line per provider.
func listMyProviders(c *http.Client, key, base string) error {
	raw, err := doJSON(c, key, "GET", base+"/v1/me/providers", nil)
	if err != nil {
		return err
	}
	var resp struct {
		Own []map[string]any `json:"own"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, trunc(string(raw)))
	}
	rows := resp.Own
	if len(rows) == 0 {
		fmt.Println("me providers: (none yet — run: airouter me pick -preset groq -key-stdin MODEL)")
		return nil
	}
	fmt.Printf("me providers: %d\n", len(rows))
	for _, p := range rows {
		fmt.Printf("  %v  %-4v  %-32v  %-32v  state=%v  key=%v\n", p["id"], p["category"], p["name"], p["model"], p["state"], p["key_hint"])
	}
	return nil
}

// addMyProvider calls POST /v1/me/providers. Provider key is $GROQ_API_KEY;
// limits match the free-pool catalog entry for openai/gpt-oss-120b.
func addMyProvider(c *http.Client, key, base string) error {
	providerKey := os.Getenv("GROQ_API_KEY")
	if providerKey == "" {
		fmt.Println("add provider: skipping (set GROQ_API_KEY to demo adding a provider)")
		return nil
	}
	raw, err := doJSON(c, key, "POST", base+"/v1/me/providers", map[string]any{
		"preset": "groq", "model": "openai/gpt-oss-120b", "category": "text",
		"rpm": 30, "rpd": 1000, "tpm": 8000, "priority": 10, "api_key": providerKey,
	})
	if err != nil {
		return err
	}
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, trunc(string(raw)))
	}
	fmt.Printf("add provider: created #%d %q\n", out.ID, out.Name)
	return nil
}

// doJSON sends a JSON body and returns the response body. Sets Authorization
// from key; status codes >= 400 become Go errors with the body attached.
func doJSON(c *http.Client, key, method, url string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trunc(string(data)))
	}
	return data, nil
}

func trunc(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
