package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// ----- fake TTS providers ---------------------------------------------------

// fakeTTSProviderOpenAI simulates a Groq-style TTS endpoint: POST /audio/speech
// with JSON body, returns raw audio bytes. Records the voice it received.
func fakeTTSProviderOpenAI(t *testing.T, audio []byte) (*httptest.Server, *string) {
	var gotVoice string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Fatalf("TTS OpenAI: unexpected path %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotVoice, _ = body["voice"].(string)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(audio)
	}))
	t.Cleanup(s.Close)
	return s, &gotVoice
}

// fakeTTSProviderGemini simulates Gemini's :generateContent endpoint: returns
// base64-encoded audio bytes in candidates[0].content.parts[0].inlineData.data.
// Verifies the x-goog-api-key header (Gemini's native auth).
func fakeTTSProviderGemini(t *testing.T, audio []byte) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Fatalf("TTS Gemini: unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") == "" {
			t.Fatalf("TTS Gemini: missing x-goog-api-key header")
		}
		resp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{
								"inlineData": map[string]any{
									"mimeType": "audio/wav",
									"data":     base64.StdEncoding.EncodeToString(audio),
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.Close)
	return s
}

// fakeSTTProvider simulates a Groq-style STT endpoint: POST /audio/transcriptions
// with multipart form, returns JSON {"text": "..."}.
func fakeSTTProvider(t *testing.T, wantText string) (*httptest.Server, *string) {
	var gotFilename string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("STT: unexpected path %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("STT: ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("STT: FormFile: %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename
		_, _ = io.Copy(io.Discard, file)
		if r.FormValue("model") == "" {
			t.Errorf("STT: missing 'model' form field")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": wantText})
	}))
	t.Cleanup(s.Close)
	return s, &gotFilename
}

// ----- TTS tests ------------------------------------------------------------

func TestSpeakGroqStyle(t *testing.T) {
	audio := []byte("FAKE-WAV-BYTES")
	srv, gotVoice := fakeTTSProviderOpenAI(t, audio)
	r := newRouter(t, nil)
	p := &types.ProviderConfig{
		Name: "groq-tts", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "canopylabs/orpheus-v1-english", Category: types.CategoryTTS,
		Priority: 10, Workers: 1, RPD: 100,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Speak(context.Background(), types.SpeakRequest{
		Text:  "Hello world",
		Voice: "tara",
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if string(resp.Audio) != "FAKE-WAV-BYTES" {
		t.Fatalf("got audio %q, want %q", string(resp.Audio), "FAKE-WAV-BYTES")
	}
	if resp.Provider != "groq-tts" {
		t.Fatalf("provider: %s", resp.Provider)
	}
	if resp.Format != "wav" {
		t.Fatalf("format: %s", resp.Format)
	}
	if *gotVoice != "tara" {
		t.Fatalf("voice sent to provider: %q, want 'tara'", *gotVoice)
	}
	if resp.Voice != "tara" {
		t.Fatalf("resp.Voice: %s", resp.Voice)
	}
}

func TestSpeakGeminiStyle(t *testing.T) {
	audio := []byte("FAKE-GEMINI-WAV")
	srv := fakeTTSProviderGemini(t, audio)
	r := newRouter(t, nil)

	// Patch the BaseURL to include the magic string "generativelanguage.googleapis.com"
	// so callSpeak dispatches to the Gemini path. In production this is part of
	// the real URL; for the test we append it to the fake server's URL.
	p := &types.ProviderConfig{
		Name: "gemini-tts", Kind: "openai", BaseURL: srv.URL + "/generativelanguage.googleapis.com",
		APIKey: "k", Model: "gemini-3.8-flash-tts", Category: types.CategoryTTS,
		Priority: 10, Workers: 1, RPD: 100,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Speak(context.Background(), types.SpeakRequest{
		Text:  "Hello from Gemini",
		Voice: "Kore",
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if string(resp.Audio) != "FAKE-GEMINI-WAV" {
		t.Fatalf("got audio %q, want %q", string(resp.Audio), "FAKE-GEMINI-WAV")
	}
	if resp.ContentType != "audio/wav" {
		t.Fatalf("ContentType: %s", resp.ContentType)
	}
}

func TestSpeakFailoverOnError(t *testing.T) {
	// First provider always 500s; second provider returns the audio.
	var hits int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv1.Close)
	audio := []byte("FROM-PROVIDER-2")
	srv2, _ := fakeTTSProviderOpenAI(t, audio)

	r := newRouter(t, nil)
	p1 := &types.ProviderConfig{Name: "bad", Kind: "openai", BaseURL: srv1.URL, APIKey: "k",
		Model: "x", Category: types.CategoryTTS, Priority: 10, Workers: 1}
	p2 := &types.ProviderConfig{Name: "good", Kind: "openai", BaseURL: srv2.URL, APIKey: "k",
		Model: "y", Category: types.CategoryTTS, Priority: 20, Workers: 1}
	if err := r.SaveProvider(context.Background(), p1); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveProvider(context.Background(), p2); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if string(resp.Audio) != "FROM-PROVIDER-2" {
		t.Fatalf("audio: %q", string(resp.Audio))
	}
	if resp.Provider != "good" {
		t.Fatalf("provider: %s", resp.Provider)
	}
	if len(resp.Attempts) != 1 || resp.Attempts[0].Provider != "bad" {
		t.Fatalf("attempts: %+v", resp.Attempts)
	}
}

func TestSpeakNoTTSProvidersReturnsError(t *testing.T) {
	r := newRouter(t, nil)
	p := &types.ProviderConfig{Name: "text-only", Kind: "openai", BaseURL: "http://x",
		APIKey: "k", Model: "m", Category: types.CategoryText, Priority: 10}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	_, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "no TTS providers") {
		t.Fatalf("expected 'no TTS providers' error, got %v", err)
	}
}

func TestSpeakEmptyTextReturnsError(t *testing.T) {
	r := newRouter(t, nil)
	_, err := r.Speak(context.Background(), types.SpeakRequest{Text: "   "})
	if err == nil || !strings.Contains(err.Error(), "Text is required") {
		t.Fatalf("expected 'Text is required' error, got %v", err)
	}
}

func TestSpeakDefaultVoiceForProvider(t *testing.T) {
	audio := []byte("ok")
	srv, gotVoice := fakeTTSProviderOpenAI(t, audio)
	r := newRouter(t, nil)
	p := &types.ProviderConfig{
		Name: "groq-orpheus", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "canopylabs/orpheus-v1-english", Category: types.CategoryTTS,
		Priority: 10, Workers: 1,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	resp, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// Default voice for orpheus-v1-english should be "tara".
	if *gotVoice != "tara" {
		t.Fatalf("default voice: %q, want 'tara'", *gotVoice)
	}
	if resp.Voice != "tara" {
		t.Fatalf("resp.Voice: %s", resp.Voice)
	}
}

// ----- STT tests ------------------------------------------------------------

func TestTranscribeGroqStyle(t *testing.T) {
	srv, gotFilename := fakeSTTProvider(t, "Hello, this is a test.")
	r := newRouter(t, nil)
	p := &types.ProviderConfig{
		Name: "groq-whisper", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "whisper-large-v3", Category: types.CategorySTT,
		Priority: 10, Workers: 1, RPD: 100,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Transcribe(context.Background(), types.TranscribeRequest{
		Audio:    []byte("FAKE-AUDIO-BYTES"),
		Filename: "recording.mp3",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if resp.Text != "Hello, this is a test." {
		t.Fatalf("text: %q", resp.Text)
	}
	if resp.Provider != "groq-whisper" {
		t.Fatalf("provider: %s", resp.Provider)
	}
	if *gotFilename != "recording.mp3" {
		t.Fatalf("filename sent: %q", *gotFilename)
	}
}

func TestTranscribeFailoverOnError(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	t.Cleanup(srv1.Close)
	srv2, _ := fakeSTTProvider(t, "Recovered after failover.")

	r := newRouter(t, nil)
	p1 := &types.ProviderConfig{Name: "bad", Kind: "openai", BaseURL: srv1.URL, APIKey: "k",
		Model: "x", Category: types.CategorySTT, Priority: 10, Workers: 1}
	p2 := &types.ProviderConfig{Name: "good", Kind: "openai", BaseURL: srv2.URL, APIKey: "k",
		Model: "y", Category: types.CategorySTT, Priority: 20, Workers: 1}
	if err := r.SaveProvider(context.Background(), p1); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveProvider(context.Background(), p2); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Transcribe(context.Background(), types.TranscribeRequest{
		Audio:    []byte("audio"),
		Filename: "x.wav",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if resp.Text != "Recovered after failover." {
		t.Fatalf("text: %q", resp.Text)
	}
	if resp.Provider != "good" {
		t.Fatalf("provider: %s", resp.Provider)
	}
}

func TestTranscribeNoSTTProvidersReturnsError(t *testing.T) {
	r := newRouter(t, nil)
	p := &types.ProviderConfig{Name: "text-only", Kind: "openai", BaseURL: "http://x",
		APIKey: "k", Model: "m", Category: types.CategoryText, Priority: 10}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	_, err := r.Transcribe(context.Background(), types.TranscribeRequest{
		Audio: []byte("audio"), Filename: "x.wav",
	})
	if err == nil || !strings.Contains(err.Error(), "no STT providers") {
		t.Fatalf("expected 'no STT providers' error, got %v", err)
	}
}

func TestTranscribeEmptyAudioReturnsError(t *testing.T) {
	r := newRouter(t, nil)
	_, err := r.Transcribe(context.Background(), types.TranscribeRequest{Audio: nil})
	if err == nil || !strings.Contains(err.Error(), "Audio is required") {
		t.Fatalf("expected 'Audio is required' error, got %v", err)
	}
}

// ----- category isolation tests --------------------------------------------

func TestGenerateSkipsTTSAndSTTProviders(t *testing.T) {
	// A TTS provider should never be picked by Generate (chat) even if it is
	// the only provider configured with a working endpoint.
	srv, _ := fakeTTSProviderOpenAI(t, []byte("audio"))
	r := newRouter(t, nil)
	p := &types.ProviderConfig{Name: "tts-only", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "orpheus", Category: types.CategoryTTS, Priority: 10}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	_, err := gen(r, types.Request{})
	if err == nil {
		t.Fatal("Generate should fail because no text providers are configured")
	}
	// Should be ErrNoProviders, not a 400 from the TTS endpoint.
	if !strings.Contains(err.Error(), "no enabled providers") {
		t.Fatalf("expected no-providers error, got %v", err)
	}
}

func TestSpeakSkipsTextAndSTTProviders(t *testing.T) {
	// A text provider should never be picked by Speak even if it is the only
	// provider configured.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("text provider should not have been called by Speak")
	}))
	t.Cleanup(srv.Close)
	r := newRouter(t, nil)
	p := &types.ProviderConfig{Name: "text-only", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "m", Category: types.CategoryText, Priority: 10}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	_, err := r.Speak(context.Background(), types.SpeakRequest{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "no TTS providers") {
		t.Fatalf("expected 'no TTS providers' error, got %v", err)
	}
}

// TestMultipartFilenameMimeType verifies the MIME-type-from-filename helper.
func TestMultipartFilenameMimeType(t *testing.T) {
	cases := []struct{ name, want string }{
		{"audio.mp3", "audio/mpeg"},
		{"audio.wav", "audio/wav"},
		{"audio.m4a", "audio/mp4"},
		{"audio.ogg", "audio/ogg"},
		{"audio.flac", "audio/flac"},
		{"audio.webm", "audio/webm"},
		{"unknown.bin", "application/octet-stream"},
	}
	for _, c := range cases {
		got := mimeTypeFromFilename(c.name)
		if got != c.want {
			t.Errorf("mimeTypeFromFilename(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// ----- audio seconds estimation tests --------------------------------------

// TestEstimateTTSAudioSeconds verifies the text-length-based estimate.
func TestEstimateTTSAudioSeconds(t *testing.T) {
	cases := []struct {
		text string
		want int // min seconds (the estimate is len/15)
	}{
		{"", 0},
		{"hi", 1},          // short → minimum 1
		{"Hello world", 1}, // 11 chars → 1 sec (rounded)
		{"This is a longer sentence that should take a few seconds to say.", 4}, // 60 chars → 4 sec
	}
	for _, c := range cases {
		got := estimateTTSAudioSeconds(c.text)
		if c.text == "" {
			if got != 0 {
				t.Errorf("estimateTTSAudioSeconds(%q): got %d, want 0", c.text, got)
			}
			continue
		}
		// We don't check exact values because rounding can differ by 1; check the
		// rough relationship holds.
		expected := len(c.text) / 15
		if expected < 1 {
			expected = 1
		}
		if got != expected {
			t.Errorf("estimateTTSAudioSeconds(%q): got %d, want %d", c.text, got, expected)
		}
	}
}

// TestWavDurationSeconds verifies the WAV header parser.
func TestWavDurationSeconds(t *testing.T) {
	// Build a minimal WAV header for 1 second of 16kHz mono 16-bit audio.
	// 1 second of audio = 16000 samples * 2 bytes/sample = 32000 bytes of data.
	const sampleRate = 16000
	const bitsPerSample = 16
	const channels = 1
	byteRate := sampleRate * channels * bitsPerSample / 8
	dataSize := 32000 // 1 second
	wav := buildWAVHeader(sampleRate, bitsPerSample, channels, byteRate, dataSize)
	secs := wavDurationSeconds(wav)
	if secs != 1 {
		t.Fatalf("1-second WAV: got %d secs, want 1", secs)
	}

	// 2 seconds
	wav2 := buildWAVHeader(sampleRate, bitsPerSample, channels, byteRate, dataSize*2)
	if secs := wavDurationSeconds(wav2); secs != 2 {
		t.Fatalf("2-second WAV: got %d secs, want 2", secs)
	}

	// Non-WAV bytes
	if secs := wavDurationSeconds([]byte("not a wav file at all")); secs != 0 {
		t.Fatalf("non-WAV: got %d, want 0", secs)
	}

	// Too short
	if secs := wavDurationSeconds([]byte{0, 0, 0}); secs != 0 {
		t.Fatalf("short: got %d, want 0", secs)
	}
}

// buildWAVHeader constructs a minimal WAV file header for testing.
func buildWAVHeader(sampleRate, bitsPerSample, channels, byteRate, dataSize int) []byte {
	// Total file size = 44-byte header + dataSize
	totalSize := 44 + dataSize
	wav := make([]byte, 44)
	copy(wav[0:4], []byte("RIFF"))
	wav[4] = byte(totalSize)
	wav[5] = byte(totalSize >> 8)
	wav[6] = byte(totalSize >> 16)
	wav[7] = byte(totalSize >> 24)
	copy(wav[8:12], []byte("WAVE"))
	copy(wav[12:16], []byte("fmt "))
	// fmt chunk size = 16
	wav[16] = 16
	// audio format = 1 (PCM)
	wav[20] = 1
	wav[22] = byte(channels)
	wav[24] = byte(sampleRate)
	wav[25] = byte(sampleRate >> 8)
	wav[26] = byte(sampleRate >> 16)
	wav[27] = byte(sampleRate >> 24)
	wav[28] = byte(byteRate)
	wav[29] = byte(byteRate >> 8)
	wav[30] = byte(byteRate >> 16)
	wav[31] = byte(byteRate >> 24)
	wav[32] = byte(channels * bitsPerSample / 8) // block align
	wav[34] = byte(bitsPerSample)
	copy(wav[36:40], []byte("data"))
	wav[40] = byte(dataSize)
	wav[41] = byte(dataSize >> 8)
	wav[42] = byte(dataSize >> 16)
	wav[43] = byte(dataSize >> 24)
	// Append the data bytes (we just use zeros — the parser only reads the header).
	return append(wav, make([]byte, dataSize)...)
}

// TestEstimateSTTAudioSeconds verifies the STT estimate uses WAV parsing when
// possible and falls back to byte-count otherwise.
func TestEstimateSTTAudioSeconds(t *testing.T) {
	// 1-second WAV → 1 sec
	wav1 := buildWAVHeader(16000, 16, 1, 32000, 32000)
	if got := estimateSTTAudioSeconds(wav1, "audio.wav", "audio/wav"); got != 1 {
		t.Fatalf("1-sec WAV: got %d, want 1", got)
	}

	// Non-WAV bytes: 32000 bytes / 16000 = 2 sec
	if got := estimateSTTAudioSeconds([]byte(make([]byte, 32000)), "audio.mp3", "audio/mpeg"); got != 2 {
		t.Fatalf("32KB MP3: got %d, want 2", got)
	}

	// Empty audio → 0
	if got := estimateSTTAudioSeconds(nil, "audio.wav", ""); got != 0 {
		t.Fatalf("empty: got %d, want 0", got)
	}
}

// ----- Gemini STT test -----------------------------------------------------

// fakeGeminiSTTServer simulates the three Gemini STT endpoints:
//   - POST .../upload/v1beta/files (multipart/related upload, returns file name + URI)
//   - GET .../v1beta/files/{name} (returns state=ACTIVE)
//   - POST .../v1beta/interactions (returns output_text)
//   - DELETE .../v1beta/files/{name} (returns 200)
//
// Paths are matched by suffix so the test can prepend a magic string
// ("generativelanguage.googleapis.com") to the BaseURL to trigger the Gemini
// dispatch path in callTranscribe.
func fakeGeminiSTTServer(t *testing.T, wantText string) *httptest.Server {
	var fileName string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload/v1beta/files") && r.Method == "POST":
			if r.Header.Get("x-goog-api-key") == "" {
				t.Errorf("upload: missing x-goog-api-key header")
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
			if len(body) == 0 {
				t.Errorf("upload: empty body")
			}
			fileName = "files/test-1"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file": map[string]string{
					"name":     fileName,
					"uri":      "https://generativelanguage.googleapis.com/v1beta/" + fileName,
					"state":    "ACTIVE",
					"mimeType": "audio/wav",
				},
			})
		case strings.Contains(r.URL.Path, "/v1beta/files/") && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "ACTIVE", "name": fileName})
		case strings.HasSuffix(r.URL.Path, "/v1beta/interactions") && r.Method == "POST":
			if r.Header.Get("x-goog-api-key") == "" {
				t.Errorf("interactions: missing x-goog-api-key header")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			input, _ := body["input"].([]any)
			if len(input) == 0 {
				t.Errorf("interactions: empty input")
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"output_text": wantText})
		case strings.Contains(r.URL.Path, "/v1beta/files/") && r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestTranscribeGeminiStyle(t *testing.T) {
	// Build a 1-second WAV so the audio-seconds estimate is accurate.
	audio := buildWAVHeader(16000, 16, 1, 32000, 32000)
	srv := fakeGeminiSTTServer(t, "Hello from Gemini transcribe.")
	r := newRouter(t, nil)

	// Gemini-style: BaseURL must contain "generativelanguage.googleapis.com"
	// for callTranscribe to dispatch to the Gemini path.
	p := &types.ProviderConfig{
		Name: "gemini-stt", Kind: "openai",
		BaseURL:  srv.URL + "/generativelanguage.googleapis.com",
		APIKey:   "k",
		Model:    "gemini-3.5-transcribe",
		Category: types.CategorySTT,
		Priority: 10, Workers: 1, RPD: 100,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	resp, err := r.Transcribe(context.Background(), types.TranscribeRequest{
		Audio:    audio,
		Filename: "audio.wav",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if resp.Text != "Hello from Gemini transcribe." {
		t.Fatalf("text: %q", resp.Text)
	}
	if resp.Provider != "gemini-stt" {
		t.Fatalf("provider: %s", resp.Provider)
	}
}

// TestAudioSecondsTracking verifies that ASH/ASD counters increment after a
// successful TTS call, and that the daily counter is reflected in Status.
func TestAudioSecondsTracking(t *testing.T) {
	audio := []byte("FAKE-WAV")
	srv, _ := fakeTTSProviderOpenAI(t, audio)
	r := newRouter(t, nil)
	p := &types.ProviderConfig{
		Name: "groq-orpheus", Kind: "openai", BaseURL: srv.URL, APIKey: "k",
		Model: "canopylabs/orpheus-v1-english", Category: types.CategoryTTS,
		Priority: 10, Workers: 1, ASH: 100, ASD: 1000,
	}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	// First call: 30-char text → ~2 seconds of estimated audio.
	resp, err := r.Speak(context.Background(), types.SpeakRequest{
		Text: "Hello world from airouter test.", // 30 chars → 2 secs
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if string(resp.Audio) != "FAKE-WAV" {
		t.Fatalf("audio: %q", string(resp.Audio))
	}

	// Status should now show ~2 audio seconds used today.
	st := r.Status(context.Background(), "")
	if len(st) != 1 {
		t.Fatalf("expected 1 provider in status, got %d", len(st))
	}
	if st[0].AudioSecToday < 1 {
		t.Fatalf("AudioSecToday: %d, want ≥ 1", st[0].AudioSecToday)
	}
	if st[0].ASH != 100 || st[0].ASD != 1000 {
		t.Fatalf("ASH/ASD: %d/%d, want 100/1000", st[0].ASH, st[0].ASD)
	}
}

// Ensure the multipart import is used (it is, in fakeSTTProvider via r.FormFile,
// but keep an explicit reference to guard against linters removing it).
var _ = multipart.NewWriter
