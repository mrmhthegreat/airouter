package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Tenants hammer the router at the same time. Each has its own provider and key.
// A tenant must only ever be served by its own provider, with its own key, and
// must never be able to reach another tenant's provider, even by pinning its name.
func TestConcurrentTenantIsolation(t *testing.T) {
	const tenants, perTenant = 6, 40
	type seen struct {
		mu   sync.Mutex
		keys map[string]int
		hits atomic.Int64
	}
	var servers [tenants]*httptest.Server
	var sn [tenants]*seen
	for i := 0; i < tenants; i++ {
		i := i
		sn[i] = &seen{keys: map[string]int{}}
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sn[i].hits.Add(1)
			sn[i].mu.Lock()
			sn[i].keys[r.Header.Get("Authorization")]++
			sn[i].mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"answer-from-%d"}}],"usage":{"total_tokens":3}}`, i)
		}))
		t.Cleanup(servers[i].Close)
	}
	var poolHits atomic.Int64
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		poolHits.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pool"}}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(pool.Close)

	r, err := New(Options{Store: store.NewMemoryStore(), MaxWait: 10 * time.Second, TenantPolicy: TenantPolicy{AllowCustomBaseURL: true}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add(t, r, "shared-pool", pool.URL, 50, 0, nil)
	for i := 0; i < tenants; i++ {
		id := fmt.Sprintf("tenant%d", i)
		if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.AddTenantProvider(ctx, id, types.ProviderConfig{Name: fmt.Sprintf("mine-%d", i), Kind: "openai",
			BaseURL: servers[i].URL, APIKey: fmt.Sprintf("key-%d", i), Model: "m", Priority: 1, Workers: 8}); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var bad atomic.Int64
	// A tenant's own model has at most 8 workers (maxTenantWorkers). Keep each
	// tenant under that, or the extra requests would (correctly) spill over into
	// the shared pool, which is a different behaviour from what is tested here.
	var slots [tenants]chan struct{}
	for i := range slots {
		slots[i] = make(chan struct{}, 6)
	}
	for i := 0; i < tenants; i++ {
		for k := 0; k < perTenant; k++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				slots[i] <- struct{}{}
				defer func() { <-slots[i] }()
				resp, err := r.Generate(ctx, types.Request{TenantID: fmt.Sprintf("tenant%d", i), Prompt: "hi"})
				if err != nil || resp.Text != fmt.Sprintf("answer-from-%d", i) || !resp.Own {
					bad.Add(1)
					t.Errorf("tenant%d got %v / %+v", i, err, resp)
				}
			}(i)
		}
	}
	wg.Wait()
	if bad.Load() > 0 {
		t.FailNow()
	}
	for i := 0; i < tenants; i++ {
		if got := sn[i].hits.Load(); got != perTenant {
			t.Errorf("provider %d got %d requests, want %d", i, got, perTenant)
		}
		for k := range sn[i].keys {
			if k != fmt.Sprintf("Bearer key-%d", i) {
				t.Errorf("provider %d was called with someone else's key %q", i, k)
			}
		}
	}
	if poolHits.Load() != 0 {
		t.Errorf("shared pool was used %d times although every tenant has its own key", poolHits.Load())
	}

	// tenant0 tries to use tenant1's provider by naming it
	before := sn[1].hits.Load()
	resp, err := r.Generate(ctx, types.Request{TenantID: "tenant0", Prompt: "hi", Provider: "mine-1"})
	if err == nil && strings.Contains(resp.Text, "answer-from-1") {
		t.Fatal("tenant0 reached tenant1's provider by name")
	}
	if sn[1].hits.Load() != before {
		t.Fatal("tenant1's provider was called on behalf of tenant0")
	}
	// ...and cannot see, change or delete it through the self-service calls
	own0, shared0 := r.ListTenantProviders(ctx, "tenant0")
	for _, p := range own0 {
		if p.Name == "mine-1" {
			t.Fatal("tenant0 sees tenant1's provider in its own list")
		}
	}
	for _, p := range shared0 {
		if p.Name == "mine-1" {
			t.Fatal("tenant1's private provider is listed as shared")
		}
	}
	var other int64
	for _, p := range r.Status(ctx, "tenant1") {
		if p.Name == "mine-1" {
			other = p.ID
		}
	}
	if other == 0 {
		t.Fatal("test setup: provider id not found")
	}
	if err := r.DeleteTenantProvider(ctx, "tenant0", other); err == nil {
		t.Fatal("tenant0 deleted tenant1's provider")
	}
	if err := r.SetTenantProviderKey(ctx, "tenant0", other, "stolen"); err == nil {
		t.Fatal("tenant0 replaced tenant1's key")
	}
}
