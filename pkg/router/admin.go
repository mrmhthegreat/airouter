package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// CreateTenant validates and stores a new tenant, returning the freshly
// generated API key (shown once) alongside the tenant.
func (r *Router) CreateTenant(ctx context.Context, t types.Tenant) (types.Tenant, string, error) {
	if strings.TrimSpace(t.ID) == "" {
		return t, "", errors.New("tenant id is required")
	}
	if t.TZ == "" {
		t.TZ = "UTC"
	}
	if _, err := time.LoadLocation(t.TZ); err != nil {
		return t, "", fmt.Errorf("unknown tz %q", t.TZ)
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	key, hash := store.NewAPIKey()
	t.CreatedAt = r.now().UTC()
	if err := r.store.CreateTenant(ctx, t, hash); err != nil {
		return t, "", err
	}
	return t, key, nil
}

// Authenticate resolves a tenant from an API key. Returns ErrUnauthorized when
// the key does not match anything, ErrTenantDisabled when the tenant is off.
func (r *Router) Authenticate(ctx context.Context, key string) (*types.Tenant, error) {
	t, err := r.store.TenantByKeyHash(ctx, store.HashKey(key))
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, types.ErrUnauthorized
	}
	if t.Disabled {
		return nil, types.ErrTenantDisabled
	}
	return t, nil
}

// SaveProvider validates and stores a provider (applying a preset and the
// used_today_hint if given). Editing an existing provider lifts any pause on
// it (a new key or model should be tried immediately).
func (r *Router) SaveProvider(ctx context.Context, p *types.ProviderConfig) error {
	if p.Preset != "" {
		if err := preset.Apply(p); err != nil {
			return err
		}
	}
	if err := p.Validate(); err != nil {
		return err
	}
	isNew := p.ID == 0
	if err := r.store.SaveProvider(ctx, p); err != nil {
		return err
	}
	if !isNew {
		_ = r.ClearPause(ctx, p.ID)
	}
	if isNew && p.UsedTodayHint > 0 {
		_ = r.store.AddUsage(ctx, "_seed", p.ID, dayOf(*p, r.now()), p.UsedTodayHint, 0, 0)
	}
	return r.Refresh(ctx)
}

// DeleteProvider removes a provider. Usage history is kept (so reports for
// past days stay accurate); the live state is dropped on next Refresh.
func (r *Router) DeleteProvider(ctx context.Context, id int64) error {
	if err := r.store.DeleteProvider(ctx, id); err != nil {
		return err
	}
	return r.Refresh(ctx)
}

// ClearPause lifts any daily-limit / bad-key pause on a provider. Use this
// after you fix a key, fix a model id, or just want to retry a provider now.
func (r *Router) ClearPause(ctx context.Context, id int64) error {
	if err := r.store.SetPause(ctx, id, time.Time{}, ""); err != nil {
		return err
	}
	r.mu.RLock()
	st := r.states[id]
	r.mu.RUnlock()
	if st != nil {
		st.mu.Lock()
		st.deadUntil, st.deadReason, st.coolUntil, st.bad400 = time.Time{}, "", time.Time{}, 0
		st.mu.Unlock()
	}
	return nil
}

// SetDisabled enables or disables a provider. Re-enabling clears any pause.
func (r *Router) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	ps, err := r.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.ID == id {
			p.Disabled = disabled
			if err := r.store.SaveProvider(ctx, &p); err != nil {
				return err
			}
			if !disabled {
				_ = r.ClearPause(ctx, id)
			}
			return r.Refresh(ctx)
		}
	}
	return types.ErrNotFound
}

// BootstrapProviders imports entries from the preset free pool into the store,
// skipping entries whose env var is unset (so providers with no key don't get
// added with a broken state) and entries whose category is not in `categories`
// (defaults to chat-capable text models only, since airouter's request shape
// only works for those). Returns the list of names that were imported and the
// list that were skipped (with reasons).
//
// This is the "one command gives you a working pool" path. Set overwrite=true
// to update existing same-name entries in place. Pass nil for categories to
// import every entry (useful for seeing what's available — most non-text
// entries will fail at call time because they need different request shapes).
func (r *Router) BootstrapProviders(ctx context.Context, overwrite bool, onlyPreset string, categories []string) (added, skipped []string, err error) {
	// Three cases, deliberately distinct so HTTP ?category=all can actually
	// mean "all" instead of falling into the default-text branch:
	//   categories == nil          → default = text only (caller said nothing)
	//   categories == []string{}   → all categories (caller explicitly said "all")
	//   categories == ["text",...] → only those
	var pool []preset.FreeModel
	if categories == nil {
		pool = preset.FreePoolByCategory(preset.DefaultBootstrapCategories)
	} else {
		pool = preset.FreePoolByCategory(categories) // empty slice → all
	}
	existing, err := r.store.ListProviders(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]types.ProviderConfig{}
	for _, p := range existing {
		byName[p.TenantID+"|"+p.Name] = p
	}
	for _, m := range pool {
		if onlyPreset != "" && m.Preset != onlyPreset {
			continue
		}
		cfg := m.ToProviderConfig()
		// Skip providers that need an env var the user has not set, unless
		// they're Ollama (no key needed). This keeps bootstrap from seeding
		// a dozen broken entries on a fresh machine.
		if cfg.Kind != "ollama" && cfg.APIKeyEnv != "" && cfg.ResolveKey() == "" {
			skipped = append(skipped, cfg.Name+" (set $"+cfg.APIKeyEnv+")")
			continue
		}
		key := cfg.TenantID + "|" + cfg.Name
		if prev, ok := byName[key]; ok && !overwrite {
			skipped = append(skipped, cfg.Name+" (already exists; use -overwrite)")
			continue
		} else if ok {
			cfg.ID = prev.ID
		}
		if err := r.SaveProvider(ctx, &cfg); err != nil {
			skipped = append(skipped, cfg.Name+" ("+err.Error()+")")
			continue
		}
		added = append(added, cfg.Name)
	}
	return added, skipped, nil
}
