package router

import (
        "context"
        "encoding/json"
        "errors"
        "net/http"
        "net/http/httptest"
        "strings"
        "sync/atomic"
        "testing"
        "time"

        "github.com/you/airouter/pkg/preset"
        "github.com/you/airouter/pkg/store"
        "github.com/you/airouter/pkg/types"
)

// okBody builds an OpenAI-compatible success body with the given text and 100 tokens.
func okBody(content string) string {
        b, _ := json.Marshal(map[string]any{
                "choices": []any{map[string]any{"message": map[string]string{"content": content}}},
                "usage":   map[string]int{"total_tokens": 100},
        })
        return string(b)
}

// fakeProvider returns an httptest.Server that calls h on every request and a
// counter of how many times it was hit.
func fakeProvider(t *testing.T, h func(w http.ResponseWriter, body map[string]any)) (*httptest.Server, *int32) {
        var hits int32
        s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                atomic.AddInt32(&hits, 1)
                var body map[string]any
                _ = json.NewDecoder(r.Body).Decode(&body)
                h(w, body)
        }))
        t.Cleanup(s.Close)
        return s, &hits
}

func alwaysOK(text string) func(http.ResponseWriter, map[string]any) {
        return func(w http.ResponseWriter, _ map[string]any) { _, _ = w.Write([]byte(okBody(text))) }
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newRouter(t *testing.T, clk *fakeClock) *Router {
        opt := Options{Store: store.NewMemoryStore(), MaxWait: 100 * time.Millisecond}
        if clk != nil {
                opt.Now = clk.now
        }
        r, err := New(opt)
        if err != nil {
                t.Fatal(err)
        }
        return r
}

func add(t *testing.T, r *Router, name, url string, prio, rpd int, mod func(*types.ProviderConfig)) {
        p := &types.ProviderConfig{Name: name, Kind: "openai", BaseURL: url, APIKey: "k",
                Model: name + "-m", Priority: prio, RPD: rpd, Workers: 4}
        if mod != nil {
                mod(p)
        }
        if err := r.SaveProvider(context.Background(), p); err != nil {
                t.Fatal(err)
        }
}

func gen(r *Router, req types.Request) (*types.Response, error) {
        if req.Prompt == "" {
                req.Prompt = "hi"
        }
        return r.Generate(context.Background(), req)
}

func TestFailoverWhenDailyCapReached(t *testing.T) {
        a, _ := fakeProvider(t, alwaysOK("from-a"))
        b, _ := fakeProvider(t, alwaysOK("from-b"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 2, nil)
        add(t, r, "b", b.URL, 2, 0, nil)
        var got []string
        for i := 0; i < 4; i++ {
                resp, err := gen(r, types.Request{})
                if err != nil {
                        t.Fatal(err)
                }
                got = append(got, resp.Provider)
        }
        if strings.Join(got, ",") != "a,a,b,b" {
                t.Fatalf("got %v", got)
        }
}

func TestDailyRateLimitPausesProviderUntilTimezoneReset(t *testing.T) {
        var healthy atomic.Bool
        a, aHits := fakeProvider(t, func(w http.ResponseWriter, _ map[string]any) {
                if !healthy.Load() {
                        w.WriteHeader(429)
                        _, _ = w.Write([]byte(`{"error":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}`))
                        return
                }
                _, _ = w.Write([]byte(okBody("from-a")))
        })
        b, _ := fakeProvider(t, alwaysOK("from-b"))
        // 20:00 UTC = 12:00 in Los Angeles, so LA midnight is ~12h away.
        clk := &fakeClock{time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)}
        r := newRouter(t, clk)
        add(t, r, "a", a.URL, 1, 0, func(p *types.ProviderConfig) { p.ResetTZ = "America/Los_Angeles" })
        add(t, r, "b", b.URL, 2, 0, nil)

        resp, err := gen(r, types.Request{})
        if err != nil || resp.Provider != "b" || len(resp.Attempts) != 1 {
                t.Fatalf("expected failover to b after a's daily 429: %+v %v", resp, err)
        }
        for i := 0; i < 3; i++ { // a must not be hit again today
                if resp, _ = gen(r, types.Request{}); resp.Provider != "b" {
                        t.Fatal("a should be paused")
                }
        }
        if *aHits != 1 {
                t.Fatalf("a was hit %d times, want 1", *aHits)
        }
        healthy.Store(true)
        clk.t = clk.t.Add(11 * time.Hour) // 23:00 LA: still same day
        if resp, _ = gen(r, types.Request{}); resp.Provider != "b" {
                t.Fatal("a must stay paused before its reset")
        }
        clk.t = clk.t.Add(2 * time.Hour) // 01:00 LA next day: reset passed
        if resp, err = gen(r, types.Request{}); err != nil || resp.Provider != "a" {
                t.Fatalf("a should be back after reset: %+v %v", resp, err)
        }
}

func TestJSONModeValidatesAndFailsOver(t *testing.T) {
        a, _ := fakeProvider(t, alwaysOK("sorry, not json"))
        b, _ := fakeProvider(t, alwaysOK("```json\n{\"ok\": true}\n```"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 0, nil)
        add(t, r, "b", b.URL, 2, 0, nil)
        resp, err := gen(r, types.Request{JSON: true})
        if err != nil || resp.Provider != "b" || string(resp.JSON) != `{"ok": true}` {
                t.Fatalf("%+v %v", resp, err)
        }
}

func TestGemmaStyleProviderGetsNoSystemRoleAndNoJSONMode(t *testing.T) {
        var bad atomic.Bool
        a, _ := fakeProvider(t, func(w http.ResponseWriter, body map[string]any) {
                for _, m := range body["messages"].([]any) {
                        if m.(map[string]any)["role"] == "system" {
                                bad.Store(true)
                        }
                }
                if _, ok := body["response_format"]; ok {
                        bad.Store(true)
                }
                first := body["messages"].([]any)[0].(map[string]any)["content"].(string)
                if !strings.Contains(first, "BE BRIEF") || !strings.Contains(first, "Return ONLY valid JSON") {
                        bad.Store(true)
                }
                _, _ = w.Write([]byte(okBody(`{"a":1}`)))
        })
        r := newRouter(t, nil)
        f := false
        add(t, r, "gemma", a.URL, 1, 0, func(p *types.ProviderConfig) { p.JSONMode, p.SystemRole = &f, &f })
        if _, err := gen(r, types.Request{System: "BE BRIEF", JSON: true}); err != nil || bad.Load() {
                t.Fatalf("err=%v bad=%v", err, bad.Load())
        }
}

func TestTenantDailyLimitAndUsage(t *testing.T) {
        a, _ := fakeProvider(t, alwaysOK("x"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 0, nil)
        ctx := context.Background()
        if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 2}); err != nil {
                t.Fatal(err)
        }
        for i := 0; i < 2; i++ {
                if _, err := gen(r, types.Request{TenantID: "acme"}); err != nil {
                        t.Fatal(err)
                }
        }
        var tl *types.TenantLimitError
        if _, err := gen(r, types.Request{TenantID: "acme"}); !errors.As(err, &tl) {
                t.Fatalf("want TenantLimitError, got %v", err)
        }
        if _, err := gen(r, types.Request{TenantID: "other"}); !errors.Is(err, types.ErrUnknownTenant) {
                t.Fatalf("want ErrUnknownTenant, got %v", err)
        }
        // Shared provider quota: default tenant's use counts too.
        if _, err := gen(r, types.Request{}); err != nil {
                t.Fatal(err)
        }
        rep, _ := r.Usage(ctx, "acme", "")
        if rep.RequestsToday != 2 || rep.Providers[0].AllRequests != 3 {
                t.Fatalf("%+v", rep)
        }
}

func TestAllExhaustedAndUsedTodayHint(t *testing.T) {
        a, hits := fakeProvider(t, alwaysOK("x"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 500, func(p *types.ProviderConfig) { p.UsedTodayHint = 500 })
        var ex *types.ExhaustedError
        if _, err := gen(r, types.Request{}); !errors.As(err, &ex) || ex.RetryAfter <= 0 {
                t.Fatalf("want ExhaustedError, got %v", err)
        }
        if *hits != 0 {
                t.Fatal("must not call a provider that is out of quota")
        }
}

func TestPerMinuteLimitIsTemporaryNotDaily(t *testing.T) {
        a, _ := fakeProvider(t, alwaysOK("x"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 0, func(p *types.ProviderConfig) { p.RPM = 1 })
        if _, err := gen(r, types.Request{}); err != nil {
                t.Fatal(err)
        }
        var ex *types.ExhaustedError
        _, err := gen(r, types.Request{}) // MaxWait (100ms) < ~60s window
        if !errors.As(err, &ex) || ex.RetryAfter > time.Minute || ex.Reasons["a"] != "per-minute request limit" {
                t.Fatalf("%v %+v", err, ex)
        }
}

func TestUsagePersistsAcrossRestart(t *testing.T) {
        a, _ := fakeProvider(t, alwaysOK("x"))
        st := store.NewMemoryStore()
        r1, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        add(t, r1, "a", a.URL, 1, 2, nil)
        _, _ = gen(r1, types.Request{})
        _, _ = gen(r1, types.Request{})
        r2, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond}) // "restart"
        var ex *types.ExhaustedError
        if _, err := gen(r2, types.Request{}); !errors.As(err, &ex) {
                t.Fatalf("usage should survive restart, got %v", err)
        }
}

func TestSpreadStrategyBalances(t *testing.T) {
        a, ah := fakeProvider(t, alwaysOK("a"))
        b, bh := fakeProvider(t, alwaysOK("b"))
        r := newRouter(t, nil)
        add(t, r, "a", a.URL, 1, 100, nil)
        add(t, r, "b", b.URL, 2, 100, nil)
        for i := 0; i < 10; i++ {
                if _, err := gen(r, types.Request{Strategy: "spread"}); err != nil {
                        t.Fatal(err)
                }
        }
        if *ah != 5 || *bh != 5 {
                t.Fatalf("a=%d b=%d", *ah, *bh)
        }
}

func TestDailyPausePersistsAcrossProcessesAndCanBeCleared(t *testing.T) {
        var healthy atomic.Bool
        a, aHits := fakeProvider(t, func(w http.ResponseWriter, _ map[string]any) {
                if !healthy.Load() {
                        w.WriteHeader(429)
                        _, _ = w.Write([]byte(`{"error":"quota per day exceeded"}`))
                        return
                }
                _, _ = w.Write([]byte(okBody("from-a")))
        })
        b, _ := fakeProvider(t, alwaysOK("from-b"))
        st := store.NewMemoryStore()
        r1, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        add(t, r1, "a", a.URL, 1, 0, nil)
        add(t, r1, "b", b.URL, 2, 0, nil)
        if resp, _ := gen(r1, types.Request{}); resp.Provider != "b" {
                t.Fatal("expected failover")
        }
        // A second "process" (like running the CLI again) must not retry a.
        r2, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        if resp, _ := gen(r2, types.Request{}); resp.Provider != "b" || *aHits != 1 {
                t.Fatalf("a was retried: hits=%d", *aHits)
        }
        st2 := r2.Status(context.Background(), "")
        if !strings.HasPrefix(st2[0].State, "paused") {
                t.Fatalf("status: %q", st2[0].State)
        }
        healthy.Store(true)
        if err := r2.ClearPause(context.Background(), st2[0].ID); err != nil {
                t.Fatal(err)
        }
        if resp, _ := gen(r2, types.Request{}); resp.Provider != "a" {
                t.Fatalf("a should be usable after ClearPause, got %s", resp.Provider)
        }
}

// TestBootstrapImportsFreePool verifies the bootstrap command adds catalog
// entries that have their env var set (and are in the requested category),
// and skips the ones that don't.
func TestBootstrapImportsFreePool(t *testing.T) {
        t.Setenv("GROQ_API_KEY", "fake-key-for-test")
        t.Setenv("GEMINI_API_KEY", "fake-key-for-test")
        // Cerebras, SambaNova, OpenRouter, Mistral, OpenAI all unset → must be skipped.
        st := store.NewMemoryStore()
        r, err := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        if err != nil {
                t.Fatal(err)
        }
        // Default categories = text only.
        added, skipped, err := r.BootstrapProviders(context.Background(), false, "", nil)
        if err != nil {
                t.Fatal(err)
        }
        // Text chat models with GROQ + GEMINI env set:
        //   Groq (3): openai/gpt-oss-120b, openai/gpt-oss-20b, qwen/qwen3.8-27b
        //   Gemini (8): 3.5-flash-lite, 3.1-flash-lite, 3.8-flash, 3.6-flash, 3-flash,
        //               2.5-flash-lite, 2.5-flash, 4-hydrogen
        //   Gemma (2): gemma-4-31b-it, gemma-4-26b-it
        //   Ollama (1): gemma3:4b
        //   = 14 added
        // Skipped for missing env: cerebras, sambanova, 2 openrouter = 4
        if len(added) != 14 {
                t.Fatalf("added %d providers, want 14:\n  %s", len(added), strings.Join(added, "\n  "))
        }
        if len(skipped) != 4 {
                t.Fatalf("skipped %d, want 4:\n  %s", len(skipped), strings.Join(skipped, "\n  "))
        }
        // The added Groq provider should have the catalog's RPM/RPD/TPM baked in.
        ps, _ := st.ListProviders(context.Background())
        var groq120b types.ProviderConfig
        for _, p := range ps {
                if p.Model == "openai/gpt-oss-120b" {
                        groq120b = p
                }
        }
        if groq120b.Model == "" {
                t.Fatalf("groq openai/gpt-oss-120b not added")
        }
        if groq120b.RPM != 30 || groq120b.RPD != 1000 || groq120b.TPM != 8000 {
                t.Fatalf("groq limits not baked in: %+v", groq120b)
        }
}

// TestBootstrapOnlyPreset verifies the -preset filter.
func TestBootstrapOnlyPreset(t *testing.T) {
        t.Setenv("GROQ_API_KEY", "fake-key-for-test")
        st := store.NewMemoryStore()
        r, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        added, _, err := r.BootstrapProviders(context.Background(), false, "groq", nil)
        if err != nil {
                t.Fatal(err)
        }
        // 3 Groq text chat models + 2 Orpheus TTS + 2 Whisper STT + 3 safety = 10
        // when categories is nil (all). But default = text only, so 3.
        if len(added) != 3 {
                t.Fatalf("expected 3 groq text entries, got %d: %v", len(added), added)
        }
}

// TestBootstrapCategoryFilter verifies the category parameter filters correctly.
func TestBootstrapCategoryFilter(t *testing.T) {
        t.Setenv("GROQ_API_KEY", "fake-key-for-test")
        t.Setenv("GEMINI_API_KEY", "fake-key-for-test")
        st := store.NewMemoryStore()
        r, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})

        // TTS only — 4 Gemini TTS + 2 Groq Orpheus = 6
        added, _, err := r.BootstrapProviders(context.Background(), false, "", []string{"tts"})
        if err != nil {
                t.Fatal(err)
        }
        if len(added) != 6 {
                t.Fatalf("tts: expected 6, got %d: %v", len(added), added)
        }

        // STT only — 1 Gemini Transcribe + 2 Groq Whisper = 3
        st2 := store.NewMemoryStore()
        r2, _ := New(Options{Store: st2, MaxWait: 50 * time.Millisecond})
        added, _, err = r2.BootstrapProviders(context.Background(), false, "", []string{"stt"})
        if err != nil {
                t.Fatal(err)
        }
        if len(added) != 3 {
                t.Fatalf("stt: expected 3, got %d: %v", len(added), added)
        }

        // Text + TTS together
        st3 := store.NewMemoryStore()
        r3, _ := New(Options{Store: st3, MaxWait: 50 * time.Millisecond})
        added, _, err = r3.BootstrapProviders(context.Background(), false, "", []string{"text", "tts"})
        if err != nil {
                t.Fatal(err)
        }
        // 14 text + 6 tts = 20
        if len(added) != 20 {
                t.Fatalf("text+tts: expected 20, got %d: %v", len(added), added)
        }
}

// TestBootstrapAllCategories verifies the three-way distinction:
//   nil           → default (text only)
//   []string{}    → all categories
//   ["text", ...] → only those
func TestBootstrapAllCategories(t *testing.T) {
        t.Setenv("GROQ_API_KEY", "fake-key-for-test")
        t.Setenv("GEMINI_API_KEY", "fake-key-for-test")

        // nil → text only
        st := store.NewMemoryStore()
        r, _ := New(Options{Store: st, MaxWait: 50 * time.Millisecond})
        added, _, err := r.BootstrapProviders(context.Background(), false, "", nil)
        if err != nil {
                t.Fatal(err)
        }
        for _, name := range added {
                if strings.Contains(name, "tts") || strings.Contains(name, "whisper") || strings.Contains(name, "transcribe") {
                        t.Fatalf("nil categories should default to text-only but got %s", name)
                }
        }

        // empty slice → everything (text + tts + stt + embedding + live + agent + safety + image)
        st2 := store.NewMemoryStore()
        r2, _ := New(Options{Store: st2, MaxWait: 50 * time.Millisecond})
        added, _, err = r2.BootstrapProviders(context.Background(), false, "", []string{})
        if err != nil {
                t.Fatal(err)
        }
        // 39 total entries in the catalog (Nov 2026). Some are skipped because
        // their env vars aren't set, but the added+skipped count should be 39.
        // We don't hard-code 39 (catalog grows); we just check that non-text
        // categories appear when categories is empty.
        totalEntries := len(preset.FreePool)
        got := len(added)
        if got < 14 { // at least the text-only count
                t.Fatalf("empty categories should add at least the text entries, got %d", got)
        }
        // Look for at least one non-text entry in the skipped list (TTS, STT, etc.
        // are skipped because the env vars we set are GROQ + GEMINI which apply
        // to them too — so they should actually be ADDED, not skipped).
        foundNonText := false
        for _, name := range added {
                if strings.Contains(name, "tts") || strings.Contains(name, "whisper") || strings.Contains(name, "transcribe") || strings.Contains(name, "embedding") {
                        foundNonText = true
                        break
                }
        }
        if !foundNonText {
                t.Fatalf("empty categories should add non-text entries too, got only: %v", added)
        }
        _ = totalEntries
}

// TestPresetDefinitionsAreComplete sanity-checks the catalog: every preset
// referenced by FreePool must exist in Definitions.
func TestPresetDefinitionsAreComplete(t *testing.T) {
        for _, m := range preset.FreePool {
                if _, ok := preset.Definitions[m.Preset]; !ok {
                        t.Fatalf("free pool entry %q references unknown preset %q", m.Model, m.Preset)
                }
        }
}
