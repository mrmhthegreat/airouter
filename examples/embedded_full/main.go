// Example: embed airouter — the full tour (three layers, one *Client).
//
// This file shows every way you can use a single *airouter.Client:
//
//  1. In-process Go calls      — c.Generate / c.Status / c.SaveProvider / ...
//  2. HTTP handler mounted       — c.Handler(token) → mount at any URL prefix
//  3. CLI subcommands            — clicmd.Dispatch(args) → same code as the
//     standalone `airouter` binary
//
// All three are the SAME *Client, SAME DB, SAME package methods. The
// embedded.go file is the short tour (just #1). This one is the long tour.
//
// RUN:
//
//	export GROQ_API_KEY=gsk_...
//	export AIROUTER_ADMIN_TOKEN=change-me       # protects /airouter/v1/providers* etc.
//	go run ./examples/embedded_full.go
//
// Then in another terminal:
//
//	curl localhost:8000/airouter/healthz
//	curl localhost:8000/airouter/v1/providers -H "Authorization: Bearer change-me"
//	curl -X POST localhost:8000/airouter/v1/bootstrap?store_keys=true -H "Authorization: Bearer change-me"
//
// Or use the embedded CLI from yet another terminal:
//
//	AIROUTER_DB=./example.db go run ./cmd/airouter provider list
//
// (Once you've built the standalone binary: `./airouter provider list`.)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrmhthegreat/airouter"
	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── 1. Open the Client ─────────────────────────────────────────────────
	//
	// One Client. Every layer below uses this same object.
	c, err := airouter.Open(airouter.Options{DB: "example.db", Logf: log.Printf})
	if err != nil {
		log.Fatalf("airouter.Open: %v", err)
	}
	defer c.Close()

	// ── 2. Import env keys (idempotent) ────────────────────────────────────
	//
	// Same as embedded.go — copy any GROQ_API_KEY etc. into the encrypted
	// store so subsequent runs don't need env vars. Safe to repeat.
	keys := map[string]string{}
	for _, k := range []string{"GROQ_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY", "ZAI_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			keys[k] = v
		}
	}
	if len(keys) > 0 {
		added, skipped, _ := c.AddFreePool(ctx, keys)
		log.Printf("[setup] imported %d key(s) (%d already present)", len(added), len(skipped))
	}

	// ── LAYER 1: in-process Go calls ───────────────────────────────────────
	//
	// The engine + /api routes in any embedder call these directly.
	// Demonstrated here: Status, SaveProvider, BootstrapProvidersOpts, Generate.
	log.Println("[layer 1] in-process Go calls")

	// (a) Read live status — same call /api/usage and `airouter provider list` make.
	st := c.Status(ctx, "")
	log.Printf("  Status: %d providers, %d ready", len(st), countReady(st))

	// (b) Bootstrap the free-pool catalog from env keys (same as `airouter bootstrap -store-keys`).
	added, skipped, err := c.BootstrapProvidersOpts(ctx, router.BootstrapOptions{
		StoreKeys:  true,             // copy env keys into the encrypted DB
		Categories: []string{"text"}, // text models only (default)
	})
	if err != nil {
		log.Printf("  BootstrapProvidersOpts: %v", err)
	} else {
		log.Printf("  Bootstrap: added %d, skipped %d", len(added), len(skipped))
	}

	// (c) Add a custom provider by hand (same as `airouter provider add`).
	cfg := types.ProviderConfig{
		Name: "my-custom-groq", Kind: "openai",
		BaseURL:  "https://api.groq.com/openai/v1",
		Model:    "openai/gpt-oss-120b",
		APIKey:   os.Getenv("GROQ_API_KEY"), // will be encrypted at rest
		Priority: 50,
	}
	if cfg.APIKey != "" {
		if err := c.SaveProvider(ctx, &cfg); err != nil {
			log.Printf("  SaveProvider: %v", err)
		} else {
			log.Printf("  SaveProvider: added #%d %q", cfg.ID, cfg.Name)
		}
	}

	// (d) Generate one prompt (same as `airouter ask` and /v1/chat/completions).
	resp, err := c.Generate(ctx, types.Request{
		Prompt: "Say one short sentence about the wind.",
	})
	if err != nil {
		logError("Generate", err)
	} else {
		log.Printf("  Generate: %q via %s/%s (%d tokens, %dms, %d failover(s))",
			resp.Text, resp.Provider, resp.Model, resp.Tokens, resp.LatencyMS, len(resp.Attempts))
	}

	// ── LAYER 2: HTTP handler mounted at /airouter/ ────────────────────────
	//
	// c.Handler(adminToken) returns an http.Handler that exposes the full
	// airouter HTTP API + web UI. Mount it at any URL prefix with
	// http.StripPrefix — the handler uses relative paths internally
	// (/v1/providers, /v1/chat/completions, /, …).
	//
	// Admin endpoints (/v1/providers*, /v1/bootstrap, /v1/tenants*) need
	// Authorization: Bearer $AIROUTER_ADMIN_TOKEN. Public endpoints
	// (/v1/presets, /v1/presets/free-pool, /healthz) need no auth. Tenant
	// endpoints (/v1/chat/completions, /v1/usage, /v1/me/*) take the tenant
	// API key instead.
	log.Println("[layer 2] HTTP handler mounted at /airouter/")
	adminToken := os.Getenv("AIROUTER_ADMIN_TOKEN")
	if adminToken == "" {
		log.Println("  warning: AIROUTER_ADMIN_TOKEN is empty — admin endpoints are disabled")
	}
	airouterHandler := c.Handler(adminToken)

	// ── LAYER 3: CLI subcommands via the clicmd package ────────────────────
	//
	// Uncomment to dispatch the same subcommands the standalone `airouter`
	// binary supports (`provider list`, `bootstrap`, `presets`, `free-pool`,
	// `ask`, `speak`, `transcribe`, `tenant`, `usage`, `me`):
	//
	//   import "github.com/mrmhthegreat/airouter/pkg/clicmd"
	//   if len(os.Args) > 1 && os.Args[1] == "cli" {
	//       clicmd.Dispatch(os.Args[2:])  // e.g. `./embedded_full cli provider list`
	//       return
	//   }
	//
	// We don't run it here because Dispatch exits the process on errors,
	// which would skip the HTTP server below. In your own main() you'd
	// typically route CLI subcommands BEFORE starting the HTTP server.
	log.Println("[layer 3] clicmd subcommands available — see the comment in main()")

	// ── Build the HTTP server: your own routes + airouter mounted ────────
	//
	// Demonstrates mounting airouter inside a mux that already has its own
	// routes. The airouter handler is at /airouter/; / and /healthz are
	// custom routes from this embedder.
	mux := http.NewServeMux()

	// (custom) root health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","app":"embedded_full"}`)
	})

	// (custom) one route that uses the Client in-process — same as /api/chat
	// in the emotion project
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Prompt == "" {
			http.Error(w, `{"detail":"bad json"}`, 400)
			return
		}
		resp, err := c.Generate(r.Context(), types.Request{Prompt: in.Prompt})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"detail":%q}`, err.Error()), 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"text": resp.Text, "provider": resp.Provider, "model": resp.Model,
		})
	})

	// (mounted) airouter's full HTTP API + web UI at /airouter/
	mux.Handle("/airouter/", http.StripPrefix("/airouter", airouterHandler))

	// ── Start the server ──────────────────────────────────────────────────
	addr := ":8000"
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      4 * time.Minute,
	}
	log.Printf("[serve] http://localhost%s  — try:", addr)
	log.Printf("  curl localhost%s/healthz", addr)
	log.Printf("  curl -X POST localhost%s/ask -d '{\"prompt\":\"hello\"}'", addr)
	log.Printf("  curl localhost%s/airouter/healthz", addr)
	log.Printf("  curl localhost%s/airouter/v1/presets/free-pool", addr)
	log.Printf("  curl localhost%s/airouter/v1/providers -H 'Authorization: Bearer %s'", addr, adminToken)
	log.Printf("  open http://localhost%s/airouter/  in a browser", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// ── helpers (same as embedded.go) ──────────────────────────────────────────

func countReady(st []router.ProviderStatus) int {
	n := 0
	for _, s := range st {
		if s.State == "ok" {
			n++
		}
	}
	return n
}

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
