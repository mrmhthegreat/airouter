package clicmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/server"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Ask implements `ask [flags] PROMPT...`. Calls *airouter.Client.Generate.
// Output format (in priority order):
//
//	-envelope          full Response as JSON (text + provider + tokens + attempts)
//	-json              ask the model for JSON and pretty-print only that JSON
//	-text              raw text only, no [provider] footer on stderr
//	default            text on stdout + a [provider / model, N tokens, Xms] footer on stderr
//	                  when stdout is a terminal. If stdout is piped, no footer.
//
// Args:
//
//	["What is the capital of France?"]               → text answer + footer
//	["-system", "Be terse.", "What is 2+2?"]        → with system prompt
//	["-json", "Return {\"x\": 5}"]                  → JSON-only output
//	["-provider", "groq-openai/gpt-oss-120b", "..."] → pin one provider
//	["-tenant", "acme", "..."]                      → use this tenant's quota
//	[]                                              → reads prompt from stdin
func Ask(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	system := fs.String("system", "", "system prompt")
	asJSON := fs.Bool("json", false, "ask the model for JSON and print only that JSON")
	envelope := fs.Bool("envelope", false, "print the full response (provider, model, tokens, attempts) as JSON")
	asText := fs.Bool("text", false, "raw text only (no provider footer on stderr)")
	provider := fs.String("provider", "", "pin one provider by name")
	strategy := fs.String("strategy", "", "priority | spread")
	maxTok := fs.Int("max-tokens", 0, "max output tokens")
	_ = fs.Parse(args)

	prompt, err := ReadPromptFromStdinOrArgs(fs.Args())
	if err != nil {
		return err
	}
	if prompt == "" {
		return fmt.Errorf("no prompt given (pass it as an argument or on stdin)")
	}

	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, err := r.Generate(ctx, types.Request{
		TenantID: *tenant, System: *system, Prompt: prompt,
		JSON: *asJSON, Provider: *provider, Strategy: *strategy, MaxTokens: *maxTok,
	})
	if err != nil {
		return err
	}

	switch {
	case *envelope:
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(out))
	case *asJSON:
		var v any
		_ = json.Unmarshal(resp.JSON, &v)
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
	default:
		fmt.Println(resp.Text)
	}

	if !*asText && !*envelope && !*asJSON && stdoutIsTerminal() {
		fmt.Fprintf(os.Stderr, "[%s / %s, %d tokens, %dms]\n", resp.Provider, resp.Model, resp.Tokens, resp.LatencyMS)
	}
	return nil
}

// stdoutIsTerminal reports whether os.Stdout is a character device (a TTY).
// Implemented without golang.org/x/term to keep the dependency list small.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode().Type() == fs.ModeCharDevice
}

// Serve implements `serve [-addr :8080]`. Runs the airouter HTTP API + web UI
// as a standalone server. The same handler can be mounted inside another
// program via airouter.Client.Handler(token) — see the emotion project's
// serve.go for an example of mounting at /airouter/.
//
// Args:
//
//	["-addr", ":8080"]                  listen address (default :8080)
//	["-cors", "*"]                      allowed browser origins (default: off)
//	["-allow-tenant-urls"]              let tenants add providers with any base URL
//	["-block-tenant-ollama"]            don't let tenants use this server's Ollama
//	["-max-tenant-models", "100"]       most models one tenant may add
func Serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	db := DbFlag(fs)
	addr := fs.String("addr", ":8080", "listen address")
	cors := fs.String("cors", os.Getenv("AIROUTER_CORS"), "allowed browser origins, comma-separated, or * (default: off)")
	allowURLs := fs.Bool("allow-tenant-urls", os.Getenv("AIROUTER_ALLOW_TENANT_URLS") == "1", "let tenants add models with ANY base URL (default: only catalog providers; custom URLs let a tenant aim the server at internal hosts)")
	blockOllama := fs.Bool("block-tenant-ollama", false, "do not let tenants use this server's Ollama")
	maxModels := fs.Int("max-tenant-models", 100, "most models one tenant may add")
	_ = fs.Parse(args)

	r, err := OpenRouter(*db, router.TenantPolicy{AllowCustomBaseURL: *allowURLs, BlockOllama: *blockOllama, MaxProviders: *maxModels})
	if err != nil {
		return err
	}
	admin := os.Getenv("AIROUTER_ADMIN_TOKEN")
	if admin == "" {
		fmt.Fprintln(os.Stderr, "note: AIROUTER_ADMIN_TOKEN is not set → admin endpoints are disabled; use the CLI.")
	}
	ts, _ := r.Store().ListTenants(context.Background())
	if len(ts) <= 1 {
		fmt.Fprintln(os.Stderr, "note: no tenant keys yet. Create one:  tenant add NAME")
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.WithCORS(server.NewHandler(r, admin), *cors),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      4 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	fmt.Fprintf(os.Stderr, "airouter listening on %s (db: %s)\n  web UI:        http://localhost%s/\n  OpenAI base:   http://localhost%s/v1\n", *addr, *db, *addr, *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
