package router

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNoConverter is returned when audio conversion is wanted but ffmpeg is not
// installed. Speak then returns the provider's original format (and says so in
// SpeakResponse.Format) instead of failing the request.
var ErrNoConverter = errors.New("airouter: ffmpeg not found, cannot convert audio")

// ffmpegPath finds ffmpeg once per call (cheap); tests may replace it.
var ffmpegPath = func() (string, error) { return exec.LookPath("ffmpeg") }

// convertSlots limits how many ffmpeg processes run at once, so a burst of
// requests cannot use up all the CPU or memory of the machine.
var convertSlots = make(chan struct{}, 4)

// convertAudio converts audio bytes to "mp3" or "opus" (an Ogg/Opus file, the
// format WhatsApp voice notes use). Other targets are not supported. It needs
// ffmpeg on the PATH.
func convertAudio(ctx context.Context, in []byte, to string) (out []byte, contentType string, err error) {
	bin, err := ffmpegPath()
	if err != nil {
		return nil, "", ErrNoConverter
	}
	var args []string
	switch strings.ToLower(to) {
	case "mp3":
		args, contentType = []string{"-f", "mp3", "-codec:a", "libmp3lame", "-q:a", "4"}, "audio/mpeg"
	case "opus", "ogg":
		// Mono Ogg/Opus: what WhatsApp and Telegram voice notes expect.
		args, contentType = []string{"-f", "ogg", "-codec:a", "libopus", "-b:a", "32k", "-ac", "1", "-application", "voip"}, "audio/ogg"
	default:
		return nil, "", fmt.Errorf("airouter: cannot convert to %q (use mp3 or opus)", to)
	}
	select {
	case convertSlots <- struct{}{}:
		defer func() { <-convertSlots }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"-hide_banner", "-loglevel", "error", "-i", "pipe:0"}, append(args, "pipe:1")...)...)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("airouter: ffmpeg failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, "", errors.New("airouter: ffmpeg produced no audio")
	}
	return stdout.Bytes(), contentType, nil
}

// maybeConvert converts the provider's audio to the format the caller asked
// for when it differs. On any failure it keeps the original audio, so a missing
// converter never turns a good synthesis into an error.
func maybeConvert(ctx context.Context, audio []byte, contentType, have, want string) ([]byte, string, string) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || want == strings.ToLower(have) || (want == "ogg" && have == "opus") {
		return audio, contentType, have
	}
	if want != "mp3" && want != "opus" && want != "ogg" {
		return audio, contentType, have
	}
	out, ct, err := convertAudio(ctx, audio, want)
	if err != nil {
		return audio, contentType, have
	}
	if want == "ogg" {
		want = "opus"
	}
	return out, ct, want
}

// hasAudioHeader reports whether b starts with a known container signature.
func hasAudioHeader(b []byte) bool {
	switch {
	case bytes.HasPrefix(b, []byte("RIFF")), bytes.HasPrefix(b, []byte("OggS")),
		bytes.HasPrefix(b, []byte("ID3")), bytes.HasPrefix(b, []byte("fLaC")):
		return true
	case len(b) > 1 && b[0] == 0xFF && b[1]&0xE0 == 0xE0: // MPEG frame sync
		return true
	}
	return false
}

var rateRe = regexp.MustCompile(`rate=(\d+)`)

// ensureWAV turns raw 16-bit mono PCM into a WAV file. Gemini TTS answers with
// raw PCM (mime "audio/L16;codec=pcm;rate=24000") and no header, which players
// and ffmpeg cannot open. Anything that already has a header is returned as is.
func ensureWAV(b []byte, contentType string) ([]byte, string) {
	ct := strings.ToLower(contentType)
	if hasAudioHeader(b) || !(strings.Contains(ct, "l16") || strings.Contains(ct, "pcm")) {
		return b, contentType
	}
	rate := 24000
	if m := rateRe.FindStringSubmatch(ct); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 8000 && n <= 96000 {
			rate = n
		}
	}
	return pcmToWAV(b, rate, 1, 16), "audio/wav"
}

// pcmToWAV prepends a 44-byte RIFF/WAVE header to little-endian PCM samples.
func pcmToWAV(pcm []byte, rate, channels, bits int) []byte {
	var h bytes.Buffer
	h.WriteString("RIFF")
	binary.Write(&h, binary.LittleEndian, uint32(36+len(pcm)))
	h.WriteString("WAVEfmt ")
	binary.Write(&h, binary.LittleEndian, uint32(16))
	binary.Write(&h, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&h, binary.LittleEndian, uint16(channels))
	binary.Write(&h, binary.LittleEndian, uint32(rate))
	binary.Write(&h, binary.LittleEndian, uint32(rate*channels*bits/8))
	binary.Write(&h, binary.LittleEndian, uint16(channels*bits/8))
	binary.Write(&h, binary.LittleEndian, uint16(bits))
	h.WriteString("data")
	binary.Write(&h, binary.LittleEndian, uint32(len(pcm)))
	return append(h.Bytes(), pcm...)
}
