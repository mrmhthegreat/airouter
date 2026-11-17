// Example: embed airouter in any Go program. This is the pattern to use when
// you want the auto-switching/free-tier behaviour without running the HTTP
// server — for example inside a worker, a CLI tool, or a long-running daemon.
//
// Run:   go run examples/embedded.go
// (make sure GROQ_API_KEY or GEMINI_API_KEY is set in your env)

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func main() {
	// Open a SQLite DB. The same DB can be shared with the airouter CLI —
	// usage and pauses will round-trip correctly.
	db, err := sql.Open("sqlite3", "file:airouter.db?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	st, err := store.NewSQLStore(db)
	if err != nil {
		log.Fatal(err)
	}

	r, err := router.New(router.Options{
		Store: st,
		Logf:  func(f string, a ...any) { log.Printf("[airouter] "+f, a...) },
	})
	if err != nil {
		log.Fatal(err)
	}

	// Bootstrap the DB from the curated free-pool catalog. Skips providers
	// whose env var is unset, so this is safe to call repeatedly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	added, skipped, err := r.BootstrapProviders(ctx, false, "", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("bootstrap: added %d, skipped %d\n", len(added), len(skipped))
	if len(added) == 0 {
		fmt.Println("nothing added — set GROQ_API_KEY or GEMINI_API_KEY and retry.")
		os.Exit(0)
	}

	// A normal generate call. The router picks the best provider, sends the
	// request, and fails over automatically on errors or limits.
	resp, err := r.Generate(ctx, types.Request{
		Prompt: "Write one short sentence about the wind.",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("answer: %s\n", resp.Text)
	fmt.Printf("provider=%s model=%s tokens=%d latency=%dms\n",
		resp.Provider, resp.Model, resp.Tokens, resp.LatencyMS)
	if len(resp.Attempts) > 0 {
		fmt.Printf("failed over after %d attempt(s):\n", len(resp.Attempts))
		for _, a := range resp.Attempts {
			fmt.Printf("  - %s: %s\n", a.Provider, a.Error)
		}
	}

	// Same call, but ask for JSON output. The router will validate the model's
	// output and fail over to the next provider if it returns junk.
	resp, err = r.Generate(ctx, types.Request{
		System: `Reply as {"emotion": "...", "intensity": 1-10} only.`,
		Prompt: "I just found a wallet on the sidewalk.",
		JSON:   true,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\njson answer: %s\n", string(resp.JSON))

	// Text-to-speech: same failover behaviour, but only TTS-capable providers
	// are considered (Groq Orpheus + Gemini TTS).
	if _, _, err := r.BootstrapProviders(ctx, true, "", []string{"tts"}); err != nil {
		log.Printf("bootstrap tts: %v", err)
	}
	speakResp, err := r.Speak(ctx, types.SpeakRequest{
		Text:  "Hello from airouter.",
		Voice: "Kore", // Gemini default voice; ignored for Groq
	})
	if err != nil {
		log.Printf("speak: %v (set GEMINI_API_KEY or GROQ_API_KEY to enable TTS)", err)
	} else {
		if err := os.WriteFile("/tmp/airouter-hello.wav", speakResp.Audio, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nspeak: wrote %d bytes of %s audio from %s/%s (voice=%s, %dms)\n",
			len(speakResp.Audio), speakResp.ContentType, speakResp.Provider, speakResp.Model,
			speakResp.Voice, speakResp.LatencyMS)
		fmt.Println("listen with:  play /tmp/airouter-hello.wav   (or afplay on macOS)")
	}

	// Speech-to-text: only Groq Whisper is routed today (Gemini Transcribe
	// would need a two-step Files API upload, not yet implemented).
	if _, _, err := r.BootstrapProviders(ctx, true, "", []string{"stt"}); err != nil {
		log.Printf("bootstrap stt: %v", err)
	}
	if speakResp != nil {
		// Round-trip: transcribe the WAV we just generated.
		transResp, err := r.Transcribe(ctx, types.TranscribeRequest{
			Audio:    speakResp.Audio,
			Filename: "hello.wav",
			Language: "en",
		})
		if err != nil {
			log.Printf("transcribe: %v", err)
		} else {
			fmt.Printf("\nround-trip transcription: %q\n", transResp.Text)
		}
	}
}
