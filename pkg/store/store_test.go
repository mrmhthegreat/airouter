package store

import (
	"context"
	"testing"
	"time"

	"github.com/you/airouter/pkg/types"
)

func TestMemoryStoreRoundTrip(t *testing.T) {
	st := NewMemoryStore()
	ctx := context.Background()

	// Tenant round-trip + key hash lookup.
	t1 := types.Tenant{ID: "acme", Name: "Acme", TZ: "UTC", DailyRequestLimit: 100}
	if err := st.CreateTenant(ctx, t1, "hash-1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.TenantByID(ctx, "acme")
	if err != nil || got == nil || got.ID != "acme" || got.DailyRequestLimit != 100 {
		t.Fatalf("TenantByID: %v %v", got, err)
	}
	got, err = st.TenantByKeyHash(ctx, "hash-1")
	if err != nil || got == nil || got.ID != "acme" {
		t.Fatalf("TenantByKeyHash: %v %v", got, err)
	}

	// Provider round-trip (insert → ID assigned; update keeps ID).
	p := types.ProviderConfig{Name: "groq-a", Kind: "openai", BaseURL: "http://x", Model: "m"}
	if err := st.SaveProvider(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("ID not assigned")
	}
	p.RPD = 1000
	if err := st.SaveProvider(ctx, &p); err != nil {
		t.Fatal(err)
	}
	ps, _ := st.ListProviders(ctx)
	if len(ps) != 1 || ps[0].RPD != 1000 {
		t.Fatalf("update lost: %+v", ps)
	}

	// Usage accumulates.
	day := time.Now().UTC().Format("2006-01-02")
	if err := st.AddUsage(ctx, "acme", p.ID, day, 1, 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.AddUsage(ctx, "acme", p.ID, day, 2, 50, 1); err != nil {
		t.Fatal(err)
	}
	r, tok, _ := st.ProviderDayUsage(ctx, p.ID, day)
	if r != 3 || tok != 150 {
		t.Fatalf("ProviderDayUsage: r=%d tok=%d", r, tok)
	}
	tr, tt, te, _ := st.TenantProviderUsage(ctx, "acme", p.ID, day)
	if tr != 3 || tt != 150 || te != 1 {
		t.Fatalf("TenantProviderUsage: r=%d tok=%d errs=%d", tr, tt, te)
	}

	// Pause survives a Set/Get round-trip.
	until := time.Now().Add(time.Hour)
	if err := st.SetPause(ctx, p.ID, until, "daily limit"); err != nil {
		t.Fatal(err)
	}
	gotUntil, why, _ := st.GetPause(ctx, p.ID)
	if !gotUntil.Equal(until) || why != "daily limit" {
		t.Fatalf("pause: %v %q", gotUntil, why)
	}
	// Clearing it works.
	if err := st.SetPause(ctx, p.ID, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	gotUntil, _, _ = st.GetPause(ctx, p.ID)
	if !gotUntil.IsZero() {
		t.Fatalf("pause not cleared: %v", gotUntil)
	}
}
