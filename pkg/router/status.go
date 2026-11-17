package router

import (
	"context"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// ProviderStatus is the live, human-readable state of one provider. Returned
// by Status; surfaced via `airouter provider list` and `GET /v1/providers`.
type ProviderStatus struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	TenantID       string `json:"tenant_id,omitempty"`
	Kind           string `json:"kind"`
	Model          string `json:"model"`
	Preset         string `json:"preset,omitempty"`
	Category       string `json:"category"` // text | tts | stt
	Priority       int    `json:"priority"`
	State          string `json:"state"` // ok | disabled | no api key | cooling down | daily limit reached | paused: <reason>
	UsedToday      int    `json:"used_today"`
	TokensToday    int    `json:"tokens_today"`
	AudioSecToday  int    `json:"audio_seconds_today"` // ASD usage (TTS/STT only)
	RPD            int    `json:"rpd"`
	RPM            int    `json:"rpm"`
	TPM            int    `json:"tpm"`
	ASH            int    `json:"ash"` // audio seconds per hour cap (0 = no cap)
	ASD            int    `json:"asd"` // audio seconds per day cap (0 = no cap)
	LastMinute     int    `json:"requests_last_minute"`
	ResetTZ        string `json:"reset_tz"`
	ResetsInSec    int64  `json:"resets_in_seconds"`
	HasKey         bool   `json:"has_key"`
	RemainingToday *int   `json:"remaining_today,omitempty"`
}

// Status returns the live status of every provider. Pass a tenantID to see only
// that tenant's own providers plus shared ones; pass "" for the admin (all) view.
func (r *Router) Status(ctx context.Context, tenantID string) []ProviderStatus {
	now := r.now()
	var out []ProviderStatus
	for _, c := range r.snapshot(ctx) {
		if tenantID != "" && c.cfg.TenantID != "" && c.cfg.TenantID != tenantID {
			continue
		}
		cfg, st := c.cfg, c.state
		st.mu.Lock()
		r.hydrate(ctx, cfg, st, now)
		s := ProviderStatus{
			ID: cfg.ID, Name: cfg.Name, TenantID: cfg.TenantID, Kind: cfg.Kind, Model: cfg.Model,
			Preset: cfg.Preset, Category: cfg.Category, Priority: cfg.Priority,
			UsedToday:     st.requestsToday,
			TokensToday:   st.tokensToday,
			AudioSecToday: st.audioSecToday,
			RPD:           cfg.RPD,
			RPM:           cfg.RPM,
			TPM:           cfg.TPM,
			ASH:           cfg.ASH,
			ASD:           cfg.ASD,
			ResetTZ:       cfg.ResetTZ,
			ResetsInSec:   int64(untilReset(cfg, now).Seconds()),
			HasKey:        cfg.Kind == "ollama" || cfg.ResolveKey() != "",
			State:         "ok",
		}
		if s.Category == "" {
			s.Category = types.CategoryText
		}
		for _, h := range st.hits {
			if now.Sub(h.at) < time.Minute {
				s.LastMinute++
			}
		}
		if cfg.RPD > 0 {
			rem := maxInt(0, cfg.RPD-st.requestsToday)
			s.RemainingToday = &rem
		}
		switch {
		case cfg.Disabled:
			s.State = "disabled"
		case !s.HasKey:
			s.State = "no api key"
		case now.Before(st.deadUntil):
			s.State = "paused: " + st.deadReason
		case cfg.RPD > 0 && st.requestsToday >= cfg.RPD:
			s.State = "daily request limit reached"
		case cfg.ASD > 0 && st.audioSecToday >= cfg.ASD:
			s.State = "daily audio seconds limit reached"
		case now.Before(st.coolUntil):
			s.State = "cooling down"
		}
		st.mu.Unlock()
		out = append(out, s)
	}
	return out
}

// UsageRow is one row of a tenant's usage report.
type UsageRow struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Day            string `json:"day"`
	TenantRequests int    `json:"tenant_requests"`
	TenantTokens   int    `json:"tenant_tokens"`
	TenantErrors   int    `json:"tenant_errors"`
	AllRequests    int    `json:"all_tenants_requests"` // quota is shared, so this is what counts against the limit
	LimitPerDay    int    `json:"limit_per_day"`
	ResetsInSec    int64  `json:"resets_in_seconds"`
}

// UsageReport is the per-tenant usage report returned by Usage.
type UsageReport struct {
	Tenant        string     `json:"tenant"`
	RequestsToday int        `json:"requests_today"`
	TokensToday   int        `json:"tokens_today"`
	DailyLimit    int        `json:"tenant_daily_limit"`
	Providers     []UsageRow `json:"providers"`
}

// Usage reports a tenant's usage. Pass day="" for "today" (per provider, in
// that provider's timezone) or a specific YYYY-MM-DD for a historical day.
func (r *Router) Usage(ctx context.Context, tenantID, day string) (*UsageReport, error) {
	t, err := r.store.TenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, types.ErrUnknownTenant
	}
	now := r.now()
	rep := &UsageReport{Tenant: t.ID, DailyLimit: t.DailyRequestLimit}
	for _, c := range r.snapshot(ctx) {
		if c.cfg.TenantID != "" && c.cfg.TenantID != tenantID {
			continue
		}
		d := day
		if d == "" {
			d = dayOf(c.cfg, now)
		}
		tr, tt, te, err := r.store.TenantProviderUsage(ctx, tenantID, c.cfg.ID, d)
		if err != nil {
			return nil, err
		}
		ar, _, _ := r.store.ProviderDayUsage(ctx, c.cfg.ID, d)
		rep.RequestsToday += tr
		rep.TokensToday += tt
		rep.Providers = append(rep.Providers, UsageRow{
			Provider: c.cfg.Name, Model: c.cfg.Model, Day: d,
			TenantRequests: tr, TenantTokens: tt, TenantErrors: te,
			AllRequests: ar, LimitPerDay: c.cfg.RPD,
			ResetsInSec: int64(untilReset(c.cfg, now).Seconds()),
		})
	}
	return rep, nil
}
