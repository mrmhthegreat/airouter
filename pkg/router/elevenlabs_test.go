package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// fakeEL simulates one ElevenLabs account.
type fakeEL struct {
	mu        sync.Mutex
	srv       *httptest.Server
	voices    []map[string]any
	nextID    int
	hits      map[string]int // "METHOD path-kind" -> count
	lastTTS   map[string]any
	lastFmt   string
	lastVoice string
	apiKeys   map[string]bool
}

func newFakeEL(t *testing.T) *fakeEL {
	f := &fakeEL{hits: map[string]int{}, apiKeys: map[string]bool{}}
	f.voices = []map[string]any{
		{"voice_id": "premade0000000000001", "name": "Rachel", "category": "premade", "labels": map[string]string{"gender": "female", "accent": "american"}},
		{"voice_id": "premade0000000000002", "name": "Adam", "category": "premade", "labels": map[string]string{"gender": "male"}},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is a base URL that the router recognises as ElevenLabs.
func (f *fakeEL) URL() string { return f.srv.URL + "/elevenlabs.io" }

func (f *fakeEL) hit(k string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[k] }

func (f *fakeEL) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/elevenlabs.io")
	f.apiKeys[r.Header.Get("xi-api-key")] = true
	switch {
	case r.Method == "GET" && path == "/v2/voices":
		f.hits["list"]++
		_ = json.NewEncoder(w).Encode(map[string]any{"voices": f.voices, "has_more": false})
	case r.Method == "POST" && path == "/v1/voices/add":
		f.hits["clone"]++
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(r.MultipartForm.File["files"]) == 0 || r.FormValue("name") == "" {
			http.Error(w, `{"detail":"need name and files"}`, 422)
			return
		}
		f.nextID++
		id := fmt.Sprintf("clone%015d", f.nextID)
		f.voices = append(f.voices, map[string]any{"voice_id": id, "name": r.FormValue("name"), "category": "cloned"})
		_ = json.NewEncoder(w).Encode(map[string]any{"voice_id": id, "requires_verification": false})
	case r.Method == "DELETE" && strings.HasPrefix(path, "/v1/voices/"):
		f.hits["delete"]++
		id := strings.TrimPrefix(path, "/v1/voices/")
		for i, v := range f.voices {
			if v["voice_id"] == id {
				f.voices = append(f.voices[:i], f.voices[i+1:]...)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
				return
			}
		}
		http.Error(w, `{"detail":{"status":"voice_not_found"}}`, 404)
	case r.Method == "POST" && strings.HasPrefix(path, "/v1/text-to-speech/"):
		f.hits["tts"]++
		id := strings.TrimPrefix(path, "/v1/text-to-speech/")
		found := false
		for _, v := range f.voices {
			found = found || v["voice_id"] == id
		}
		if !found {
			http.Error(w, `{"detail":{"status":"voice_not_found","message":"A voice with the voice_id `+id+` was not found."}}`, 404)
			return
		}
		f.lastVoice, f.lastFmt = id, r.URL.Query().Get("output_format")
		f.lastTTS = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&f.lastTTS)
		switch {
		case strings.HasPrefix(f.lastFmt, "mp3"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 64)...))
		case strings.HasPrefix(f.lastFmt, "opus"):
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write(append([]byte("OggS"), make([]byte, 64)...))
		default: // pcm_*
			w.Header().Set("Content-Type", "audio/pcm")
			_, _ = w.Write(rawPCM(2400))
		}
	case r.Method == "POST" && path == "/v1/speech-to-text":
		f.hits["stt"]++
		_ = r.ParseMultipartForm(32 << 20)
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "hello from scribe (" + r.FormValue("model_id") + ")", "language_code": "en"})
	default:
		http.Error(w, "unexpected "+r.Method+" "+path, 404)
	}
}

func newELRouter(t *testing.T, pol TenantPolicy) *Router {
	r, err := New(Options{Store: store.NewMemoryStore(), MaxWait: 50 * time.Millisecond, TenantPolicy: pol})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func addEL(t *testing.T, r *Router, name string, f *fakeEL, key string, mod func(*types.ProviderConfig)) {
	add(t, r, name, f.URL(), 5, 0, func(p *types.ProviderConfig) {
		p.Category = types.CategoryTTS
		p.Model = "eleven_multilingual_v2"
		p.APIKey = key
		if mod != nil {
			mod(p)
		}
	})
}

func sampleFiles() []types.VoiceSample {
	return []types.VoiceSample{{Filename: "a.mp3", Data: []byte("fake-audio-1")}, {Filename: "b.mp3", Data: []byte("fake-audio-2")}}
}

func TestElevenLabsSpeakByNameSettingsAndFormats(t *testing.T) {
	f := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{})
	addEL(t, r, "el", f, "secret-key", nil)
	ctx := context.Background()
	stab, speed := 0.3, 1.1

	resp, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: "rachel", Model: "eleven_flash_v2_5",
		VoiceSettings: &types.VoiceSettings{Stability: &stab, Speed: &speed}, Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastVoice != "premade0000000000001" || resp.Voice != "Rachel" {
		t.Fatalf("name was not resolved: sent %q, resp voice %q", f.lastVoice, resp.Voice)
	}
	if !f.apiKeys["secret-key"] {
		t.Fatal("xi-api-key header missing")
	}
	if f.lastTTS["model_id"] != "eleven_flash_v2_5" || f.lastTTS["language_code"] != "en" {
		t.Fatalf("model/language not sent: %v", f.lastTTS)
	}
	vs, _ := f.lastTTS["voice_settings"].(map[string]any)
	if vs["stability"] != 0.3 || vs["speed"] != 1.1 || vs["style"] != nil {
		t.Fatalf("voice_settings wrong: %v", vs)
	}
	if !bytes.HasPrefix(resp.Audio, []byte("RIFF")) || resp.Format != "wav" || resp.Own {
		t.Fatalf("default should be a real WAV: %q fmt=%s", resp.Audio[:4], resp.Format)
	}
	for format, want := range map[string]string{"mp3": "mp3_44100_128", "opus": "opus_48000_64", "pcm": "pcm_24000"} {
		resp, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: "Adam", Format: format})
		if err != nil || resp.Format != format || f.lastFmt != want {
			t.Fatalf("%s: err=%v fmt=%s sent=%s", format, err, resp.Format, f.lastFmt)
		}
	}
	// partial name works only when unambiguous
	if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: "ada"}); err != nil {
		t.Fatalf("unique partial name: %v", err)
	}
	// no voice given: first premade voice
	if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi"}); err != nil || f.lastVoice != "premade0000000000001" {
		t.Fatalf("default voice: %v %s", err, f.lastVoice)
	}
}

// A wrong voice name is the caller's mistake: it must not pause the provider.
func TestElevenLabsUnknownVoiceDoesNotPauseProvider(t *testing.T) {
	f := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{})
	addEL(t, r, "el", f, "k", nil)
	ctx := context.Background()
	for _, v := range []string{"Nobody", "zzzzzzzzzzzzzzzzzzzz"} { // a name, and a well-formed but unknown id
		if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: v}); !errors.Is(err, types.ErrVoiceNotFound) {
			t.Fatalf("%q: want ErrVoiceNotFound, got %v", v, err)
		}
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: "Adam"}); err != nil {
		t.Fatalf("provider was punished for a bad voice: %v", err)
	}
}

func TestCloneVoiceFlow(t *testing.T) {
	f := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{})
	addEL(t, r, "el", f, "k", nil)
	ctx := context.Background()

	if _, err := r.CloneVoice(ctx, types.CloneVoiceRequest{Name: "Me", Samples: sampleFiles()}); !errors.Is(err, types.ErrConsentRequired) {
		t.Fatalf("no consent must be refused: %v", err)
	}
	if _, err := r.CloneVoice(ctx, types.CloneVoiceRequest{Name: "Me", ConsentConfirmed: true}); !errors.Is(err, types.ErrBadRequest) {
		t.Fatalf("no samples must be refused: %v", err)
	}
	v, err := r.CloneVoice(ctx, types.CloneVoiceRequest{Name: "Me", Samples: sampleFiles(), ConsentConfirmed: true, Labels: map[string]string{"gender": "male"}})
	if err != nil || !v.Cloned || v.Category != "cloned" {
		t.Fatalf("clone: %v %+v", err, v)
	}
	// speak with the clone by name (any case) and by id
	for _, who := range []string{"me", v.ID} {
		if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: who}); err != nil || f.lastVoice != v.ID {
			t.Fatalf("speak %q: %v (sent %s)", who, err, f.lastVoice)
		}
	}
	list, err := r.ListVoices(ctx, "", ListVoicesOptions{})
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	var cloned int
	for _, lv := range list {
		if lv.Cloned {
			cloned++
		}
	}
	if cloned != 1 {
		t.Fatalf("exactly one voice should be marked cloned, got %d", cloned)
	}
	if got, _ := r.ListVoices(ctx, "", ListVoicesOptions{Search: "male"}); len(got) == 0 {
		t.Fatal("search by label failed")
	}
	if err := r.DeleteVoice(ctx, "", v.ID); err != nil {
		t.Fatal(err)
	}
	if f.hit("delete") != 1 {
		t.Fatal("voice was not deleted at ElevenLabs")
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{Text: "hi", Voice: "Me"}); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("deleted voice must be gone: %v", err)
	}
}

// One tenant must never see, use, or delete another tenant's cloned voice, even
// when both clone inside the same shared ElevenLabs account.
func TestVoiceIsolationBetweenTenants(t *testing.T) {
	shared := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{AllowSharedVoiceClone: true, AllowCustomBaseURL: true})
	addEL(t, r, "el-shared", shared, "shared-key", nil)
	ctx := context.Background()
	for _, id := range []string{"alice", "bob"} {
		if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	av, err := r.CloneVoice(ctx, types.CloneVoiceRequest{TenantID: "alice", Name: "AliceVoice", Samples: sampleFiles(), ConsentConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	// bob: cannot list, speak (by id or by name), or delete it
	bl, err := r.ListVoices(ctx, "bob", ListVoicesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range bl {
		if v.ID == av.ID || v.Name == "AliceVoice" {
			t.Fatal("bob can see alice's voice")
		}
	}
	for _, who := range []string{av.ID, "AliceVoice", "alicevoice"} {
		if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: who}); !errors.Is(err, types.ErrVoiceNotFound) {
			t.Fatalf("bob speaking with %q: want ErrVoiceNotFound, got %v", who, err)
		}
	}
	if err := r.DeleteVoice(ctx, "bob", av.ID); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("bob deleting alice's voice: %v", err)
	}
	if shared.hit("delete") != 0 {
		t.Fatal("alice's voice was deleted at ElevenLabs by bob")
	}
	// alice still can
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "alice", Text: "hi", Voice: "AliceVoice"}); err != nil {
		t.Fatal(err)
	}
	if al, _ := r.ListVoices(ctx, "alice", ListVoicesOptions{}); len(al) != 3 {
		t.Fatalf("alice should see 2 premade + her own, got %d", len(al))
	}
	// both see the ready-made voices
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: "Rachel"}); err != nil {
		t.Fatal(err)
	}
}

// Without its own ElevenLabs key a tenant cannot burn the operator's voice slots;
// with its own key it clones in its OWN account and the shared account is untouched.
func TestTenantClonesOnlyWithOwnKey(t *testing.T) {
	shared, mine := newFakeEL(t), newFakeEL(t)
	r := newELRouter(t, TenantPolicy{AllowCustomBaseURL: true}) // AllowSharedVoiceClone stays false
	addEL(t, r, "el-shared", shared, "shared-key", nil)
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "carol"}); err != nil {
		t.Fatal(err)
	}
	req := types.CloneVoiceRequest{TenantID: "carol", Name: "Carol", Samples: sampleFiles(), ConsentConfirmed: true}
	if _, err := r.CloneVoice(ctx, req); !errors.Is(err, types.ErrVoiceCloneNotAllowed) {
		t.Fatalf("want ErrVoiceCloneNotAllowed, got %v", err)
	}
	if shared.hit("clone") != 0 {
		t.Fatal("clone reached the shared account")
	}
	if _, err := r.AddTenantProvider(ctx, "carol", types.ProviderConfig{Name: "carol-el", Kind: "openai", BaseURL: mine.URL(), APIKey: "carol-key",
		Model: "eleven_multilingual_v2", Category: types.CategoryTTS, Priority: 1}); err != nil {
		t.Fatal(err)
	}
	v, err := r.CloneVoice(ctx, req)
	if err != nil || !v.Own {
		t.Fatalf("clone with own key: %v %+v", err, v)
	}
	if mine.hit("clone") != 1 || shared.hit("clone") != 0 {
		t.Fatal("clone must go to carol's own account only")
	}
	// her clone only exists in her account, so failover must not send it to the shared one
	resp, err := r.Speak(ctx, types.SpeakRequest{TenantID: "carol", Text: "hi", Voice: "Carol"})
	if err != nil || !resp.Own {
		t.Fatalf("speak with clone: %v %+v", err, resp)
	}
	if shared.hit("tts") != 0 {
		t.Fatal("a cloned voice was sent to a different ElevenLabs account")
	}
	// per-tenant clone limit
	r2 := newELRouter(t, TenantPolicy{MaxClonedVoices: 1})
	addEL(t, r2, "el", shared, "k", nil)
	if _, err := r2.CloneVoice(ctx, types.CloneVoiceRequest{Name: "One", Samples: sampleFiles(), ConsentConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.CloneVoice(ctx, types.CloneVoiceRequest{Name: "Two", Samples: sampleFiles(), ConsentConfirmed: true}); !errors.Is(err, types.ErrBadRequest) {
		t.Fatalf("clone limit not enforced: %v", err)
	}
}

func TestElevenLabsTranscribe(t *testing.T) {
	f := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{})
	add(t, r, "el-stt", f.URL(), 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategorySTT; p.Model = "scribe_v1" })
	resp, err := r.Transcribe(context.Background(), types.TranscribeRequest{Audio: []byte("abc"), Filename: "a.mp3", Language: "en"})
	if err != nil || !strings.Contains(resp.Text, "scribe_v1") || resp.Language != "en" {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestListVoicesWithoutElevenLabsExplainsHowToAdd(t *testing.T) {
	r := newELRouter(t, TenantPolicy{})
	if _, err := r.ListVoices(context.Background(), "", ListVoicesOptions{}); err == nil || !strings.Contains(err.Error(), "provider add -preset elevenlabs") {
		t.Fatalf("got %v", err)
	}
}

var _ = io.EOF

// A voice the operator (default tenant) clones is shared: every tenant can list
// and speak it, but cannot delete it. A tenant's own clone stays private.
func TestOperatorVoiceSharingIsOptIn(t *testing.T) {
	f := newFakeEL(t)
	r := newELRouter(t, TenantPolicy{AllowCustomBaseURL: true})
	addEL(t, r, "el-shared", f, "shared-key", nil)
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "bob"}); err != nil {
		t.Fatal(err)
	}
	sv, err := r.CloneVoice(ctx, types.CloneVoiceRequest{TenantID: types.DefaultTenantID, Name: "HouseVoice", Samples: sampleFiles(), ConsentConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	sees := func() bool {
		list, err := r.ListVoices(ctx, "bob", ListVoicesOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range list {
			if v.ID == sv.ID {
				return true
			}
		}
		return false
	}
	// Private by default: bob neither sees nor speaks it.
	if sees() {
		t.Fatal("a new operator voice must be private")
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: sv.ID}); err == nil {
		t.Fatal("bob spoke a private operator voice")
	}
	if err := r.ShareVoice(ctx, "bob", sv.ID, true); err == nil {
		t.Fatal("a tenant must not share the operator's voice")
	}
	if err := r.ShareVoice(ctx, types.DefaultTenantID, sv.ID, true); err != nil {
		t.Fatal(err)
	}
	if !sees() {
		t.Fatal("bob should see the shared operator voice")
	}
	for _, who := range []string{sv.ID, "HouseVoice"} {
		if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: who}); err != nil {
			t.Fatalf("bob speaking with %q: %v", who, err)
		}
	}
	if err := r.DeleteVoice(ctx, "bob", sv.ID); !errors.Is(err, types.ErrVoiceNotFound) {
		t.Fatalf("bob must not delete the shared voice: %v", err)
	}
	if f.hit("delete") != 0 {
		t.Fatal("shared voice was deleted at ElevenLabs by a tenant")
	}
	// Unshare: hidden again.
	if err := r.ShareVoice(ctx, types.DefaultTenantID, sv.ID, false); err != nil {
		t.Fatal(err)
	}
	if sees() {
		t.Fatal("unshared voice is still visible")
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "bob", Text: "hi", Voice: sv.ID}); err == nil {
		t.Fatal("bob spoke an unshared voice")
	}
}
