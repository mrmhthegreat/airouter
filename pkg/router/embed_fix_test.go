package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// dimServer answers /embeddings with vectors of `dim` numbers (value = input
// position) and records every request it gets.
type dimServer struct {
	*httptest.Server
	mu     sync.Mutex
	sizes  []int // number of inputs per request
	dimReq []int // "dimensions" value received per request
}

func newDimServer(t *testing.T, dim, status int) *dimServer {
	d := &dimServer{}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		d.mu.Lock()
		d.sizes = append(d.sizes, len(b.Input))
		d.dimReq = append(d.dimReq, b.Dimensions)
		d.mu.Unlock()
		if status != 200 {
			http.Error(w, `{"error":"nope"}`, status)
			return
		}
		type it struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var out struct {
			Data []it `json:"data"`
		}
		for i, text := range b.Input {
			v := make([]float32, dim)
			v[0] = float32(len(text)) // lets a test see which text became which vector
			out.Data = append(out.Data, it{i, v})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *dimServer) calls() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.sizes) }

func TestEmbedRejectsBadInputBeforeCallingAnyone(t *testing.T) {
	srv := newDimServer(t, 4, 200)
	r := newRouter(t, nil)
	add(t, r, "e", srv.URL, 1, 0, embedMod)
	ctx := context.Background()
	for name, texts := range map[string][]string{"blank": {"ok", "   "}, "empty string": {""}} {
		if _, err := r.Embed(ctx, types.EmbedRequest{Texts: texts}); !errors.Is(err, types.ErrBadRequest) {
			t.Errorf("%s: want ErrBadRequest, got %v", name, err)
		}
	}
	many := make([]string, maxEmbedTexts+1)
	for i := range many {
		many[i] = "x"
	}
	if _, err := r.Embed(ctx, types.EmbedRequest{Texts: many}); !errors.Is(err, types.ErrBadRequest) {
		t.Errorf("too many texts: %v", err)
	}
	if srv.calls() != 0 {
		t.Fatalf("provider was called %d times for invalid input", srv.calls())
	}
}

// A long list is split into batches to the SAME provider and comes back in order.
func TestEmbedBatchesToOneProviderInOrder(t *testing.T) {
	srv := newDimServer(t, 4, 200)
	r := newRouter(t, nil)
	add(t, r, "e", srv.URL, 1, 0, embedMod)
	texts := make([]string, 250)
	for i := range texts {
		texts[i] = string(make([]byte, i+1)) // text i has length i+1
		texts[i] = fmt.Sprintf("%0*d", i%50+1, 0)
	}
	resp, err := r.Embed(context.Background(), types.EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vectors) != 250 || resp.Dimensions != 4 || resp.Tokens <= 0 {
		t.Fatalf("resp: %d vectors, dim %d, tokens %d", len(resp.Vectors), resp.Dimensions, resp.Tokens)
	}
	if fmt.Sprint(srv.sizes) != "[100 100 50]" {
		t.Fatalf("batches = %v, want [100 100 50]", srv.sizes)
	}
	for i, v := range resp.Vectors {
		if int(v[0]) != len(texts[i]) {
			t.Fatalf("vector %d belongs to another text", i)
		}
	}
}

// If a later batch fails, the whole call moves to the next provider, so one call
// never returns vectors from two different models.
func TestEmbedNeverMixesModelsWithinOneCall(t *testing.T) {
	var n atomic.Int64
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) >= 2 { // first batch fine, second batch fails
			http.Error(w, "boom", 500)
			return
		}
		var b struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		out := `{"data":[`
		for i := range b.Input {
			if i > 0 {
				out += ","
			}
			out += fmt.Sprintf(`{"index":%d,"embedding":[1,1,1]}`, i)
		}
		_, _ = w.Write([]byte(out + `]}`))
	}))
	t.Cleanup(flaky.Close)
	good := newDimServer(t, 3, 200)
	r := newRouter(t, nil)
	add(t, r, "flaky", flaky.URL, 1, 0, embedMod)
	add(t, r, "good", good.URL, 2, 0, embedMod)
	texts := make([]string, 150)
	for i := range texts {
		texts[i] = "t"
	}
	resp, err := r.Embed(context.Background(), types.EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "good" || len(resp.Attempts) != 1 || resp.Attempts[0].Kind == "" {
		t.Fatalf("expected failover to 'good' with a classified attempt: %+v", resp)
	}
	for _, v := range resp.Vectors {
		if v[1] != 0 { // 'good' vectors are [len,0,0]; 'flaky' ones are [1,1,1]
			t.Fatal("vectors from two providers were mixed")
		}
	}
}

func TestEmbedDimensionsAreEnforcedAndSkippedWithoutPenalty(t *testing.T) {
	big := newDimServer(t, 3072, 200) // ignores the dimensions parameter
	small := newDimServer(t, 768, 200)
	r := newRouter(t, nil)
	add(t, r, "gem3072", big.URL, 1, 0, embedMod)
	add(t, r, "other768", small.URL, 2, 0, embedMod)
	ctx := context.Background()
	resp, err := r.Embed(ctx, types.EmbedRequest{Texts: []string{"a"}, Dimensions: 768})
	if err != nil || resp.Provider != "other768" || resp.Dimensions != 768 {
		t.Fatalf("want other768/768, got err=%v", err)
	}
	if big.dimReq[0] != 768 || small.dimReq[0] != 768 {
		t.Fatalf("the dimensions parameter was not sent: %v %v", big.dimReq, small.dimReq)
	}
	// the mismatch is a bad answer, not an outage: gem3072 is still usable when no size is asked
	resp, err = r.Embed(ctx, types.EmbedRequest{Texts: []string{"a"}})
	if err != nil || resp.Provider != "gem3072" || resp.Dimensions != 3072 {
		t.Fatalf("provider with a wrong size was punished: err=%v", err)
	}
	// nobody can give 1536: clear error, not "limited, retry in 1h"
	_, err = r.Embed(ctx, types.EmbedRequest{Texts: []string{"a"}, Dimensions: 1536})
	var af *types.AllFailedError
	if !errors.As(err, &af) || len(af.Attempts) != 2 {
		t.Fatalf("want AllFailedError with 2 attempts, got %v", err)
	}
}

// "Every provider failed" must say why, instead of claiming the providers are
// limited for an hour. Same for speech and transcription.
func TestEveryAudioAndEmbedPathReportsRealFailure(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status   int
		wantBad  bool
		wantKind string
	}{{400, true, kindBadRequest}, {500, false, kindTransient}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", tc.status) }))
		defer srv.Close()
		check := func(name string, err error) {
			t.Helper()
			var br *types.BadRequestError
			var af *types.AllFailedError
			var ex *types.ExhaustedError
			switch {
			case errors.As(err, &ex):
				t.Errorf("%s/%d: reported as 'limited, retry later': %v", name, tc.status, err)
			case tc.wantBad && !errors.As(err, &br):
				t.Errorf("%s/%d: want BadRequestError, got %v", name, tc.status, err)
			case !tc.wantBad && !errors.As(err, &af):
				t.Errorf("%s/%d: want AllFailedError, got %v", name, tc.status, err)
			}
			var atts []types.Attempt
			if br != nil {
				atts = br.Attempts
			} else if af != nil {
				atts = af.Attempts
			}
			if len(atts) != 1 || atts[0].Kind != tc.wantKind || atts[0].Status != tc.status {
				t.Errorf("%s/%d: attempt lacks status/kind: %+v", name, tc.status, atts)
			}
		}
		re := newRouter(t, nil)
		add(t, re, "e", srv.URL, 1, 0, embedMod)
		_, err := re.Embed(ctx, types.EmbedRequest{Texts: []string{"a"}})
		check("embed", err)

		rs := newRouter(t, nil)
		add(t, rs, "s", srv.URL, 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategoryTTS })
		_, err = rs.Speak(ctx, types.SpeakRequest{Text: "hi"})
		check("speak", err)

		rt := newRouter(t, nil)
		add(t, rt, "t", srv.URL, 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategorySTT })
		_, err = rt.Transcribe(ctx, types.TranscribeRequest{Audio: []byte("x"), Filename: "a.wav"})
		check("transcribe", err)
	}
}

// own_requests_today must count what the tenant's own models SERVED, and must not
// be inflated by failed attempts on the shared pool.
func TestOverviewOwnAndPoolCountsIgnoreFailures(t *testing.T) {
	r, _ := New(Options{Store: store.NewMemoryStore(), TenantPolicy: TenantPolicy{AllowCustomBaseURL: true}})
	ctx := context.Background()
	add(t, r, "pool", "http://x", 1, 0, nil)
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 100, PoolOnlyCap: true}); err != nil {
		t.Fatal(err)
	}
	own, err := r.AddTenantProvider(ctx, "acme", types.ProviderConfig{Name: "mine", Kind: "openai", BaseURL: "http://y", APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var poolID int64
	for _, s := range r.Status(ctx, "acme") {
		if s.Name == "pool" {
			poolID = s.ID
		}
	}
	day := r.now().UTC().Format("2006-01-02")
	_ = r.Store().AddUsage(ctx, "acme", poolID, day, 5, 0, 2) // 5 attempts, 2 failed: 3 served
	_ = r.Store().AddUsage(ctx, "acme", own.ID, day, 4, 0, 1) // 4 attempts, 1 failed: 3 served
	ov, err := r.TenantOverview(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if ov.Cap.Used != 3 || ov.Cap.PoolToday != 3 || ov.Cap.OwnToday != 3 {
		t.Fatalf("used=%d pool=%d own=%d, want 3/3/3", ov.Cap.Used, ov.Cap.PoolToday, ov.Cap.OwnToday)
	}
}

func TestEmbedTokensAreRecorded(t *testing.T) {
	srv := newDimServer(t, 2, 200)
	r := newRouter(t, nil)
	add(t, r, "e", srv.URL, 1, 0, embedMod)
	if _, err := r.Embed(context.Background(), types.EmbedRequest{Texts: []string{"hello world, this is a sentence"}}); err != nil {
		t.Fatal(err)
	}
	u, err := r.Usage(context.Background(), types.DefaultTenantID, "")
	if err != nil || u.TokensToday <= 0 {
		t.Fatalf("tokens not recorded: %v %+v", err, u)
	}
}
