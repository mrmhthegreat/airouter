package main

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

        "github.com/you/airouter/pkg/server"
        "github.com/you/airouter/pkg/types"
)

// cmdAsk implements `airouter ask [flags] PROMPT...`.
//
// Output format (in priority order):
//   -envelope          full Response as JSON (text + provider + tokens + attempts)
//   -json              ask the model for JSON and pretty-print only that JSON
//   -text              raw text only, no [provider] footer on stderr
//   default            text on stdout + a [provider / model, N tokens, Xms] footer on stderr
//                      when stdout is a terminal. If stdout is piped, no footer.
func cmdAsk(args []string) error {
        fs := flag.NewFlagSet("ask", flag.ExitOnError)
        db := dbFlag(fs)
        tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
        system := fs.String("system", "", "system prompt")
        asJSON := fs.Bool("json", false, "ask the model for JSON and print only that JSON")
        envelope := fs.Bool("envelope", false, "print the full response (provider, model, tokens, attempts) as JSON")
        asText := fs.Bool("text", false, "raw text only (no provider footer on stderr)")
        provider := fs.String("provider", "", "pin one provider by name")
        strategy := fs.String("strategy", "", "priority | spread")
        maxTok := fs.Int("max-tokens", 0, "max output tokens")
        _ = fs.Parse(args)

        prompt, err := readPromptFromStdinOrArgs(fs.Args())
        if err != nil {
                return err
        }
        if prompt == "" {
                return fmt.Errorf("no prompt given (pass it as an argument or on stdin)")
        }

        r, err := openRouter(*db)
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

        // Footer goes to stderr so it never corrupts piped stdout output. When
        // stdout is a terminal and the user did not pass -text, print it; when
        // stdout is piped, skip it (so `airouter ask ... | grep` works cleanly).
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

// cmdServe implements `airouter serve [-addr :8080]`.
func cmdServe(args []string) error {
        fs := flag.NewFlagSet("serve", flag.ExitOnError)
        db := dbFlag(fs)
        addr := fs.String("addr", ":8080", "listen address")
        _ = fs.Parse(args)

        r, err := openRouter(*db)
        if err != nil {
                return err
        }
        admin := os.Getenv("AIROUTER_ADMIN_TOKEN")
        if admin == "" {
                fmt.Fprintln(os.Stderr, "note: AIROUTER_ADMIN_TOKEN is not set → admin endpoints are disabled; use the CLI.")
        }
        ts, _ := r.Store().ListTenants(context.Background())
        if len(ts) <= 1 {
                fmt.Fprintln(os.Stderr, "note: no tenant keys yet. Create one:  airouter tenant add NAME")
        }

        srv := &http.Server{
                Addr:              *addr,
                Handler:           server.NewHandler(r, admin),
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
        fmt.Fprintf(os.Stderr, "airouter listening on %s (db: %s)\n", *addr, *db)
        if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
                return err
        }
        return nil
}
