package router

import (
	"context"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// capGate applies a tenant's daily request cap. Generate, Speak and Transcribe
// all call it so the three behave the same.
//
//   - Normal cap: when the cap is used up it returns (false, err) and the
//     request must stop with that *types.TenantLimitError.
//   - Pool-only cap (Tenant.PoolOnlyCap): only shared-pool answers count. When
//     the cap is used up it returns ownOnly=true and capErr set; the caller
//     keeps going but may only use the tenant's own providers (see onlyOwn).
func (r *Router) capGate(ctx context.Context, tenant *types.Tenant, start time.Time) (ownOnly bool, capErr *types.TenantLimitError, err error) {
	if tenant.DailyRequestLimit <= 0 {
		return false, nil, nil
	}
	loc := locFor(tenant.TZ)
	day := start.In(loc).Format("2006-01-02")
	usedFn := r.store.TenantDayUsage
	if tenant.PoolOnlyCap {
		usedFn = r.store.TenantPoolDayUsage
	}
	used, uerr := usedFn(ctx, tenant.ID, day)
	if uerr != nil || used < tenant.DailyRequestLimit {
		return false, nil, nil
	}
	now := start.In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	capErr = &types.TenantLimitError{RetryAfter: next.Sub(now)}
	if !tenant.PoolOnlyCap {
		return false, capErr, capErr
	}
	return true, capErr, nil
}

// onlyOwn keeps only the tenant's own providers.
func onlyOwn(cands []candidate) []candidate {
	var own []candidate
	for _, c := range cands {
		if c.cfg.TenantID != "" {
			own = append(own, c)
		}
	}
	return own
}
