package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// embedServer answers POST /embeddings with one 2-number vector per input
// (value = input length) and records the path.
func embedServer(t *testing.T, status int, path *string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path != nil {
			*path = r.URL.Path
		}
		if status != 200 {
			http.Error(w, "boom", status)
			return
		}
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var out struct {
			Data []item `json:"data"`
		}
		// answer in reverse order to prove the router restores input order via index
		for i := len(body.Input) - 1; i >= 0; i-- {
			out.Data = append(out.Data, item{Index: i, Embedding: []float32{float32(len(body.Input[i])), 1}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(s.Close)
	return s
}

func embedMod(p *types.ProviderConfig) { p.Category = types.CategoryEmbed }

func TestEmbedReturnsVectorsInOrder(t *testing.T) {
	var path string
	srv := embedServer(t, 200, &path)
	r := newCapRouter(t, 100*time.Millisecond)
	add(t, r, "emb", srv.URL, 1, 0, embedMod)
	resp, err := r.Embed(context.Background(), types.EmbedRequest{Texts: []string{"a", "abc", "ab"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/embeddings" || len(resp.Vectors) != 3 || resp.Vectors[0][0] != 1 || resp.Vectors[1][0] != 3 || resp.Vectors[2][0] != 2 {
		t.Fatalf("path=%q vectors=%v", path, resp.Vectors)
	}
	if resp.Provider != "emb" || resp.Own {
		t.Fatalf("bad meta: %+v", resp)
	}
}

func TestEmbedFailsOverAndNeedsProviders(t *testing.T) {
	bad := embedServer(t, 500, nil)
	good := embedServer(t, 200, nil)
	r := newCapRouter(t, 100*time.Millisecond)
	if _, err := r.Embed(context.Background(), types.EmbedRequest{Texts: []string{"x"}}); err == nil {
		t.Fatal("no embedding providers should be an error")
	}
	add(t, r, "bad", bad.URL, 1, 0, embedMod)
	add(t, r, "good", good.URL, 2, 0, embedMod)
	resp, err := r.Embed(context.Background(), types.EmbedRequest{Texts: []string{"x"}})
	if err != nil || resp.Provider != "good" || len(resp.Attempts) != 1 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if _, err := r.Embed(context.Background(), types.EmbedRequest{}); !errors.Is(err, types.ErrBadRequest) {
		t.Fatalf("empty texts: %v", err)
	}
}

// A pool-only cap that is used up blocks the shared pool for embeddings (and
// speech), but the tenant's own embedding key keeps working.
func TestEmbedPoolOnlyCap(t *testing.T) {
	pool := embedServer(t, 200, nil)
	own := embedServer(t, 200, nil)
	r := newCapRouter(t, 100*time.Millisecond)
	add(t, r, "pool", pool.URL, 1, 0, embedMod)
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 1, PoolOnlyCap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Embed(ctx, types.EmbedRequest{TenantID: "acme", Texts: []string{"x"}}); err != nil {
		t.Fatalf("first pool request: %v", err)
	}
	var tl *types.TenantLimitError
	if _, err := r.Embed(ctx, types.EmbedRequest{TenantID: "acme", Texts: []string{"x"}}); !errors.As(err, &tl) {
		t.Fatalf("pool cap used up should give TenantLimitError, got %v", err)
	}
	if _, err := r.AddTenantProvider(ctx, "acme", types.ProviderConfig{Name: "mine", Kind: "openai", BaseURL: own.URL, APIKey: "k", Model: "m", Category: types.CategoryEmbed, Priority: 1}); err != nil {
		t.Fatal(err)
	}
	resp, err := r.Embed(ctx, types.EmbedRequest{TenantID: "acme", Texts: []string{"x"}})
	if err != nil || !resp.Own {
		t.Fatalf("own key should still answer: %+v %v", resp, err)
	}
}
