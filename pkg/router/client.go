package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// buildMessages turns a Request into the message list a provider expects.
// System is optional. Either Prompt (single user turn) or Messages can be used.
func buildMessages(req types.Request) ([]types.Message, error) {
	var msgs []types.Message
	if req.System != "" {
		msgs = append(msgs, types.Message{Role: "system", Content: req.System})
	}
	if len(req.Messages) > 0 {
		msgs = append(msgs, req.Messages...)
	} else if req.Prompt != "" {
		msgs = append(msgs, types.Message{Role: "user", Content: req.Prompt})
	}
	hasUser := false
	for _, m := range msgs {
		if m.Role == "user" {
			hasUser = true
			break
		}
	}
	if !hasUser {
		return nil, fmt.Errorf("%w: a prompt or at least one user message is required", types.ErrBadRequest)
	}
	return msgs, nil
}

// foldSystem merges system messages into the first user message, for models
// that reject a dedicated system role (e.g. Gemma).
func foldSystem(msgs []types.Message) []types.Message {
	var sys []string
	var rest []types.Message
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m.Content)
		} else {
			rest = append(rest, m)
		}
	}
	if len(sys) == 0 {
		return msgs
	}
	for i := range rest {
		if rest[i].Role == "user" {
			rest[i].Content = strings.Join(sys, "\n\n") + "\n\n" + rest[i].Content
			break
		}
	}
	return rest
}

// callProvider sends one request to one provider and returns (text, totalTokens, error).
// It builds the right body shape for OpenAI-compatible vs Ollama, applies
// JSON-mode and system-role quirks, and turns non-2xx responses into HTTPError.
func (r *Router) callProvider(ctx context.Context, p types.ProviderConfig, msgs []types.Message, req types.Request) (string, int, http.Header, error) {
	// Gemma-style provider: no response_format, so ask for JSON in words.
	if req.JSON && p.Kind != "ollama" && !p.UseJSONMode() {
		msgs = append([]types.Message(nil), msgs...)
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				msgs[i].Content += "\n\nReturn ONLY valid JSON. No markdown fences, no commentary."
				break
			}
		}
	}
	if !p.UseSystemRole() {
		msgs = foldSystem(msgs)
	}

	var url string
	var body map[string]any
	if p.Kind == "ollama" {
		url = p.BaseURL + "/api/chat"
		opts := map[string]any{"num_ctx": 8192}
		if req.Temperature != nil {
			opts["temperature"] = *req.Temperature
		}
		if req.MaxTokens > 0 {
			opts["num_predict"] = req.MaxTokens
		}
		body = map[string]any{"model": p.Model, "messages": msgs, "stream": false, "options": opts}
		if req.JSON {
			body["format"] = "json"
		}
	} else {
		url = p.BaseURL + "/chat/completions"
		body = map[string]any{"model": p.Model, "messages": msgs}
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if req.MaxTokens > 0 {
			body["max_tokens"] = req.MaxTokens
		}
		if req.JSON && p.UseJSONMode() {
			body["response_format"] = map[string]string{"type": "json_object"}
		}
	}

	raw, _ := json.Marshal(body)
	callCtx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", 0, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("Authorization", "Bearer "+k)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return "", 0, resp.Header, &types.HTTPError{Code: resp.StatusCode, Body: string(data),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), r.now()), Header: resp.Header}
	}

	if p.Kind == "ollama" {
		var d struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			PromptEval int `json:"prompt_eval_count"`
			Eval       int `json:"eval_count"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", 0, resp.Header, errBadResponse("bad ollama response: " + err.Error())
		}
		return d.Message.Content, d.PromptEval + d.Eval, resp.Header, nil
	}

	var d struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &d); err != nil || len(d.Choices) == 0 {
		return "", 0, resp.Header, errBadResponse("bad response: " + types.Truncate(string(data), 160))
	}
	return d.Choices[0].Message.Content, d.Usage.Total, resp.Header, nil
}

// extractJSON pulls a valid JSON value out of model output: handles ``` fences
// and surrounding chatter. Used when the provider's JSON mode is off or when
// the model ignores it.
func extractJSON(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), true
	}
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		i, j := strings.Index(s, pair[0]), strings.LastIndex(s, pair[1])
		if i >= 0 && j > i && json.Valid([]byte(s[i:j+1])) {
			return json.RawMessage(s[i : j+1]), true
		}
	}
	return nil, false
}
