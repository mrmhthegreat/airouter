package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// cmdTranscribe implements `airouter transcribe [flags] AUDIO_FILE`.
//
// Reads the audio file, sends it to an STT provider (Groq Whisper by default),
// and prints the transcribed text to stdout.
//
// Examples:
//
//	airouter transcribe recording.mp3
//	airouter transcribe -language en recording.wav
//	airouter transcribe -provider groq-whisper-large-v3-turbo recording.mp3
//	airouter transcribe -prompt " Interview with Dr. Smith" recording.mp3
//
// Use -envelope to print the full response (provider, model, language, latency)
// as JSON on stderr after the text.
func cmdTranscribe(args []string) error {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	db := dbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	language := fs.String("language", "", "optional ISO-639-1 language hint (e.g. 'en', 'ar')")
	prompt := fs.String("prompt", "", "optional style/spelling prompt (max 224 tokens)")
	provider := fs.String("provider", "", "pin one STT provider by name")
	envelope := fs.Bool("envelope", false, "print the full response (provider, model, language, latency) as JSON on stderr")
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		return fmt.Errorf("give an audio file path as an argument (e.g. `airouter transcribe recording.mp3`)")
	}
	path := fs.Arg(0)
	audio, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	r, err := openRouter(*db)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, err := r.Transcribe(ctx, types.TranscribeRequest{
		TenantID: *tenant,
		Audio:    audio,
		Filename: path,
		Language: *language,
		Prompt:   *prompt,
		Provider: *provider,
	})
	if err != nil {
		return err
	}

	fmt.Println(resp.Text)
	if *envelope || stdoutIsTerminal() {
		lang := resp.Language
		if lang == "" {
			lang = "?"
		}
		fmt.Fprintf(os.Stderr, "[%s / %s, language=%s, %dms]\n",
			resp.Provider, resp.Model, lang, resp.LatencyMS)
	}
	return nil
}
