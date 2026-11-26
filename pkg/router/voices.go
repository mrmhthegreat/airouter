package router

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/elevenlabs"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

const (
	maxVoiceSamples     = 25
	maxVoiceSampleBytes = 12 << 20 // per recording
	maxVoiceTotalBytes  = 50 << 20
)

// ListVoicesOptions narrows ListVoices.
type ListVoicesOptions struct {
	Provider string // only this ElevenLabs provider (by name)
	Search   string // case-insensitive match on name, description, category or labels
}

// elProviders returns the ElevenLabs providers this tenant may use, its own
// first, then the shared ones by priority.
func (r *Router) elProviders(ctx context.Context, tenantID, name string, ownOnly bool) []candidate {
	var out []candidate
	for _, c := range r.snapshot(ctx) {
		if !isElevenLabs(c.cfg) || c.cfg.Disabled || !c.cfg.HasKey() {
			continue
		}
		if c.cfg.TenantID != "" && c.cfg.TenantID != tenantID {
			continue
		}
		if ownOnly && c.cfg.TenantID == "" {
			continue
		}
		if name != "" && c.cfg.Name != name {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].cfg, out[j].cfg
		if (a.TenantID != "") != (b.TenantID != "") {
			return a.TenantID != ""
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
	return out
}

func (r *Router) activeTenant(ctx context.Context, id string) (*types.Tenant, error) {
	if id == "" {
		id = types.DefaultTenantID
	}
	t, err := r.store.TenantByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, types.ErrUnknownTenant
	}
	if t.Disabled {
		return nil, types.ErrTenantDisabled
	}
	return t, nil
}

// ListVoices lists the voices a tenant can speak with, across every ElevenLabs
// provider it can use: the premade and library voices, the voices of its own
// ElevenLabs account, and its own clones. Clones of other tenants are never listed.
func (r *Router) ListVoices(ctx context.Context, tenantID string, o ListVoicesOptions) ([]types.Voice, error) {
	t, err := r.activeTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	provs := r.elProviders(ctx, t.ID, o.Provider, false)
	if len(provs) == 0 {
		return nil, errors.New("airouter: no ElevenLabs provider configured (provider add -preset elevenlabs -model eleven_multilingual_v2 -category tts)")
	}
	recs, _ := r.store.ListVoices(ctx, "")
	mine, shared := map[string]bool{}, map[string]bool{}
	for i := range recs {
		if recs[i].TenantID == t.ID {
			mine[recs[i].VoiceID] = true
		}
		if sharedRec(&recs[i]) {
			shared[recs[i].VoiceID] = true
		}
	}
	search := strings.ToLower(strings.TrimSpace(o.Search))
	seen := map[string]bool{}
	var out []types.Voice
	var lastErr error
	ok := 0
	for _, c := range provs {
		vs, err := r.visibleVoices(ctx, c.cfg, t.ID)
		if err != nil {
			lastErr = err
			continue
		}
		ok++
		for _, v := range vs {
			if seen[v.VoiceID] {
				continue
			}
			if search != "" && !voiceMatches(v, search) {
				continue
			}
			seen[v.VoiceID] = true
			out = append(out, types.Voice{
				ID: v.VoiceID, Name: v.Name, Category: v.Category, Description: v.Description,
				PreviewURL: v.PreviewURL, Labels: v.Labels, Provider: c.cfg.Name,
				Own: c.cfg.TenantID != "", Cloned: mine[v.VoiceID], Shared: shared[v.VoiceID],
			})
		}
	}
	if ok == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func voiceMatches(v elevenlabs.Voice, q string) bool {
	if strings.Contains(strings.ToLower(v.Name+" "+v.Description+" "+v.Category), q) {
		return true
	}
	for k, val := range v.Labels {
		if strings.Contains(strings.ToLower(k+" "+val), q) {
			return true
		}
	}
	return false
}

// CloneVoice creates an Instant Voice Clone from the samples and remembers who
// owns it. The caller must confirm consent. A tenant clones in its OWN
// ElevenLabs account; cloning in the shared account needs
// TenantPolicy.AllowSharedVoiceClone (the default tenant is always allowed).
func (r *Router) CloneVoice(ctx context.Context, req types.CloneVoiceRequest) (*types.Voice, error) {
	t, err := r.activeTenant(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if !req.ConsentConfirmed {
		return nil, types.ErrConsentRequired
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return nil, fmt.Errorf("%w: voice name is required (max 100 characters)", types.ErrBadRequest)
	}
	if len(req.Samples) == 0 || len(req.Samples) > maxVoiceSamples {
		return nil, fmt.Errorf("%w: send 1 to %d audio samples", types.ErrBadRequest, maxVoiceSamples)
	}
	total := 0
	for _, s := range req.Samples {
		if len(s.Data) == 0 {
			return nil, fmt.Errorf("%w: sample %q is empty", types.ErrBadRequest, s.Filename)
		}
		if len(s.Data) > maxVoiceSampleBytes {
			return nil, fmt.Errorf("%w: sample %q is larger than %d MB", types.ErrBadRequest, s.Filename, maxVoiceSampleBytes>>20)
		}
		total += len(s.Data)
	}
	if total > maxVoiceTotalBytes {
		return nil, fmt.Errorf("%w: samples are larger than %d MB in total", types.ErrBadRequest, maxVoiceTotalBytes>>20)
	}
	maxVoices := r.tpolicy.MaxClonedVoices
	if maxVoices <= 0 {
		maxVoices = 10
	}
	if mine, _ := r.store.ListVoices(ctx, t.ID); len(mine) >= maxVoices {
		return nil, fmt.Errorf("%w: at most %d cloned voices per tenant (delete one first)", types.ErrBadRequest, maxVoices)
	}
	ownOnly := t.ID != types.DefaultTenantID && !r.tpolicy.AllowSharedVoiceClone
	provs := r.elProviders(ctx, t.ID, req.Provider, ownOnly)
	if len(provs) == 0 {
		if ownOnly {
			return nil, types.ErrVoiceCloneNotAllowed
		}
		return nil, errors.New("airouter: no ElevenLabs provider configured")
	}
	c := provs[0]
	smp := make([]elevenlabs.Sample, len(req.Samples))
	for i, s := range req.Samples {
		smp[i] = elevenlabs.Sample{Filename: s.Filename, Data: s.Data}
	}
	res, err := r.elClient(c.cfg).CloneVoice(ctx, elevenlabs.CloneOptions{
		Name: name, Description: req.Description, Samples: smp, Labels: req.Labels, RemoveBackgroundNoise: req.RemoveBackgroundNoise,
	})
	if err != nil {
		var ae *elevenlabs.APIError
		if errors.As(err, &ae) && (ae.Status == 400 || ae.Status == 402 || ae.Status == 403 || ae.Status == 422) {
			// The account cannot clone (plan), or the samples were rejected: tell the caller, do not pause the provider.
			return nil, fmt.Errorf("%w: ElevenLabs refused the clone (HTTP %d): %s", types.ErrBadRequest, ae.Status, types.Truncate(ae.Body, 300))
		}
		return nil, elErr(err, name)
	}
	if err := r.store.SaveVoice(ctx, types.VoiceRecord{VoiceID: res.VoiceID, TenantID: t.ID, ProviderID: c.cfg.ID, Name: name, Description: req.Description, CreatedAt: r.now().UTC()}); err != nil {
		return nil, err
	}
	r.vcache.drop(c.cfg.ID)
	return &types.Voice{ID: res.VoiceID, Name: name, Category: "cloned", Description: req.Description, Labels: req.Labels,
		Provider: c.cfg.Name, Own: c.cfg.TenantID != "", Cloned: true, RequiresVerification: res.RequiresVerification}, nil
}

// DeleteVoice deletes a voice this tenant cloned through airouter. Other
// tenants' voices, and voices airouter did not create, answer ErrVoiceNotFound.
func (r *Router) DeleteVoice(ctx context.Context, tenantID, voiceID string) error {
	t, err := r.activeTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	rec, err := r.store.VoiceByID(ctx, strings.TrimSpace(voiceID))
	if err != nil {
		return err
	}
	if rec == nil || rec.TenantID != t.ID {
		return types.ErrVoiceNotFound
	}
	for _, c := range r.snapshot(ctx) {
		if c.cfg.ID != rec.ProviderID {
			continue
		}
		if err := r.elClient(c.cfg).DeleteVoice(ctx, rec.VoiceID); err != nil {
			var ae *elevenlabs.APIError
			if !(errors.As(err, &ae) && ae.Status == 404) { // 404 = already gone
				return elErr(err, rec.Name)
			}
		}
		r.vcache.drop(c.cfg.ID)
		break
	}
	return r.store.DeleteVoice(ctx, rec.VoiceID)
}

// ShareVoice lets the operator (the default tenant) share a voice it cloned with
// every tenant, or make it private again. A new clone is private. Only the
// operator's own clones can be shared; tenants' clones never are.
func (r *Router) ShareVoice(ctx context.Context, tenantID, voiceID string, shared bool) error {
	t, err := r.activeTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	rec, err := r.store.VoiceByID(ctx, strings.TrimSpace(voiceID))
	if err != nil {
		return err
	}
	if rec == nil || rec.TenantID != t.ID {
		return types.ErrVoiceNotFound
	}
	if rec.TenantID != types.DefaultTenantID {
		return fmt.Errorf("%w: only the operator can share a voice", types.ErrBadRequest)
	}
	return r.store.SetVoiceShared(ctx, rec.VoiceID, shared)
}
