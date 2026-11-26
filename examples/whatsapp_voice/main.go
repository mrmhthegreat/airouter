// Example: an app (for example a WhatsApp bot) that uses airouter as a LIBRARY.
// No HTTP server, no admin token. Everything runs in-process on one SQLite file.
//
// It shows:
//   - a tenant whose daily cap counts only the shared pool (own keys are free)
//   - speech with an ElevenLabs voice chosen at request time, returned as Ogg/Opus
//     (the format WhatsApp voice notes need)
//   - cloning a voice from recordings (with explicit consent)
//   - one call that gives the status page everything (TenantOverview)
//   - MaxWait: wait briefly for a rate-limited provider before failing over
//
// RUN (needs a paid ElevenLabs plan only for the clone step):
//
//	export ELEVENLABS_API_KEY=...
//	export GROQ_API_KEY=...
//	go run ./examples/whatsapp_voice
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/mrmhthegreat/airouter"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func main() {
	ctx := context.Background()

	c, err := airouter.Open(airouter.Options{DB: "whatsapp-example.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// One-time setup: shared text pool + an ElevenLabs provider for speech.
	// (Safe to run again: existing models are skipped.)
	if _, _, err := c.AddFreePool(ctx, map[string]string{"GROQ_API_KEY": os.Getenv("GROQ_API_KEY")}); err != nil {
		log.Println("free pool:", err)
	}
	if key := os.Getenv("ELEVENLABS_API_KEY"); key != "" {
		_ = c.SaveProvider(ctx, &types.ProviderConfig{
			Name: "eleven", Kind: "openai", BaseURL: "https://api.elevenlabs.io",
			APIKey: key, Model: "eleven_flash_v2_5", Category: types.CategoryTTS,
		})
	}

	// A tenant: 200 shared-pool requests per day. Requests answered by the
	// tenant's own keys do not count and keep working after the cap.
	if _, _, err := c.CreateTenant(ctx, types.Tenant{ID: "shop42", DailyRequestLimit: 200, PoolOnlyCap: true}); err != nil {
		log.Println("tenant:", err) // already exists
	}
	// Change it later without recreating the tenant:
	if _, err := c.UpdateTenantCap(ctx, "shop42", 300, true, false); err != nil {
		log.Fatal(err)
	}

	// 1. Write the reply with AI (waits up to 5s for a briefly limited provider).
	reply, err := c.Generate(ctx, types.Request{
		TenantID: "shop42",
		System:   "You write short, friendly WhatsApp replies.",
		Prompt:   "Tell the customer their order #1234 shipped today.",
		MaxWait:  5 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("reply (%s, own key: %v): %s\n", reply.Provider, reply.Own, reply.Text)

	// 2. Turn it into a WhatsApp voice note. The voice is picked per request,
	//    by name or id, and tuned for this one message.
	stability, speed := 0.4, 1.05
	note, err := c.Speak(ctx, types.SpeakRequest{
		TenantID:      "shop42",
		Text:          reply.Text,
		Voice:         "Rachel",
		Format:        "opus", // mono Ogg/Opus
		VoiceSettings: &types.VoiceSettings{Stability: &stability, Speed: &speed},
		MaxWait:       5 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("voice-note.ogg", note.Audio, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("voice note: %s, %d bytes (%s)\n", note.Format, len(note.Audio), note.ContentType)

	// 3. Clone the shop owner's voice. Consent is required.
	if len(os.Args) > 1 {
		path := os.Args[1]

		if !filepath.IsLocal(path) {
			log.Fatal("audio file must be inside the current directory")
		}

		root, err := os.OpenRoot(".")
		if err != nil {
			log.Fatal(err)
		}
		defer root.Close()

		file, err := root.Open(path)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			log.Fatal(err)
		}
		v, err := c.CloneVoice(ctx, types.CloneVoiceRequest{
			TenantID:         "default", // "default" may clone in the shared ElevenLabs account
			Name:             "Shop owner",
			Samples:          []types.VoiceSample{{Filename: "sample.mp3", Data: data}},
			ConsentConfirmed: true, // the speaker agreed
		})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("cloned voice:", v.Name, v.ID, "- use it with Voice:", fmt.Sprintf("%q", v.Name))
	}

	// 4. Everything a status page needs, in ONE call.
	ov, err := c.TenantOverview(ctx, "shop42")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("cap: %d of %d used (pool only: %v), resets in %ds\n", ov.Cap.Used, ov.Cap.Limit, ov.Cap.PoolOnly, ov.Cap.ResetsInSec)
	for _, m := range ov.Shared {
		fmt.Printf("  shared %-40s %-8s resets in %ds\n", m.Name, m.State, m.ResetsInSec)
	}
}
