// Package router is the core of airouter: it takes a Request, picks the best
// available provider, sends the prompt, fails over automatically on errors or
// limits, and returns the Response. Provider selection respects per-provider
// per-model RPM/RPD/TPM limits, per-provider timezone resets, and tenant-level
// daily caps. Long cooldowns (daily limit, bad key) are persisted to the Store
// so they survive restarts and concurrent CLI invocations.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Options configures a new Router. Only Store is required; everything else has
// sensible defaults.
type Options struct {
	Store    store.Store
	Strategy string        // "priority" (default: use first provider until limited) | "spread"
	MaxWait  time.Duration // how long Generate may wait for a briefly-limited provider (default 20s)
	// AttemptTimeout bounds ONE upstream call (default 60s). A provider that
	// hangs is put on a backoff cooldown so the next request skips it.
	AttemptTimeout time.Duration
	// RequestTimeout bounds a whole Generate call including every failover
	// (default 2m). It returns types.ErrTimeout when exceeded.
	RequestTimeout time.Duration
	HTTPClient     *http.Client
	// TenantPolicy limits what tenants may configure for themselves.
	TenantPolicy TenantPolicy
	// OllamaURL is the Ollama server tenants may pick local models from
	// (default $OLLAMA_HOST, else http://localhost:11434).
	OllamaURL    string
	Now          func() time.Time // injectable clock (tests)
	RefreshEvery time.Duration    // how often provider configs are re-read from the Store (default 5s)
	Logf         func(format string, args ...any)
}

// candidate is one provider config paired with its live in-memory state.
// Internal-only — callers never see this type.
type candidate struct {
	cfg   types.ProviderConfig
	state *providerState
}

// Router is the main object. Construct once with New, then call Generate for
// each request. The same Router is safe for concurrent use.
type Router struct {
	store                          store.Store
	strategy                       string
	maxWait                        time.Duration
	attemptTimeout, requestTimeout time.Duration
	tpolicy                        TenantPolicy
	ollamaURL                      string
	client                         *http.Client
	now                            func() time.Time
	refresh                        time.Duration
	logf                           func(string, ...any)

	mu        sync.RWMutex
	cands     []candidate
	states    map[int64]*providerState
	refreshed time.Time
}

// New constructs a Router. It also ensures a default tenant exists (used by
// the CLI and by admin calls), and does an initial Refresh.
func New(o Options) (*Router, error) {
	if o.Store == nil {
		return nil, errors.New("airouter: Options.Store is required")
	}
	r := &Router{
		store:          o.Store,
		strategy:       o.Strategy,
		maxWait:        o.MaxWait,
		attemptTimeout: o.AttemptTimeout,
		requestTimeout: o.RequestTimeout,
		client:         o.HTTPClient,
		now:            o.Now,
		refresh:        o.RefreshEvery,
		tpolicy:        o.TenantPolicy,
		ollamaURL:      o.OllamaURL,
		logf:           o.Logf,
		states:         map[int64]*providerState{},
	}
	if r.strategy == "" {
		r.strategy = "priority"
	}
	if r.maxWait == 0 {
		r.maxWait = 20 * time.Second
	}
	if r.tpolicy.MaxProviders == 0 {
		r.tpolicy.MaxProviders = 100
	}
	if r.ollamaURL == "" {
		r.ollamaURL = os.Getenv("OLLAMA_HOST")
	}
	if r.ollamaURL == "" {
		r.ollamaURL = preset.Definitions["ollama"].BaseURL
	}
	if !strings.Contains(r.ollamaURL, "://") {
		r.ollamaURL = "http://" + r.ollamaURL
	}
	if r.attemptTimeout == 0 {
		r.attemptTimeout = 60 * time.Second
	}
	if r.requestTimeout == 0 {
		r.requestTimeout = 2 * time.Minute
	}
	if r.client == nil {
		r.client = &http.Client{}
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.refresh == 0 {
		r.refresh = 5 * time.Second
	}
	if r.logf == nil {
		r.logf = func(string, ...any) {}
	}
	ctx := context.Background()
	if t, _ := r.store.TenantByID(ctx, types.DefaultTenantID); t == nil {
		// Always have a default tenant for CLI / admin use. Its API key is
		// generated but discarded — admin calls bypass tenant auth.
		if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: types.DefaultTenantID, Name: "Default", TZ: "UTC"}); err != nil {
			return nil, err
		}
	}
	return r, r.Refresh(ctx)
}

// Store returns the underlying store. Useful for admin commands.
func (r *Router) Store() store.Store { return r.store }

// Refresh re-reads provider configs from the Store. Live counters for unchanged
// providers are preserved (a config edit does not reset today's usage).
func (r *Router) Refresh(ctx context.Context) error {
	ps, err := r.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fresh := make([]candidate, 0, len(ps))
	for _, p := range ps {
		st := r.states[p.ID]
		if st == nil {
			st = &providerState{}
			r.states[p.ID] = st
		}
		fresh = append(fresh, candidate{p, st})
	}
	r.cands, r.refreshed = fresh, r.now()
	return nil
}

// snapshot returns a copy of the candidate list, refreshing first if stale.
func (r *Router) snapshot(ctx context.Context) []candidate {
	r.mu.RLock()
	stale := r.now().Sub(r.refreshed) > r.refresh
	r.mu.RUnlock()
	if stale {
		_ = r.Refresh(ctx)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]candidate(nil), r.cands...)
}

// candidates filters the snapshot by tenant / pinned provider / category and
// sorts it by the active strategy. A tenant's own providers always go before
// shared ones. Category defaults to "text" so a Generate call never picks up
// TTS or STT providers (which would fail with a 400/404).
func (r *Router) candidates(ctx context.Context, req types.Request) []candidate {
	category := req.Category
	if category == "" {
		category = types.CategoryText
	}
	var out []candidate
	for _, c := range r.snapshot(ctx) {
		if c.cfg.Disabled || (c.cfg.TenantID != "" && c.cfg.TenantID != req.TenantID) {
			continue
		}
		cfgCat := c.cfg.Category
		if cfgCat == "" {
			cfgCat = types.CategoryText
		}
		if cfgCat != category {
			continue
		}
		if req.Provider != "" && c.cfg.Name != req.Provider {
			continue
		}
		out = append(out, c)
	}
	strategy := req.Strategy
	if strategy == "" {
		strategy = r.strategy
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.cfg.TenantID != "") != (b.cfg.TenantID != "") {
			return a.cfg.TenantID != ""
		}
		if strategy == "spread" {
			fa, fb := a.fractionUsed(), b.fractionUsed()
			if fa != fb {
				return fa < fb
			}
		}
		if a.cfg.Priority != b.cfg.Priority {
			return a.cfg.Priority < b.cfg.Priority
		}
		return a.cfg.ID < b.cfg.ID
	})
	return out
}

// fractionUsed reports how much of the daily cap is consumed (0..1). Used by
// the "spread" strategy to balance load across providers.
func (c candidate) fractionUsed() float64 {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.cfg.RPD > 0 {
		return float64(c.state.requestsToday) / float64(c.cfg.RPD)
	}
	return float64(c.state.requestsToday) / 1000
}

// Generate is the main entry point. It picks the best available provider,
// sends the request, and on failure fails over to the next one — repeating
// until either a provider answers or all are exhausted.
func (r *Router) Generate(ctx context.Context, req types.Request) (*types.Response, error) {
	start := r.now()
	if req.TenantID == "" {
		req.TenantID = types.DefaultTenantID
	}
	tenant, err := r.store.TenantByID(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, types.ErrUnknownTenant
	}
	if tenant.Disabled {
		return nil, types.ErrTenantDisabled
	}
	if tenant.DailyRequestLimit > 0 {
		loc := locFor(tenant.TZ)
		day := start.In(loc).Format("2006-01-02")
		if used, err := r.store.TenantDayUsage(ctx, tenant.ID, day); err == nil && used >= tenant.DailyRequestLimit {
			now := start.In(loc)
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
			return nil, &types.TenantLimitError{RetryAfter: next.Sub(now)}
		}
	}
	msgs, err := buildMessages(req)
	if err != nil {
		return nil, err
	}
	cands := r.candidates(ctx, req)
	if len(cands) == 0 {
		return nil, types.ErrNoProviders
	}

	var attempts []types.Attempt
	failed := map[int64]bool{}
	deadline := start.Add(r.maxWait)

	// One overall budget for the whole request, failovers included.
	parent := ctx
	ctx, cancelAll := context.WithTimeout(ctx, r.requestTimeout)
	defer cancelAll()
	timedOut := func() error {
		if parent.Err() == nil && ctx.Err() != nil {
			return fmt.Errorf("%w after %s (%d provider(s) tried)", types.ErrTimeout, r.requestTimeout, len(attempts))
		}
		return parent.Err()
	}

	for {
		now := r.now()
		var picked *candidate
		minWait := time.Duration(1<<62 - 1)
		reasons := map[string]string{}
		allDaily, remaining := true, 0

		for i := range cands {
			c := &cands[i]
			if failed[c.cfg.ID] {
				continue
			}
			remaining++
			if !c.cfg.HasKey() {
				reasons[c.cfg.Name] = "no API key (set $" + c.cfg.APIKeyEnv + " or store one: airouter provider set-key)"
				continue
			}
			ok, wait, daily, why := r.acquire(ctx, c.cfg, c.state, now)
			if ok {
				picked = c
				break
			}
			reasons[c.cfg.Name] = why
			if !daily {
				allDaily = false
			}
			if wait < minWait {
				minWait = wait
			}
		}

		if picked == nil {
			if remaining == 0 || len(failed) > 0 && allDaily {
				return nil, r.allFailed(attempts)
			}
			if allDaily || now.Add(minWait).After(deadline) || ctx.Err() != nil {
				if err := timedOut(); err != nil && ctx.Err() != nil {
					return nil, err
				}
				if minWait == time.Duration(1<<62-1) {
					minWait = time.Hour
				}
				return nil, &types.ExhaustedError{RetryAfter: minWait, Reasons: reasons}
			}
			select {
			case <-time.After(minWait + 20*time.Millisecond):
			case <-ctx.Done():
				return nil, timedOut()
			}
			continue
		}

		text, tokens, hdr, err := r.callProvider(ctx, picked.cfg, msgs, req)
		var out *types.Response
		if err == nil {
			out = &types.Response{Text: text, Provider: picked.cfg.Name, Model: picked.cfg.Model, Tokens: tokens}
			if req.JSON {
				if j, ok := extractJSON(text); ok {
					out.JSON = j
				} else {
					err = errBadResponse("model did not return valid JSON")
				}
			} else if strings.TrimSpace(text) == "" {
				err = errBadResponse("empty response")
			}
		}
		picked.state.release(tokens)
		r.observe(ctx, picked, hdr, now)
		errs := 0
		if err != nil {
			errs = 1
		}
		_ = r.store.AddUsage(ctx, req.TenantID, picked.cfg.ID, dayOf(picked.cfg, now), 1, tokens, errs)
		if err == nil {
			picked.state.markSuccess()
			out.LatencyMS = r.now().Sub(start).Milliseconds()
			out.Attempts = attempts
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, timedOut()
		}
		f := r.classify(ctx, picked, err)
		attempts = append(attempts, types.Attempt{Provider: picked.cfg.Name, Error: err.Error(), Status: f.status, Kind: f.kind})
		failed[picked.cfg.ID] = true
	}
}

// ApplyPreset is a convenience wrapper that calls preset.Apply on p. Exposed so
// callers do not have to import pkg/preset directly when they only have a Router.
func ApplyPreset(p *types.ProviderConfig) error { return preset.Apply(p) }
