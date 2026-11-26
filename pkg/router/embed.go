package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

const (
	// embedBatchSize is how many texts go to a provider in one HTTP request. A
	// longer list is split into several requests to the SAME provider, so all
	// vectors of one call always come from one model.
	embedBatchSize = 100
	// maxEmbedTexts caps one Embed call. Send big corpora in several calls.
	maxEmbedTexts = 2000
)

// Embed turns texts into embedding vectors. It picks an embedding-capable
// provider (category="embed"), calls the OpenAI-compatible {base}/embeddings
// endpoint, and on failure fails over to the next provider, like Generate. The
// tenant's daily cap and own-key rules apply as for chat.
//
// IMPORTANT for vector search: vectors from different models (or of different
// sizes) cannot be compared. Failover can switch models in the middle of your
// data, so either configure ONE embedding model, pin it with
// EmbedRequest.Provider, or set EmbedRequest.Dimensions so a provider that
// answers with another size is skipped. Store EmbedResponse.Model and
// .Dimensions with your index.
func (r *Router) Embed(ctx context.Context, req types.EmbedRequest) (*types.EmbedResponse, error) {
	start := r.now()
	if req.TenantID == "" {
		req.TenantID = types.DefaultTenantID
	}
	if len(req.Texts) == 0 {
		return nil, fmt.Errorf("%w: EmbedRequest.Texts is required", types.ErrBadRequest)
	}
	if len(req.Texts) > maxEmbedTexts {
		return nil, fmt.Errorf("%w: at most %d texts per call (got %d); send them in several calls", types.ErrBadRequest, maxEmbedTexts, len(req.Texts))
	}
	if req.Dimensions < 0 {
		return nil, fmt.Errorf("%w: Dimensions must be 0 or more", types.ErrBadRequest)
	}
	chars := 0
	for i, t := range req.Texts {
		if strings.TrimSpace(t) == "" { // providers answer 400 for an empty input and the whole batch is lost
			return nil, fmt.Errorf("%w: text %d is empty", types.ErrBadRequest, i)
		}
		chars += len(t)
	}
	tokens := chars/4 + len(req.Texts) // rough estimate: ~4 characters per token

	tenant, err := r.store.TenantByID(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, types.ErrUnknownTenant
	}
	if tenant.Disabled {
		return nil, types.ErrTenantDisabled
	}
	ownOnly, capErr, err := r.capGate(ctx, tenant, start)
	if err != nil {
		return nil, err
	}
	cands := r.candidates(ctx, types.Request{TenantID: req.TenantID, Provider: req.Provider, Category: types.CategoryEmbed})
	if ownOnly {
		if cands = onlyOwn(cands); len(cands) == 0 {
			return nil, capErr
		}
	}
	if len(cands) == 0 {
		return nil, errors.New("airouter: no embedding providers configured (add a provider with category \"embed\")")
	}
	if r.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.requestTimeout)
		defer cancel()
	}

	var attempts []types.Attempt
	failed := map[int64]bool{}
	wait := r.maxWait
	if req.MaxWait > 0 {
		wait = req.MaxWait
	}
	deadline := start.Add(wait)
	for {
		now := r.now()
		var picked *candidate
		minWait := time.Duration(1<<62 - 1)
		reasons := map[string]string{}
		remaining := 0
		for i := range cands {
			c := &cands[i]
			if failed[c.cfg.ID] {
				continue
			}
			remaining++
			if !c.cfg.HasKey() {
				reasons[c.cfg.Name] = "no API key (set $" + c.cfg.APIKeyEnv + ")"
				continue
			}
			ok, w, _, why := r.acquire(ctx, c.cfg, c.state, now)
			if ok {
				picked = c
				break
			}
			reasons[c.cfg.Name] = why
			if w < minWait {
				minWait = w
			}
		}
		if picked == nil {
			if remaining == 0 && len(attempts) > 0 { // every provider was tried and failed
				return nil, r.allFailed(attempts)
			}
			if now.Add(minWait).After(deadline) || ctx.Err() != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if minWait == time.Duration(1<<62-1) {
					minWait = time.Hour
				}
				return nil, &types.ExhaustedError{RetryAfter: minWait, Reasons: reasons}
			}
			select {
			case <-time.After(minWait + 20*time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}

		vecs, hdr, err := r.callEmbed(ctx, picked.cfg, req.Texts, req.Dimensions)
		if err == nil {
			err = checkEmbedDims(vecs, req.Dimensions)
		}
		picked.state.release(tokens)
		if hdr != nil {
			r.observe(ctx, picked, hdr, r.now())
		}
		errs := 0
		if err != nil {
			errs = 1
		}
		_ = r.store.AddUsage(ctx, req.TenantID, picked.cfg.ID, dayOf(picked.cfg, now), 1, tokens, errs)
		if err == nil {
			return &types.EmbedResponse{
				Vectors: vecs, Dimensions: len(vecs[0]), Tokens: tokens, Provider: picked.cfg.Name, Model: picked.cfg.Model,
				Own: picked.cfg.TenantID != "", LatencyMS: r.now().Sub(start).Milliseconds(), Attempts: attempts,
			}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f := r.classify(ctx, picked, err)
		attempts = append(attempts, types.Attempt{Provider: picked.cfg.Name, Error: err.Error(), Status: f.status, Kind: f.kind})
		failed[picked.cfg.ID] = true
	}
}

// checkEmbedDims makes sure every vector has the same size, and the requested
// one when Dimensions was set. A mismatch is an unusable answer: the provider is
// skipped for this call without being punished.
func checkEmbedDims(vecs [][]float32, want int) error {
	if len(vecs) == 0 {
		return errBadResponse("embeddings: no vectors in response")
	}
	n := len(vecs[0])
	for i, v := range vecs {
		if len(v) != n {
			return errBadResponse(fmt.Sprintf("embeddings: vector %d has %d numbers, vector 0 has %d", i, len(v), n))
		}
	}
	if want > 0 && n != want {
		return errBadResponse(fmt.Sprintf("embeddings: provider returned %d dimensions, %d requested", n, want))
	}
	return nil
}

// callEmbed posts {model, input:[...]} to {base}/embeddings, in batches of
// embedBatchSize to the same provider, and returns the vectors in input order.
func (r *Router) callEmbed(ctx context.Context, p types.ProviderConfig, texts []string, dimensions int) ([][]float32, http.Header, error) {
	out := make([][]float32, 0, len(texts))
	var hdr http.Header
	for from := 0; from < len(texts); from += embedBatchSize {
		to := from + embedBatchSize
		if to > len(texts) {
			to = len(texts)
		}
		vecs, h, err := r.callEmbedBatch(ctx, p, texts[from:to], dimensions)
		if h != nil {
			hdr = h
		}
		if err != nil {
			return nil, hdr, err
		}
		out = append(out, vecs...)
	}
	return out, hdr, nil
}

func (r *Router) callEmbedBatch(ctx context.Context, p types.ProviderConfig, texts []string, dimensions int) ([][]float32, http.Header, error) {
	reqBody := map[string]any{"model": p.Model, "input": texts}
	if dimensions > 0 {
		reqBody["dimensions"] = dimensions
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if k := p.ResolveKey(); k != "" {
		httpReq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode/100 != 2 {
		ra, _ := atoiSafe(resp.Header.Get("Retry-After"))
		return nil, resp.Header, &types.HTTPError{Code: resp.StatusCode, Body: string(data), RetryAfter: time.Duration(ra) * time.Second, Header: resp.Header}
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, resp.Header, errBadResponse("bad embeddings response: " + types.Truncate(string(data), 200))
	}
	if len(parsed.Data) != len(texts) {
		return nil, resp.Header, errBadResponse(fmt.Sprintf("embeddings: got %d vectors for %d texts", len(parsed.Data), len(texts)))
	}
	vecs := make([][]float32, len(texts))
	for i, d := range parsed.Data {
		idx := d.Index
		if idx < 0 || idx >= len(vecs) || vecs[idx] != nil { // no usable index: keep response order
			idx = i
		}
		if len(d.Embedding) == 0 {
			return nil, resp.Header, errBadResponse("embeddings: empty vector in response")
		}
		vecs[idx] = d.Embedding
	}
	return vecs, resp.Header, nil
}
