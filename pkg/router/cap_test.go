package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

func newCapRouter(t *testing.T, wait time.Duration) *Router {
	r, err := New(Options{Store: store.NewMemoryStore(), MaxWait: wait, TenantPolicy: TenantPolicy{AllowCustomBaseURL: true, MaxProviders: 5}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The cap can be changed at any time, and applies to the next request.
func TestUpdateTenantCapAppliesImmediately(t *testing.T) {
	pool, _ := fakeProvider(t, alwaysOK("pool"))
	r := newCapRouter(t, 100*time.Millisecond)
	add(t, r, "pool", pool.URL, 1, 0, nil)
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := gen(r, types.Request{TenantID: "acme"}); err != nil {
		t.Fatal(err)
	}
	var tl *types.TenantLimitError
	if _, err := gen(r, types.Request{TenantID: "acme"}); !errors.As(err, &tl) {
		t.Fatalf("want cap error, got %v", err)
	}
	if _, err := r.UpdateTenantCap(ctx, "acme", 3, false, false); err != nil { // plan upgrade
		t.Fatal(err)
	}
	if _, err := gen(r, types.Request{TenantID: "acme"}); err != nil {
		t.Fatalf("raised cap should allow the request: %v", err)
	}
	if _, err := r.UpdateTenantCap(ctx, "nobody", 3, false, false); !errors.Is(err, types.ErrUnknownTenant) {
		t.Fatalf("unknown tenant: %v", err)
	}
	if _, err := r.UpdateTenantCap(ctx, "acme", -1, false, false); err == nil {
		t.Fatal("negative cap must be rejected")
	}
	if _, err := r.UpdateTenantCap(ctx, "acme", 3, false, true); err != nil { // disable
		t.Fatal(err)
	}
	if _, err := gen(r, types.Request{TenantID: "acme"}); !errors.Is(err, types.ErrTenantDisabled) {
		t.Fatalf("disabled tenant: %v", err)
	}
}

// With PoolOnlyCap, the tenant's own key is never counted against the cap, and
// the response says which kind of key answered.
func TestPoolOnlyCapIgnoresOwnKey(t *testing.T) {
	pool, _ := fakeProvider(t, alwaysOK("pool"))
	own, _ := fakeProvider(t, alwaysOK("own"))
	r := newCapRouter(t, 100*time.Millisecond)
	add(t, r, "pool", pool.URL, 5, 0, nil) // low priority number = tried first; own has 1
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 1, PoolOnlyCap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddTenantProvider(ctx, "acme", types.ProviderConfig{Name: "mine", Kind: "openai", BaseURL: own.URL, APIKey: "k", Model: "m", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ { // 4 own-key requests, cap is 1
		resp, err := gen(r, types.Request{TenantID: "acme"})
		if err != nil {
			t.Fatalf("own-key request %d blocked: %v", i, err)
		}
		if !resp.Own || resp.Provider != "mine" {
			t.Fatalf("want own key answer, got %+v", resp)
		}
	}
	// Pin the shared pool: this one counts, and the second is capped.
	resp, err := gen(r, types.Request{TenantID: "acme", Provider: "pool"})
	if err != nil || resp.Own {
		t.Fatalf("pool request: %+v %v", resp, err)
	}
	var tl *types.TenantLimitError
	if _, err := gen(r, types.Request{TenantID: "acme", Provider: "pool"}); !errors.As(err, &tl) {
		t.Fatalf("second pool request should hit the cap, got %v", err)
	}
	// Cap reached, but the tenant's own key still answers un-pinned requests.
	resp, err = gen(r, types.Request{TenantID: "acme"})
	if err != nil || !resp.Own {
		t.Fatalf("capped tenant should fall back to its own key: %+v %v", resp, err)
	}
}

// A capped tenant with no key of its own gets the cap error, not a pool answer.
func TestCappedTenantWithoutOwnKeyIsStopped(t *testing.T) {
	pool, _ := fakeProvider(t, alwaysOK("pool"))
	r := newCapRouter(t, 100*time.Millisecond)
	add(t, r, "pool", pool.URL, 1, 0, nil)
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "acme", DailyRequestLimit: 1, PoolOnlyCap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := gen(r, types.Request{TenantID: "acme"}); err != nil {
		t.Fatal(err)
	}
	var tl *types.TenantLimitError
	if _, err := gen(r, types.Request{TenantID: "acme"}); !errors.As(err, &tl) {
		t.Fatalf("want cap error, got %v", err)
	}
}

// A per-request MaxWait overrides the router default.
func TestRequestMaxWaitOverridesDefault(t *testing.T) {
	a, _ := fakeProvider(t, alwaysOK("x"))
	r := newCapRouter(t, 5*time.Second) // long default
	add(t, r, "a", a.URL, 1, 0, func(p *types.ProviderConfig) { p.RPM = 1 })
	if _, err := gen(r, types.Request{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var ex *types.ExhaustedError
	_, err := gen(r, types.Request{MaxWait: 50 * time.Millisecond})
	if !errors.As(err, &ex) {
		t.Fatalf("want ExhaustedError, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited %v despite MaxWait 50ms", time.Since(start))
	}
}
