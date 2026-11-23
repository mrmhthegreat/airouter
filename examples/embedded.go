// Example: embed airouter as a Go library — the short tour.
//
// This file is a runnable walkthrough. It opens an airouter Client, imports
// whatever API keys it finds in the environment into the encrypted store,
// runs two prompts (one convenience Chat, one structured Ask with JSON mode),
// and prints live provider status at the end.
//
// RUN:
//
//	export GROQ_API_KEY=gsk_...       # and/or GEMINI_API_KEY, OPENROUTER_API_KEY, ZAI_API_KEY
//	go run ./examples
//
// After the first run the keys live (encrypted) in ./example.db, so later
// runs need no environment variables at all.
//
// See embedded_full.go for the long version that also mounts the HTTP handler
// and calls the clicmd package — the three ways you can use the same Client.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mrmhthegreat/airouter"
	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ---- 1. Open the Client ------------------------------------------------
	//
	// airouter.Open returns a *Client that already has every method on it:
	//   - Generate / Chat / Ask        (send prompts)
	//   - Status / Usage               (read live state)
	//   - SaveProvider / DeleteProvider / SetDisabled / SetProviderKey / ClearPause
	//   - BootstrapProvidersOpts       (import from the free-pool catalog)
	//   - Speak / Transcribe           (TTS / STT)
	//   - Handler(adminToken)          (returns the HTTP handler — see embedded_full.go)
	//
	// The DB file holds provider configs, tenants, usage history, and
	// encrypted API keys. The encryption secret comes from $AIROUTER_SECRET
	// or a .key file next to the DB (auto-created, mode 0600).
	c, err := airouter.Open(airouter.Options{DB: "example.db", Logf: log.Printf})
	if err != nil {
		log.Fatalf("airouter.Open: %v", err)
	}
	defer c.Close()

	// ---- 2. Import env keys (idempotent) -----------------------------------
	//
	// AddFreePool takes a map of {ENV_VAR_NAME: value} and imports every
	// catalog model whose env var is now non-empty. The keys are stored
	// ENCRYPTED in the DB; after this you can unset the env vars. Calling it
	// again with the same keys is a no-op (existing entries are skipped),
	// so it's safe to call on every startup.
	keys := map[string]string{}
	for _, k := range []string{"GROQ_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY", "ZAI_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			keys[k] = v
		}
	}
	if len(keys) > 0 {
		added, skipped, err := c.AddFreePool(ctx, keys)
		if err != nil {
			log.Printf("AddFreePool: %v", err)
		}
		fmt.Printf("[setup] imported %d provider key(s) into the encrypted store (%d already present)\n",
			len(added), len(skipped))
	}

	// ---- 3. Live provider status (in-process Go call) ---------------------
	//
	// Status returns the live state of every provider: state (ok / cooling
	// down / daily limit reached / disabled / no api key), how many requests
	// it has used today, when its quota resets, etc. Same call /api/usage and
	// `airouter provider list` make — same *Client, same DB.
	st := c.Status(ctx, "") // "" = admin view (every provider, every tenant)
	fmt.Printf("[status] %d providers, %d ready\n", len(st), countReady(st))

	// ---- 4. Chat — the convenience one-liner -------------------------------
	//
	// Chat sends a single user prompt and returns just the text. Use this
	// when you don't need the system prompt, JSON mode, or the attempt list.
	// Internally it calls Generate with a plain Request{Prompt: prompt}.
	text, err := c.Chat(ctx, "Write one short sentence about the wind.")
	if err != nil {
		// *types.ExhaustedError  → every provider is busy right now; err.RetryAfter tells you when to retry.
		// *types.AllFailedError  → every provider errored; err.Attempts lists what each said.
		// *types.TenantLimitError → you hit your tenant's daily cap.
		logError("Chat", err)
	} else {
		fmt.Printf("[chat]  %s\n", text)
	}

	// ---- 5. Ask — the full Request/Response shape --------------------------
	//
	// Ask takes a types.Request (System, Prompt, Messages, JSON, MaxTokens,
	// Temperature, Provider, Strategy, TenantID) and returns a *types.Response
	// with Text, JSON, Provider, Model, Tokens, LatencyMS, Attempts.
	// Set JSON=true to ask the model for a JSON-only answer — airouter
	// validates the JSON and stores the raw payload in resp.JSON.
	resp, err := c.Ask(ctx, types.Request{
		System: `Reply as {"emotion": "...", "intensity": 1-10} only.`,
		Prompt: "I just found a wallet on the sidewalk.",
		JSON:   true,
	})
	if err != nil {
		logError("Ask", err)
	} else {
		// resp.JSON is a json.RawMessage — pretty-print it as a structured value.
		var v map[string]any
		if err := json.Unmarshal(resp.JSON, &v); err == nil {
			pretty, _ := json.MarshalIndent(v, "", "  ")
			fmt.Printf("[ask]   %s\n        via %s / %s, %d tokens, %dms, %d failover(s)\n",
				pretty, resp.Provider, resp.Model, resp.Tokens, resp.LatencyMS, len(resp.Attempts))
		}
	}

	// ---- 6. What's next ----------------------------------------------------
	//
	// - Mount the HTTP handler at any URL prefix:
	//     mux.Handle("/airouter/", http.StripPrefix("/airouter", c.Handler(adminToken)))
	//   See embedded_full.go for a runnable version.
	//
	// - Use the clicmd package for terminal access:
	//     import "github.com/mrmhthegreat/airouter/pkg/clicmd"
	//     clicmd.Provider([]string{"list"})   // same as `airouter provider list`
	//
	// - Add or remove providers programmatically:
	//     c.SaveProvider(ctx, &types.ProviderConfig{...})
	//     c.DeleteProvider(ctx, id)
	//     c.SetDisabled(ctx, id, true)
	//
	// See embedded_full.go for all of these in one file.
}

func countReady(st []router.ProviderStatus) int {
	n := 0
	for _, s := range st {
		if s.State == "ok" {
			n++
		}
	}
	return n
}

// logError prints structured airouter errors in a human-readable form.
func logError(what string, err error) {
	var ex *types.ExhaustedError
	var af *types.AllFailedError
	var tl *types.TenantLimitError
	switch {
	case errors.As(err, &ex):
		log.Printf("%s: every provider is busy — retry in %s", what, ex.RetryAfter.Round(time.Second))
		for name, why := range ex.Reasons {
			log.Printf("  - %s: %s", name, why)
		}
	case errors.As(err, &af):
		log.Printf("%s: every provider failed", what)
		for _, a := range af.Attempts {
			log.Printf("  - %s: %s", a.Provider, a.Error)
		}
	case errors.As(err, &tl):
		log.Printf("%s: tenant daily limit reached — resets in %s", what, tl.RetryAfter.Round(time.Minute))
	default:
		log.Printf("%s: %v", what, err)
	}
}
