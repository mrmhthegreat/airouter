//go:build cgo

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/you/airouter/pkg/types"
)

func openSQL(t *testing.T, path string) *SQLStore {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSQLStoreEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	ctx := context.Background()

	st1 := openSQL(t, path)
	p := types.ProviderConfig{Name: "a", Kind: "openai", BaseURL: "http://x", Model: "a-m",
		Priority: 1, RPD: 2, ResetTZ: "Asia/Karachi"}
	f := false
	p.JSONMode = &f
	if err := st1.SaveProvider(ctx, &p); err != nil {
		t.Fatal(err)
	}
	t1 := types.Tenant{ID: "acme", Name: "acme", TZ: "UTC", DailyRequestLimit: 100}
	if err := st1.CreateTenant(ctx, t1, "hash-acme"); err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(locFor("Asia/Karachi")).Format("2006-01-02")
	if err := st1.AddUsage(ctx, "acme", p.ID, day, 1, 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := st1.AddUsage(ctx, "acme", p.ID, day, 1, 50, 0); err != nil {
		t.Fatal(err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}

	// "Restart the process" — open the same file fresh.
	st2 := openSQL(t, path)
	defer st2.Close()

	got, err := st2.TenantByID(ctx, "acme")
	if err != nil || got == nil || got.DailyRequestLimit != 100 {
		t.Fatalf("tenant round-trip: %v %v", got, err)
	}
	ps, _ := st2.ListProviders(ctx)
	if len(ps) != 1 || ps[0].ResetTZ != "Asia/Karachi" {
		t.Fatalf("provider round-trip: %+v", ps)
	}
	if ps[0].JSONMode == nil || *ps[0].JSONMode {
		t.Fatalf("JSONMode did not round-trip: %+v", ps[0])
	}
	r, tok, _ := st2.ProviderDayUsage(ctx, p.ID, day)
	if r != 2 || tok != 150 {
		t.Fatalf("usage did not persist: r=%d tok=%d", r, tok)
	}
}

// locFor is duplicated here (not imported from router) to avoid a circular dep.
func locFor(tz string) *time.Location {
	if l, err := time.LoadLocation(tz); err == nil {
		return l
	}
	return time.UTC
}

// Make sure errors.Is on ErrNotFound still works for code that compares against it.
func TestErrNotFoundIsComparable(t *testing.T) {
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Fatal("ErrNotFound identity broken")
	}
}
