// Example: use airouter inside any Go program with the one-import facade.
//
//	go run ./examples            (set GROQ_API_KEY and/or GEMINI_API_KEY once)
//
// After the first run the keys live (encrypted) in ./example.db, so later runs
// need no environment variables at all.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mrmhthegreat/airouter"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func main() {
	ctx := context.Background()
	c, err := airouter.Open(airouter.Options{DB: "example.db", Logf: log.Printf})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	// Store whichever keys you have (encrypted). Safe to repeat: existing
	// entries are skipped.
	keys := map[string]string{}
	for _, k := range []string{"GROQ_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY", "CEREBRAS_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			keys[k] = v
		}
	}
	added, _, err := c.AddFreePool(ctx, keys)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("providers added this run: %d\n", len(added))

	text, err := c.Chat(ctx, "Write one short sentence about the wind.")
	if err != nil {
		log.Fatal(err) // *types.ExhaustedError => everything busy; see err.RetryAfter
	}
	fmt.Println("answer:", text)

	resp, err := c.Ask(ctx, types.Request{
		System: `Reply as {"emotion": "...", "intensity": 1-10} only.`,
		Prompt: "I just found a wallet on the sidewalk.",
		JSON:   true,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("json: %s (via %s/%s, %d failover(s))\n", resp.JSON, resp.Provider, resp.Model, len(resp.Attempts))

	// Want the OpenAI-compatible API + web UI inside your own server?
	//   http.ListenAndServe(":8080", c.Handler(os.Getenv("AIROUTER_ADMIN_TOKEN")))
}
