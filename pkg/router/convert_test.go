package router

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// sineWAV makes a short real WAV file with ffmpeg; the test is skipped when
// ffmpeg is not installed.
func sineWAV(t *testing.T) []byte {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-f", "wav", "pipe:1").Output()
	if err != nil || len(out) < 100 {
		t.Skipf("ffmpeg cannot make test audio: %v", err)
	}
	return out
}

func TestConvertWAVToMP3AndOpus(t *testing.T) {
	wav := sineWAV(t)
	mp3, ct, err := convertAudio(context.Background(), wav, "mp3")
	if err != nil {
		t.Skipf("no mp3 encoder in this ffmpeg: %v", err)
	}
	if ct != "audio/mpeg" || !(bytes.HasPrefix(mp3, []byte("ID3")) || (len(mp3) > 2 && mp3[0] == 0xFF && mp3[1]&0xE0 == 0xE0)) {
		t.Fatalf("not an mp3: ct=%s head=% x", ct, mp3[:4])
	}
	if ogg, ct, err := convertAudio(context.Background(), wav, "opus"); err == nil {
		if ct != "audio/ogg" || !bytes.HasPrefix(ogg, []byte("OggS")) {
			t.Fatalf("not ogg/opus: ct=%s head=% x", ct, ogg[:4])
		}
	}
	if _, _, err := convertAudio(context.Background(), wav, "flac"); err == nil {
		t.Fatal("unsupported target must be an error")
	}
}

func TestMaybeConvertKeepsOriginalOnFailure(t *testing.T) {
	old := ffmpegPath
	defer func() { ffmpegPath = old }()
	ffmpegPath = func() (string, error) { return "", exec.ErrNotFound }
	audio, ct, format := maybeConvert(context.Background(), []byte("WAVDATA"), "audio/wav", "wav", "mp3")
	if string(audio) != "WAVDATA" || ct != "audio/wav" || format != "wav" {
		t.Fatalf("must keep the original: %q %s %s", audio, ct, format)
	}
	// Same format asked: nothing to do, converter never needed.
	if a, _, f := maybeConvert(context.Background(), []byte("X"), "audio/mpeg", "mp3", "mp3"); string(a) != "X" || f != "mp3" {
		t.Fatal("same format must pass through")
	}
}

// End to end: the provider returns WAV, the caller asked for mp3.
func TestSpeakConvertsWAVToMP3(t *testing.T) {
	wav := sineWAV(t)
	if _, _, err := convertAudio(context.Background(), wav, "mp3"); err != nil {
		t.Skipf("no mp3 encoder: %v", err)
	}
	srv := fakeTTSProviderGemini(t, wav)
	r := newRouter(t, nil)
	p := &types.ProviderConfig{Name: "gemini-tts", Kind: "openai", BaseURL: srv.URL + "/generativelanguage.googleapis.com",
		APIKey: "k", Model: "m", Category: types.CategoryTTS, Priority: 10, Workers: 1, RPD: 100}
	if err := r.SaveProvider(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := r.Speak(ctx, types.SpeakRequest{Text: "hello", Voice: "Kore", Format: "mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Format != "mp3" || resp.ContentType != "audio/mpeg" || bytes.HasPrefix(resp.Audio, []byte("RIFF")) {
		t.Fatalf("not converted: format=%s ct=%s", resp.Format, resp.ContentType)
	}
	// No format asked: stays WAV.
	resp, err = r.Speak(ctx, types.SpeakRequest{Text: "hello", Voice: "Kore"})
	if err != nil || resp.Format != "wav" {
		t.Fatalf("default must stay wav: %+v %v", resp, err)
	}
}
