package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

func haveFFmpeg() bool { _, err := exec.LookPath("ffmpeg"); return err == nil }

// rawPCM makes n samples of silence-ish 16-bit mono PCM (no header).
func rawPCM(n int) []byte {
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(int16((i%100)*200-10000)))
	}
	return b
}

// Groq's TTS only returns WAV. When the caller asks for mp3/opus the router
// must ask Groq for WAV (never "mp3") and convert afterwards.
func TestGroqIsAskedForWAVNotMP3(t *testing.T) {
	var gotFormat atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotFormat.Store(body["response_format"])
		if body["response_format"] != "wav" { // what the real Groq does
			http.Error(w, `{"error":"response_format must be wav"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(pcmToWAV(rawPCM(24000), 24000, 1, 16))
	}))
	t.Cleanup(srv.Close)
	r := newRouter(t, nil)
	add(t, r, "groq-tts", srv.URL+"/api.groq.com/openai/v1", 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategoryTTS })

	for _, want := range []string{"mp3", "opus"} {
		resp, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hello", Format: want})
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if gotFormat.Load() != "wav" {
			t.Fatalf("Groq was sent response_format=%v, want wav", gotFormat.Load())
		}
		if haveFFmpeg() && resp.Format != want {
			t.Fatalf("want converted %s, got %s (%s)", want, resp.Format, resp.ContentType)
		}
		if !haveFFmpeg() && resp.Format != "wav" {
			t.Fatalf("without ffmpeg the router must fall back to wav, got %s", resp.Format)
		}
	}
}

// Real Gemini TTS returns headerless PCM with mime audio/L16;rate=24000.
// The router must hand back a playable WAV.
func TestGeminiRawPCMBecomesWAV(t *testing.T) {
	pcm := rawPCM(24000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"candidates": []map[string]any{{"content": map[string]any{"parts": []map[string]any{
			{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(pcm)}}}}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	r := newRouter(t, nil)
	add(t, r, "gem", srv.URL+"/generativelanguage.googleapis.com", 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategoryTTS })
	resp, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(resp.Audio, []byte("RIFF")) || resp.ContentType != "audio/wav" || resp.Format != "wav" {
		t.Fatalf("not a wav: %q ct=%s", resp.Audio[:4], resp.ContentType)
	}
	if rate := binary.LittleEndian.Uint32(resp.Audio[24:28]); rate != 24000 {
		t.Fatalf("sample rate in header = %d", rate)
	}
	if !bytes.Equal(resp.Audio[44:], pcm) {
		t.Fatal("pcm payload changed")
	}
	if haveFFmpeg() {
		resp, err = r.Speak(context.Background(), types.SpeakRequest{Text: "hi", Format: "opus"})
		if err != nil || resp.Format != "opus" || !bytes.HasPrefix(resp.Audio, []byte("OggS")) {
			t.Fatalf("opus from raw pcm failed: %v %+v", err, resp)
		}
	}
}

func TestEnsureWAVLeavesRealFilesAlone(t *testing.T) {
	w := pcmToWAV(rawPCM(10), 16000, 1, 16)
	if out, ct := ensureWAV(w, "audio/L16;rate=16000"); !bytes.Equal(out, w) || ct != "audio/L16;rate=16000" {
		t.Fatal("a file that already has a header must not be wrapped again")
	}
	if out, _ := ensureWAV([]byte("not audio"), "text/plain"); string(out) != "not audio" {
		t.Fatal("non-pcm must pass through")
	}
}

// Pool-only cap must behave the same for voice and transcription, not only text.
func TestPoolOnlyCapAppliesToSpeakAndTranscribe(t *testing.T) {
	pool, _ := fakeTTSProviderOpenAI(t, pcmToWAV(rawPCM(100), 24000, 1, 16))
	own, _ := fakeTTSProviderOpenAI(t, pcmToWAV(rawPCM(100), 24000, 1, 16))
	r := newCapRouter(t, 50*time.Millisecond)
	add(t, r, "pool-tts", pool.URL, 5, 0, func(p *types.ProviderConfig) { p.Category = types.CategoryTTS })
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 1, PoolOnlyCap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddTenantProvider(ctx, "acme", types.ProviderConfig{Name: "mine-tts", Kind: "openai", BaseURL: own.URL, APIKey: "k", Model: "m", Category: types.CategoryTTS, Priority: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // own key: never counted
		resp, err := r.Speak(ctx, types.SpeakRequest{TenantID: "acme", Text: "x"})
		if err != nil || !resp.Own {
			t.Fatalf("own tts %d: %v %+v", i, err, resp)
		}
	}
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "acme", Text: "x", Provider: "pool-tts"}); err != nil {
		t.Fatalf("first pool call: %v", err)
	}
	var tl *types.TenantLimitError
	if _, err := r.Speak(ctx, types.SpeakRequest{TenantID: "acme", Text: "x", Provider: "pool-tts"}); !errors.As(err, &tl) {
		t.Fatalf("second pool call must hit the cap, got %v", err)
	}
	if resp, err := r.Speak(ctx, types.SpeakRequest{TenantID: "acme", Text: "x"}); err != nil || !resp.Own {
		t.Fatalf("capped tenant must still use its own key: %v %+v", err, resp)
	}
	// Transcribe: capped tenant without an own STT key is stopped.
	stt, _ := fakeSTTProvider(t, "hello")
	add(t, r, "pool-stt", stt.URL, 1, 0, func(p *types.ProviderConfig) { p.Category = types.CategorySTT })
	if _, err := r.Transcribe(ctx, types.TranscribeRequest{TenantID: "acme", Audio: []byte("x"), Filename: "a.wav"}); !errors.As(err, &tl) {
		t.Fatalf("capped tenant without own STT key must get the cap error, got %v", err)
	}
}

// ffmpeg must never run more than convertSlots at once.
func TestConvertConcurrencyIsBounded(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("ffmpeg not installed")
	}
	if cap(convertSlots) != 4 {
		t.Fatalf("slots = %d", cap(convertSlots))
	}
	wav := pcmToWAV(rawPCM(8000), 16000, 1, 16)
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { _, _, err := convertAudio(context.Background(), wav, "mp3"); done <- err }()
	}
	for i := 0; i < 12; i++ {
		if err := <-done; err != nil && !strings.Contains(err.Error(), "encoder") {
			t.Fatal(err)
		}
	}
}
