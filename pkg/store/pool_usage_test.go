package store

import (
	"context"
	"testing"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Only successful shared-pool requests count toward a pool-only cap: failed
// attempts and the tenant's own providers do not.
func TestTenantPoolDayUsageCountsOnlySuccessfulPoolRequests(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	pool := &types.ProviderConfig{Name: "pool", Category: types.CategoryText}
	own := &types.ProviderConfig{Name: "own", Category: types.CategoryText, TenantID: "t1"}
	if err := m.SaveProvider(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveProvider(ctx, own); err != nil {
		t.Fatal(err)
	}
	_ = m.AddUsage(ctx, "t1", pool.ID, "2026-11-24", 5, 0, 2) // 5 attempts, 2 failed -> 3 served
	_ = m.AddUsage(ctx, "t1", own.ID, "2026-11-24", 9, 0, 0)  // own key: never counted
	got, err := m.TenantPoolDayUsage(ctx, "t1", "2026-11-24")
	if err != nil || got != 3 {
		t.Fatalf("got %d, %v; want 3", got, err)
	}
	if n, _ := m.TenantPoolDayUsage(ctx, "t1", "2026-11-25"); n != 0 {
		t.Fatalf("other day should be 0, got %d", n)
	}
}
