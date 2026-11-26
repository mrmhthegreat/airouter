//go:build cgo

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Both stores must behave the same for voices, tenant caps and the pool-only usage count.
func TestVoiceAndCapStoresBehaveTheSame(t *testing.T) {
	sqlSt := openSQL(t, filepath.Join(t.TempDir(), "v.db"))
	for name, st := range map[string]Store{"memory": NewMemoryStore(), "sql": sqlSt} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// ---- voices
			if v, err := st.VoiceByID(ctx, "nope"); err != nil || v != nil {
				t.Fatalf("missing voice must be (nil, nil): %v %v", v, err)
			}
			for _, rec := range []types.VoiceRecord{
				{VoiceID: "v1", TenantID: "alice", ProviderID: 3, Name: "A1"},
				{VoiceID: "v2", TenantID: "bob", ProviderID: 3, Name: "B1", Description: "d"},
				{VoiceID: "v3", TenantID: "alice", ProviderID: 4, Name: "A2"},
			} {
				if err := st.SaveVoice(ctx, rec); err != nil {
					t.Fatal(err)
				}
			}
			if al, _ := st.ListVoices(ctx, "alice"); len(al) != 2 {
				t.Fatalf("alice has %d voices", len(al))
			}
			if all, _ := st.ListVoices(ctx, ""); len(all) != 3 {
				t.Fatalf("all = %d", len(all))
			}
			_ = st.SaveVoice(ctx, types.VoiceRecord{VoiceID: "v1", TenantID: "alice", ProviderID: 3, Name: "Renamed"}) // upsert
			if v, _ := st.VoiceByID(ctx, "v1"); v == nil || v.Name != "Renamed" || v.CreatedAt.IsZero() {
				t.Fatalf("upsert failed: %+v", v)
			}
			if v, _ := st.VoiceByID(ctx, "v2"); v.TenantID != "bob" || v.Description != "d" || v.ProviderID != 3 {
				t.Fatalf("fields lost: %+v", v)
			}
			if err := st.DeleteVoice(ctx, "v2"); err != nil {
				t.Fatal(err)
			}
			if v, _ := st.VoiceByID(ctx, "v2"); v != nil {
				t.Fatal("not deleted")
			}
			if err := st.DeleteVoice(ctx, "v2"); err != nil {
				t.Fatalf("deleting twice must not fail: %v", err)
			}

			// ---- tenant cap fields survive a round trip
			if err := st.CreateTenant(ctx, types.Tenant{ID: "acme", TZ: "UTC", DailyRequestLimit: 7, PoolOnlyCap: true}, "hash"); err != nil {
				t.Fatal(err)
			}
			got, err := st.TenantByID(ctx, "acme")
			if err != nil || got == nil || !got.PoolOnlyCap || got.DailyRequestLimit != 7 {
				t.Fatalf("pool_only_cap lost: %+v %v", got, err)
			}
			if err := st.UpdateTenantCap(ctx, "acme", 9, false, true); err != nil {
				t.Fatal(err)
			}
			got, _ = st.TenantByID(ctx, "acme")
			if got.PoolOnlyCap || got.DailyRequestLimit != 9 || !got.Disabled {
				t.Fatalf("UpdateTenantCap: %+v", got)
			}
			if err := st.UpdateTenantCap(ctx, "ghost", 1, false, false); err == nil {
				t.Fatal("updating an unknown tenant must fail")
			}

			// ---- pool-only usage counts only shared (tenant_id "") providers
			shared := &types.ProviderConfig{Name: "pool", Kind: "openai", BaseURL: "http://x", Model: "m"}
			own := &types.ProviderConfig{Name: "mine", Kind: "openai", BaseURL: "http://y", Model: "m", TenantID: "acme"}
			if err := st.SaveProvider(ctx, shared); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveProvider(ctx, own); err != nil {
				t.Fatal(err)
			}
			day := time.Now().UTC().Format("2006-01-02")
			if err := st.AddUsage(ctx, "acme", shared.ID, day, 2, 10, 0); err != nil {
				t.Fatal(err)
			}
			if err := st.AddUsage(ctx, "acme", own.ID, day, 5, 10, 0); err != nil {
				t.Fatal(err)
			}
			if all, _ := st.TenantDayUsage(ctx, "acme", day); all != 7 {
				t.Fatalf("total usage = %d, want 7", all)
			}
			if pool, _ := st.TenantPoolDayUsage(ctx, "acme", day); pool != 2 {
				t.Fatalf("pool usage = %d, want 2 (own-key requests must not count)", pool)
			}
		})
	}
}

// The pool count is "requests served": failed attempts are subtracted. This runs
// on the real SQL query (SUM(CASE ...)) as well as on the in-memory store.
func TestPoolUsageSubtractsFailedAttemptsOnBothStores(t *testing.T) {
	sqlSt := openSQL(t, filepath.Join(t.TempDir(), "pool.db"))
	for name, st := range map[string]Store{"memory": NewMemoryStore(), "sql": sqlSt} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			shared1 := &types.ProviderConfig{Name: "pool1", Kind: "openai", BaseURL: "http://a", Model: "m"}
			shared2 := &types.ProviderConfig{Name: "pool2", Kind: "openai", BaseURL: "http://b", Model: "m"}
			own := &types.ProviderConfig{Name: "mine", Kind: "openai", BaseURL: "http://c", Model: "m", TenantID: "acme"}
			for _, p := range []*types.ProviderConfig{shared1, shared2, own} {
				if err := st.SaveProvider(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			day := "2026-11-24"
			_ = st.AddUsage(ctx, "acme", shared1.ID, day, 5, 0, 2) // 3 served
			_ = st.AddUsage(ctx, "acme", shared2.ID, day, 4, 0, 4) // outage: 0 served
			_ = st.AddUsage(ctx, "acme", own.ID, day, 9, 0, 0)     // own key: never counted
			_ = st.AddUsage(ctx, "other", shared1.ID, day, 7, 0, 0)
			_ = st.AddUsage(ctx, "acme", shared1.ID, "2026-11-25", 6, 0, 0)
			if got, err := st.TenantPoolDayUsage(ctx, "acme", day); err != nil || got != 3 {
				t.Fatalf("pool served = %d (%v), want 3", got, err)
			}
			// the normal cap still counts every attempt, own models included
			if got, _ := st.TenantDayUsage(ctx, "acme", day); got != 18 {
				t.Fatalf("all attempts = %d, want 18", got)
			}
			if got, _ := st.TenantPoolDayUsage(ctx, "nobody", day); got != 0 {
				t.Fatalf("unknown tenant = %d", got)
			}
		})
	}
}

// A database made before voices could be shared has no `shared` column. Opening it
// must add the column and keep the existing rows (private).
func TestOldVoicesTableIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE voices(
	  voice_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, provider_id INTEGER NOT NULL,
	  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
	  INSERT INTO voices VALUES('old1','default',1,'Old','', '2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewSQLStore(db)
	if err != nil {
		t.Fatalf("opening an old database failed: %v", err)
	}
	ctx := context.Background()
	v, err := st.VoiceByID(ctx, "old1")
	if err != nil || v == nil || v.Shared || v.Name != "Old" {
		t.Fatalf("old row lost or shared by accident: %+v %v", v, err)
	}
	if err := st.SetVoiceShared(ctx, "old1", true); err != nil {
		t.Fatal(err)
	}
	if v, _ = st.VoiceByID(ctx, "old1"); !v.Shared {
		t.Fatal("share did not stick")
	}
	if err := st.SetVoiceShared(ctx, "missing", true); err == nil {
		t.Fatal("sharing an unknown voice must fail")
	}
}
