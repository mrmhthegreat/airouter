package clicmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Speak implements `speak [flags] TEXT`. Calls *airouter.Client.Speak —
// text → audio bytes (WAV by default). Outputs the audio bytes to stdout by
// default; use -o to write to a file instead.
//
// Args:
//
//	["-o", "hello.wav", "Hello world"]                 → save WAV to file
//	["-voice", "Kore", "-provider", "gemini-...", "Hello"]  → pin TTS provider + voice
//	["-format", "mp3", "..."]                          → mp3 instead of wav
//	["-language", "ar", "مرحبا بالعالم"]               → language hint
func Speak(args []string) error {
	fs := flag.NewFlagSet("speak", flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	voice := fs.String("voice", "", "voice id (default: Kore for Gemini, tara for Groq Orpheus English)")
	format := fs.String("format", "wav", "audio format: wav (default), mp3, pcm")
	language := fs.String("language", "", "optional language hint (e.g. 'en', 'ar')")
	provider := fs.String("provider", "", "pin one TTS provider by name")
	outFile := fs.String("o", "", "write audio to this file instead of stdout")
	envelope := fs.Bool("envelope", false, "print the response metadata (provider, model, voice, latency) on stderr after the audio")
	_ = fs.Parse(args)

	text := JoinSpace(fs.Args())
	if text == "" {
		return fmt.Errorf("no text given (pass it as an argument)")
	}

	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, err := r.Speak(ctx, types.SpeakRequest{
		TenantID: *tenant,
		Text:     text,
		Voice:    *voice,
		Format:   *format,
		Language: *language,
		Provider: *provider,
	})
	if err != nil {
		return err
	}

	if *outFile != "" {
		if err := os.WriteFile(*outFile, resp.Audio, 0o644); err != nil {
			return err
		}
	} else {
		if stdoutIsTerminal() {
			fmt.Fprintln(os.Stderr, "warning: writing binary audio to stdout. Use -o FILE to save to a file instead.")
		}
		if _, err := os.Stdout.Write(resp.Audio); err != nil {
			return err
		}
	}

	if *envelope || stdoutIsTerminal() {
		fmt.Fprintf(os.Stderr, "[%s / %s, voice=%s, %s, %d bytes, %dms]\n",
			resp.Provider, resp.Model, resp.Voice, resp.ContentType, len(resp.Audio), resp.LatencyMS)
	}
	return nil
}
