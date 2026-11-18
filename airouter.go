// Package airouter is the one-import way to use airouter from your own Go
// project: free-tier AI providers behind one call, with automatic failover,
// limit tracking and encrypted key storage.
//
//	c, err := airouter.Open(airouter.Options{DB: "ai.db"})
//	if err != nil { log.Fatal(err) }
//	defer c.Close()
//
//	// once: put your provider keys in the database (encrypted)
//	c.AddFreePool(ctx, map[string]string{"GROQ_API_KEY": "gsk_...", "GEMINI_API_KEY": "..."})
//
//	text, err := c.Chat(ctx, "Write one sentence about rain")
//
// Or serve the OpenAI-compatible API + web UI from your own program:
//
//	http.ListenAndServe(":8080", c.Handler(os.Getenv("AIROUTER_ADMIN_TOKEN")))
//
// Bring your own database instead of SQLite with NewClient(store).
package airouter

import (
	"context"
	"net/http"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/server"
	"github.com/mrmhthegreat/airouter/pkg/sqlopen"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Options configures Open.
type Options struct {
	DB     string                           // SQLite file (default "airouter.db"); shared with the CLI
	Logf   func(format string, args ...any) // optional logger
	Router router.Options                   // advanced: timeouts, strategy (Store/Logf are filled in for you)
}

// Client is a Router plus conveniences. All *router.Router methods
// (Generate, Speak, Transcribe, CreateTenant, Status, Usage, ...) are available.
type Client struct{ *router.Router }

// Open opens the SQLite database and returns a ready client. Provider keys are
// encrypted with $AIROUTER_SECRET or a key file next to the database.
func Open(o Options) (*Client, error) {
	if o.DB == "" {
		o.DB = "airouter.db"
	}
	st, _, err := sqlopen.Store(o.DB, o.Logf)
	if err != nil {
		return nil, err
	}
	return newClient(st, o)
}

// NewClient builds a client on any store.Store (for example store.NewMemoryStore()
// in tests, or your own Postgres implementation).
func NewClient(st store.Store, o Options) (*Client, error) { return newClient(st, o) }

func newClient(st store.Store, o Options) (*Client, error) {
	ro := o.Router
	ro.Store = st
	if ro.Logf == nil {
		ro.Logf = o.Logf
	}
	r, err := router.New(ro)
	if err != nil {
		return nil, err
	}
	return &Client{r}, nil
}

// Close releases the database.
func (c *Client) Close() error { return c.Store().Close() }

// Chat sends one prompt and returns the answer text.
func (c *Client) Chat(ctx context.Context, prompt string) (string, error) {
	r, err := c.Generate(ctx, types.Request{Prompt: prompt})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// Ask is Generate with the full request/response (system prompt, JSON mode, ...).
func (c *Client) Ask(ctx context.Context, req types.Request) (*types.Response, error) {
	return c.Generate(ctx, req)
}

// AddFreePool imports the curated free-tier catalog for every provider you
// supply a key for. keys is keyed by env var name ("GROQ_API_KEY",
// "GEMINI_API_KEY", "OPENROUTER_API_KEY", ...). Keys are stored encrypted, so
// no environment variables are needed afterwards. Keys already set in the
// environment are used too. Returns the names added and skipped.
func (c *Client) AddFreePool(ctx context.Context, keys map[string]string, categories ...string) (added, skipped []string, err error) {
	return c.BootstrapProvidersOpts(ctx, router.BootstrapOptions{Keys: keys, StoreKeys: true, Categories: categories})
}

// Handler returns the HTTP API: OpenAI-compatible /v1/chat/completions, the
// native API, and the web UI at "/". adminToken enables admin endpoints
// (empty disables them).
func (c *Client) Handler(adminToken string) http.Handler {
	return server.NewHandler(c.Router, adminToken)
}
