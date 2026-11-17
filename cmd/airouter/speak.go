package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/you/airouter/pkg/types"
)

// cmdSpeak implements `airouter speak [flags] TEXT`.
//
// Outputs the audio bytes to stdout by default (binary). Use -o to write to a
// file instead, which is usually what you want because most terminals can't
// play raw WAV bytes from stdout.
//
// Examples:
//   airouter speak -o hello.wav "Hello world"
//   airouter speak -voice Kore -provider gemini-tts-gemini-3.8-flash-tts "Hello"
//   airouter speak -o arabic.wav -voice hassan "مرحبا بالعالم"
func cmdSpeak(args []string) error {
	fs := flag.NewFlagSet("speak", flag.ExitOnError)
	db := dbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	voice := fs.String("voice", "", "voice id (default: Kore for Gemini, tara for Groq Orpheus English)")
	format := fs.String("format", "wav", "audio format: wav (default), mp3, pcm")
	language := fs.String("language", "", "optional language hint (e.g. 'en', 'ar')")
	provider := fs.String("provider", "", "pin one TTS provider by name")
	outFile := fs.String("o", "", "write audio to this file instead of stdout")
	envelope := fs.Bool("envelope", false, "print the response metadata (provider, model, voice, latency) on stderr after the audio")
	_ = fs.Parse(args)

	text := joinSpace(fs.Args())
	if text == "" {
		return fmt.Errorf("no text given (pass it as an argument)")
	}

	r, err := openRouter(*db)
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
		// Write audio bytes to stdout. If stdout is a terminal, warn first so
		// the user does not get binary garbage in their shell.
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

// joinSpace is a local helper to avoid importing strings just for one call.
// (helpers.go already imports strings; this is duplicated to keep this file
// standalone for readers.)
func joinSpace(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}
