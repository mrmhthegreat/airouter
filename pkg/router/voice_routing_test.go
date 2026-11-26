package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// A router with a Groq-like TTS (best priority) and an ElevenLabs provider. groqStatus
// is what the Groq-like provider answers: 400 imitates a refusal of an unknown voice.
func voiceRoutingSetup(t *testing.T, groqStatus int) (r *Router, groqHits *atomic.Int64, el *fakeEL, sharedID string) {
	el = newFakeEL(t)
	groqHits = &atomic.Int64{}
	groq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		groqHits.Add(1)
		if groqStatus != 200 {
			http.Error(w, `{"error":{"message":"voice is unsupported for this model"}}`, groqStatus)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(pcmToWAV(rawPCM(800), 24000, 1, 16))
	}))
	t.Cleanup(groq.Close)
	r = newELRouter(t, TenantPolicy{AllowSharedVoiceClone: true})
	add(t, r, "groq-tts", groq.URL+"/api.groq.com/openai/v1", 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategoryTTS })
	addEL(t, r, "el", el, "k", nil) // priority 5: ranked AFTER groq-tts
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "bob"}); err != nil {
		t.Fatal(err)
	}
	v, err := r.CloneVoice(ctx, types.CloneVoiceRequest{Name: "HouseVoice", Samples: sampleFiles(), ConsentConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ShareVoice(ctx, "", v.ID, true); err != nil {
		t.Fatal(err)
	}
	return r, groqHits, el, v.ID
}

func vrState(r *Router, tenant, name string) string {
	for _, s := range r.Status(context.Background(), tenant) {
		if s.Name == name {
			return s.State
		}
	}
	return "?"
}

// An ElevenLabs voice must never be sent to Groq or Gemini first: they refuse it with
// HTTP 400, and three such answers in a row pause that provider for 24 hours for EVERY tenant.
func TestElevenLabsVoicesAreNeverSentToOtherProviders(t *testing.T) {
	r, groqHits, _, sharedID := voiceRoutingSetup(t, 400)
	ctx := context.Background()
	for _, voice := range []string{"Rachel", "rachel", "ada", "HouseVoice", sharedID, "premade0000000000002"} {
		for i := 0; i < 4; i++ {
			if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: voice}); err != nil {
				t.Fatalf("voice %q: %v", voice, err)
			}
		}
	}
	// a well-formed id that does not exist is a clean "voice not found", also without touching Groq
	for i := 0; i < 4; i++ {
		if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: "zzzzzzzzzzzzzzzzzzzz"}); !errors.Is(err, types.ErrVoiceNotFound) {
			t.Fatalf("unknown id: %v", err)
		}
	}
	if groqHits.Load() != 0 {
		t.Fatalf("the non-ElevenLabs provider was asked for an ElevenLabs voice %d times", groqHits.Load())
	}
	if st := vrState(r, "bob", "groq-tts"); st != "ok" {
		t.Fatalf("groq-tts was paused: %q", st)
	}
}

// Voices that belong to other providers keep going to them.
func TestOtherProvidersVoicesAreNotRedirected(t *testing.T) {
	r, groqHits, el, _ := voiceRoutingSetup(t, 200)
	ctx := context.Background()
	for _, voice := range []string{"tara", "Kore", ""} {
		resp, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: voice})
		if err != nil || resp.Provider != "groq-tts" {
			t.Fatalf("voice %q should be served by groq-tts: %v %+v", voice, err, resp)
		}
	}
	if groqHits.Load() != 3 || el.hit("tts") != 0 {
		t.Fatalf("groq=%d el=%d", groqHits.Load(), el.hit("tts"))
	}
	// and an ElevenLabs voice still goes to ElevenLabs even though Groq answers fine
	resp, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: "Rachel"})
	if err != nil || resp.Provider != "el" {
		t.Fatalf("Rachel: %v %+v", err, resp)
	}
}

func TestSharedVoiceLifecycle(t *testing.T) {
	r, _, _, sharedID := voiceRoutingSetup(t, 400) // the other provider refuses voices it does not know, like Groq
	ctx := context.Background()
	find := func(tenant string) *types.Voice {
		vs, err := r.ListVoices(ctx, tenant, ListVoicesOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range vs {
			if vs[i].ID == sharedID {
				return &vs[i]
			}
		}
		return nil
	}
	if v := find("bob"); v == nil || !v.Shared || v.Cloned {
		t.Fatalf("bob should see the shared voice, not as his own clone: %+v", v)
	}
	if v := find(types.DefaultTenantID); v == nil || !v.Shared || !v.Cloned {
		t.Fatalf("the operator sees it as shared and cloned: %+v", v)
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: "HouseVoice"}); err != nil {
		t.Fatal(err)
	}
	// tenants cannot share anything, and cannot touch the operator's voice
	bv, err := r.CloneVoice(ctx, types.CloneVoiceRequest{TenantID: "bob", Name: "Bobby", Samples: sampleFiles(), ConsentConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ShareVoice(ctx, "bob", bv.ID, true); !errors.Is(err, types.ErrBadRequest) {
		t.Fatalf("a tenant must not share a voice: %v", err)
	}
	if err := r.ShareVoice(ctx, "bob", sharedID, false); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("a tenant must not unshare the operator's voice: %v", err)
	}
	if err := r.DeleteVoice(ctx, "bob", sharedID); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("a tenant must not delete the operator's voice: %v", err)
	}
	if err := r.ShareVoice(ctx, "", "nonexistentvoiceid000", true); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("unknown voice: %v", err)
	}
	// bob's own clone stays private to bob
	if vs, _ := r.ListVoices(ctx, types.DefaultTenantID, ListVoicesOptions{}); func() bool {
		for _, v := range vs {
			if v.ID == bv.ID {
				return true
			}
		}
		return false
	}() {
		t.Fatal("the operator can see bob's private clone")
	}
	// unshare: bob loses it at once, by name and by id
	if err := r.ShareVoice(ctx, "", sharedID, false); err != nil {
		t.Fatal(err)
	}
	if find("bob") != nil {
		t.Fatal("bob still sees an unshared voice")
	}
	for _, who := range []string{"HouseVoice", sharedID} {
		if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: who}); !errors.Is(err, types.ErrVoiceNotFound) {
			t.Fatalf("unshared voice %q must not be usable: %v", who, err)
		}
	}
}
