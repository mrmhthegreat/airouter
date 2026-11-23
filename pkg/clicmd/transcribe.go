package clicmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Transcribe implements `transcribe [flags] AUDIO_FILE`. Calls
// *airouter.Client.Transcribe — audio file → text. Reads the file, sends it
// to an STT provider (Groq Whisper by default), prints the transcribed text
// to stdout.
//
// Args:
//
//	["recording.mp3"]                              → default STT provider, auto language
//	["-language", "en", "recording.wav"]           → language hint
//	["-provider", "groq-whisper-large-v3-turbo", "recording.mp3"]  → pin STT provider
//	["-prompt", " Interview with Dr. Smith", "recording.mp3"]       → spelling/style hint
//	["-envelope"]                                  → full response as JSON on stderr
func Transcribe(args []string) error {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	language := fs.String("language", "", "optional ISO-639-1 language hint (e.g. 'en', 'ar')")
	prompt := fs.String("prompt", "", "optional style/spelling prompt (max 224 tokens)")
	provider := fs.String("provider", "", "pin one STT provider by name")
	envelope := fs.Bool("envelope", false, "print the full response (provider, model, language, latency) as JSON on stderr")
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		return fmt.Errorf("give an audio file path as an argument (e.g. `transcribe recording.mp3`)")
	}
	path := fs.Arg(0)
	audio, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	r, err := OpenRouter(*db)
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
