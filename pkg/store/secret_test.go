package store

import (
	"context"
	"strings"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

func TestProviderKeysAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	raw := NewMemoryStore()
	enc, err := WithEncryption(raw, []byte("master-1"))
	if err != nil {
		t.Fatal(err)
	}
	p := &types.ProviderConfig{Name: "g", Kind: "openai", BaseURL: "http://x", Model: "m", APIKey: "gsk_supersecret"}
	if err := enc.SaveProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.APIKey != "gsk_supersecret" || p.ID == 0 {
		t.Fatalf("caller's struct must keep plaintext + get ID: %+v", p)
	}
	stored, _ := raw.ListProviders(ctx)
	if strings.Contains(stored[0].APIKey, "supersecret") || !strings.HasPrefix(stored[0].APIKey, "enc:v1:") {
		t.Fatalf("not encrypted at rest: %q", stored[0].APIKey)
	}
	got, err := enc.ListProviders(ctx)
	if err != nil || got[0].APIKey != "gsk_supersecret" {
		t.Fatalf("round trip: %v %v", got, err)
	}
	// Wrong master secret must fail loudly, not silently send garbage upstream.
	other, _ := WithEncryption(raw, []byte("master-2"))
	if _, err := other.ListProviders(ctx); err == nil {
		t.Fatal("wrong secret should error")
	}
}

func TestEncryptExistingUpgradesPlaintextRows(t *testing.T) {
	ctx := context.Background()
	raw := NewMemoryStore()
	_ = raw.SaveProvider(ctx, &types.ProviderConfig{Name: "old", Kind: "openai", BaseURL: "http://x", Model: "m", APIKey: "plain-key"})
	enc, _ := WithEncryption(raw, []byte("s"))
	n, err := enc.EncryptExisting(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	stored, _ := raw.ListProviders(ctx)
	if strings.Contains(stored[0].APIKey, "plain-key") {
		t.Fatal("still plaintext")
	}
	if n, _ := enc.EncryptExisting(ctx); n != 0 {
		t.Fatalf("idempotent: %d", n)
	}
}

func TestRotateTenantKey(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	k1, h1 := NewAPIKey()
	_ = m.CreateTenant(ctx, types.Tenant{ID: "t", Name: "t", TZ: "UTC"}, h1)
	_, h2 := NewAPIKey()
	if err := m.UpdateTenantKeyHash(ctx, "t", h2); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.TenantByKeyHash(ctx, HashKey(k1)); got != nil {
		t.Fatal("old key must stop working")
	}
	if got, _ := m.TenantByKeyHash(ctx, h2); got == nil {
		t.Fatal("new key must work")
	}
}
