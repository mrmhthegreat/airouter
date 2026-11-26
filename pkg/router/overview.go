package router

import (
	"context"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// CapStatus says how much of a tenant's daily request cap is used.
type CapStatus struct {
	Limit       int   `json:"limit"`               // 0 = unlimited
	Used        int   `json:"used"`                // today toward the cap. Normal cap: every attempt, failed too. Pool-only cap: only requests the shared pool served successfully
	Remaining   int   `json:"remaining"`           // -1 when unlimited
	PoolOnly    bool  `json:"pool_only"`           // own-key requests are free and never counted
	Reached     bool  `json:"reached"`             // true when the cap is used up
	ResetsInSec int64 `json:"resets_in_seconds"`   // until the tenant's next midnight
	OwnToday    int   `json:"own_requests_today"`  // answered by the tenant's own keys (free under a pool-only cap)
	PoolToday   int   `json:"pool_requests_today"` // answered by the shared pool
}

// TenantOverview is everything an app's "AI" page needs about one tenant, in a
// single call: the cap, today's usage per model, and the live state and reset
// time of every model that serves the tenant.
type TenantOverview struct {
	Tenant     types.Tenant     `json:"tenant"`
	Cap        CapStatus        `json:"cap"`
	Usage      *UsageReport     `json:"usage"`
	Own        []ProviderStatus `json:"own"`    // the tenant's own models (full live status)
	Shared     []SharedModel    `json:"shared"` // shared-pool models (name, state, reset); keys and other tenants' numbers are never shown
	ServerTime time.Time        `json:"server_time"`
}

// TenantOverview gathers cap, usage and provider status for one tenant.
func (r *Router) TenantOverview(ctx context.Context, tenantID string) (*TenantOverview, error) {
	if tenantID == "" {
		tenantID = types.DefaultTenantID
	}
	t, err := r.store.TenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, types.ErrUnknownTenant
	}
	now := r.now()
	usage, err := r.Usage(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	own, shared := r.ListTenantProviders(ctx, tenantID)
	// Shared models: add the reset time so the page can say "back in 2h".
	reset := map[string]int64{}
	for _, s := range r.Status(ctx, tenantID) {
		if s.TenantID == "" {
			reset[s.Name] = s.ResetsInSec
		}
	}
	for i := range shared {
		shared[i].ResetsInSec = reset[shared[i].Name]
	}

	loc := locFor(t.TZ)
	day := now.In(loc).Format("2006-01-02")
	total, _ := r.store.TenantDayUsage(ctx, tenantID, day)
	pool, _ := r.store.TenantPoolDayUsage(ctx, tenantID, day)
	// Requests the tenant's OWN models served successfully today, from the per-model
	// usage rows (the stored totals include failed attempts, the pool count does not).
	ownNames := map[string]bool{}
	for _, st := range r.Status(ctx, tenantID) {
		if st.TenantID != "" {
			ownNames[st.Name] = true
		}
	}
	ownServed := 0
	for _, u := range usage.Providers {
		if ownNames[u.Provider] {
			if n := u.TenantRequests - u.TenantErrors; n > 0 {
				ownServed += n
			}
		}
	}
	c := CapStatus{Limit: t.DailyRequestLimit, PoolOnly: t.PoolOnlyCap, Remaining: -1, PoolToday: pool, OwnToday: ownServed}
	c.Used = total // a normal cap counts every attempt, failed ones included
	if t.PoolOnlyCap {
		c.Used = pool
	}
	if t.DailyRequestLimit > 0 {
		c.Remaining = t.DailyRequestLimit - c.Used
		if c.Remaining < 0 {
			c.Remaining = 0
		}
		c.Reached = c.Used >= t.DailyRequestLimit
	}
	n := in(now, loc)
	c.ResetsInSec = int64(time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, loc).Sub(n).Seconds())
	return &TenantOverview{Tenant: *t, Cap: c, Usage: usage, Own: own, Shared: shared, ServerTime: now.UTC()}, nil
}

func in(t time.Time, loc *time.Location) time.Time { return t.In(loc) }
