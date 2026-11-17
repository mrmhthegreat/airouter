// Package store persists tenants, provider configs, daily-limit pauses and usage
// counters. Two implementations are shipped: an in-memory one (useful for tests
// and short-lived CLI runs) and a SQLite one (the default for the CLI/server).
//
// The interface is small on purpose: every method corresponds to something the
// router actually needs. If you want to add Postgres or Redis, implement this
// interface — nothing in pkg/router depends on SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Store is the persistence interface used by the router. Methods are grouped:
// tenants, providers, pauses (long cooldowns that survive restarts) and usage.
type Store interface {
	// Tenants -------------------------------------------------------------
	CreateTenant(ctx context.Context, t types.Tenant, keyHash string) error
	TenantByID(ctx context.Context, id string) (*types.Tenant, error) // nil, nil when missing
	TenantByKeyHash(ctx context.Context, hash string) (*types.Tenant, error)
	ListTenants(ctx context.Context) ([]types.Tenant, error)

	// Providers -----------------------------------------------------------
	SaveProvider(ctx context.Context, p *types.ProviderConfig) error // insert (ID==0) or update; sets p.ID
	ListProviders(ctx context.Context) ([]types.ProviderConfig, error)
	DeleteProvider(ctx context.Context, id int64) error

	// Pauses --------------------------------------------------------------
	// A pause survives restarts and separate CLI runs: a provider that hit its
	// daily limit stays skipped until reset, even if the process is restarted.
	SetPause(ctx context.Context, providerID int64, until time.Time, reason string) error // zero time clears
	GetPause(ctx context.Context, providerID int64) (until time.Time, reason string, err error)

	// Usage ---------------------------------------------------------------
	AddUsage(ctx context.Context, tenantID string, providerID int64, day string, requests, tokens, errs int) error
	ProviderDayUsage(ctx context.Context, providerID int64, day string) (requests, tokens int, err error) // all tenants combined
	TenantProviderUsage(ctx context.Context, tenantID string, providerID int64, day string) (requests, tokens, errs int, err error)
	TenantDayUsage(ctx context.Context, tenantID, day string) (requests int, err error)

	Close() error
}

// NewAPIKey returns a new random tenant key and its hash. Only the hash is stored.
// The key format is "air_<48 hex chars>" — the "air_" prefix lets users grep for
// leaked keys in logs.
func NewAPIKey() (key, hash string) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	key = "air_" + hex.EncodeToString(b)
	return key, HashKey(key)
}

// HashKey returns the SHA-256 hex hash of a tenant key. Hashing means a leaked
// DB dump does not reveal working API keys.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// ----- in-memory implementation --------------------------------------------

type usageKey struct {
	tenant string
	pid    int64
	day    string
}

type usageVal struct{ req, tok, errs int }

type pauseVal struct {
	until  time.Time
	reason string
}

// MemoryStore is an in-process implementation of Store. Use it for tests or for
// one-shot CLI runs where persistence is not needed.
type MemoryStore struct {
	mu        sync.Mutex
	tenants   map[string]types.Tenant
	hashes    map[string]string // hash -> tenant id
	providers map[int64]types.ProviderConfig
	nextID    int64
	usage     map[usageKey]usageVal
	pauses    map[int64]pauseVal
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tenants:   map[string]types.Tenant{},
		hashes:    map[string]string{},
		providers: map[int64]types.ProviderConfig{},
		usage:     map[usageKey]usageVal{},
		pauses:    map[int64]pauseVal{},
	}
}

func (m *MemoryStore) CreateTenant(_ context.Context, t types.Tenant, keyHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[t.ID]; ok {
		return fmt.Errorf("tenant %q already exists", t.ID)
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	m.tenants[t.ID], m.hashes[keyHash] = t, t.ID
	return nil
}

func (m *MemoryStore) TenantByID(_ context.Context, id string) (*types.Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tenants[id]; ok {
		return &t, nil
	}
	return nil, nil
}

func (m *MemoryStore) TenantByKeyHash(ctx context.Context, h string) (*types.Tenant, error) {
	m.mu.Lock()
	id, ok := m.hashes[h]
	m.mu.Unlock()
	if !ok {
		return nil, nil
	}
	return m.TenantByID(ctx, id)
}

func (m *MemoryStore) ListTenants(context.Context) ([]types.Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]types.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryStore) SaveProvider(_ context.Context, p *types.ProviderConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, q := range m.providers {
		if q.Name == p.Name && q.TenantID == p.TenantID && id != p.ID {
			return fmt.Errorf("provider %q already exists", p.Name)
		}
	}
	if p.ID == 0 {
		m.nextID++
		p.ID = m.nextID
	}
	m.providers[p.ID] = *p
	return nil
}

func (m *MemoryStore) ListProviders(context.Context) ([]types.ProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]types.ProviderConfig, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryStore) DeleteProvider(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.providers, id)
	return nil
}

func (m *MemoryStore) AddUsage(_ context.Context, tenant string, pid int64, day string, req, tok, errs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := usageKey{tenant, pid, day}
	v := m.usage[k]
	m.usage[k] = usageVal{v.req + req, v.tok + tok, v.errs + errs}
	return nil
}

func (m *MemoryStore) ProviderDayUsage(_ context.Context, pid int64, day string) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var r, t int
	for k, v := range m.usage {
		if k.pid == pid && k.day == day {
			r, t = r+v.req, t+v.tok
		}
	}
	return r, t, nil
}

func (m *MemoryStore) TenantProviderUsage(_ context.Context, tenant string, pid int64, day string) (int, int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.usage[usageKey{tenant, pid, day}]
	return v.req, v.tok, v.errs, nil
}

func (m *MemoryStore) TenantDayUsage(_ context.Context, tenant, day string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, v := range m.usage {
		if k.tenant == tenant && k.day == day {
			n += v.req
		}
	}
	return n, nil
}

func (m *MemoryStore) SetPause(_ context.Context, pid int64, until time.Time, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if until.IsZero() {
		delete(m.pauses, pid)
	} else {
		m.pauses[pid] = pauseVal{until, reason}
	}
	return nil
}

func (m *MemoryStore) GetPause(_ context.Context, pid int64) (time.Time, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.pauses[pid]
	return v.until, v.reason, nil
}

func (m *MemoryStore) Close() error { return nil }

// ErrNotFound is exported for callers that need to compare against it.
var ErrNotFound = errors.New("not found")
