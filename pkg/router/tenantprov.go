package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Tenant self-service
//
// Every tenant can manage THEIR OWN models, keys and limits: pick models from
// the preset catalog (or from the Ollama server), paste their own API key, set
// their own limits, or import a JSON file. Their providers are private (other
// tenants never see or use them) and are tried before the shared pool.
//
// Safety rules enforced here, because tenants are not admins:
//   - a tenant can only touch providers it owns (anything else is "not found");
//   - tenants never use env-var keys (that would spend the server's keys);
//   - the upstream URL comes from the preset, so a tenant cannot make the
//     server call arbitrary hosts (SSRF), unless the operator opts in with
//     TenantPolicy.AllowCustomBaseURL;
//   - the number of providers and the worker count per provider are capped.

// TenantPolicy limits what tenants may configure for themselves.
type TenantPolicy struct {
	// AllowCustomBaseURL lets tenants add providers with any base_url, not just
	// the preset ones. Off by default: it lets a tenant point the server at
	// internal addresses.
	AllowCustomBaseURL bool
	// MaxProviders caps providers per tenant (default 100).
	MaxProviders int
	// BlockOllama stops tenants from using the server's Ollama.
	BlockOllama bool
}

const maxTenantWorkers = 8

var errNotYours = types.ErrNotFound // never reveal that someone else's provider exists

// ownedProvider returns provider id if (and only if) tenantID owns it.
func (r *Router) ownedProvider(ctx context.Context, tenantID string, id int64) (types.ProviderConfig, error) {
	if tenantID == "" {
		return types.ProviderConfig{}, errNotYours
	}
	ps, err := r.store.ListProviders(ctx)
	if err != nil {
		return types.ProviderConfig{}, err
	}
	for _, p := range ps {
		if p.ID == id && p.TenantID == tenantID {
			return p, nil
		}
	}
	return types.ProviderConfig{}, errNotYours
}

func presetForURL(base string) string {
	base = strings.TrimRight(base, "/")
	var names []string
	for n := range preset.Definitions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.TrimRight(preset.Definitions[n].BaseURL, "/") == base {
			return n
		}
	}
	return ""
}

// prepareTenantProvider turns tenant input into a safe, owned ProviderConfig.
func (r *Router) prepareTenantProvider(tenantID string, p types.ProviderConfig) (types.ProviderConfig, error) {
	if strings.TrimSpace(tenantID) == "" {
		return p, errors.New("tenant id is required")
	}
	if strings.TrimSpace(p.APIKeyEnv) != "" {
		return p, errors.New("api_key_env is not allowed for tenants: put your own key in api_key")
	}
	p.ID, p.TenantID, p.APIKeyEnv = 0, tenantID, ""
	if p.Preset == "" && p.BaseURL != "" {
		p.Preset = presetForURL(p.BaseURL)
	}
	if p.Preset == "" && !r.tpolicy.AllowCustomBaseURL {
		return p, fmt.Errorf("pick a provider from the list (%s); custom URLs are disabled", strings.Join(preset.Names(), ", "))
	}
	if p.Preset != "" {
		def, ok := preset.Definitions[p.Preset]
		if !ok {
			return p, fmt.Errorf("unknown provider %q (have: %s)", p.Preset, strings.Join(preset.Names(), ", "))
		}
		if !r.tpolicy.AllowCustomBaseURL {
			p.Kind, p.BaseURL = def.Kind, def.BaseURL // the preset decides where requests go
		}
		if def.Kind == "ollama" {
			if r.tpolicy.BlockOllama {
				return p, errors.New("this server does not let tenants use Ollama")
			}
			if !r.tpolicy.AllowCustomBaseURL {
				p.BaseURL = r.ollamaURL
			}
		}
		if err := preset.Apply(&p); err != nil {
			return p, err
		}
		p.APIKeyEnv, p.Preset = "", "" // Apply re-adds the env var; tenants never get it
	}
	if p.Kind == "ollama" && r.tpolicy.BlockOllama {
		return p, errors.New("this server does not let tenants use Ollama")
	}
	for name, v := range map[string]int{"rpm": p.RPM, "rpd": p.RPD, "tpm": p.TPM, "ash": p.ASH, "asd": p.ASD, "workers": p.Workers, "priority": p.Priority} {
		if v < 0 {
			return p, fmt.Errorf("%s cannot be negative", name)
		}
	}
	if p.Workers > maxTenantWorkers {
		p.Workers = maxTenantWorkers
	}
	p.APIKey = strings.TrimSpace(p.APIKey)
	return p, nil
}

func (r *Router) countOwned(ctx context.Context, tenantID string) (int, map[string]bool) {
	ps, _ := r.store.ListProviders(ctx)
	n, names := 0, map[string]bool{}
	for _, p := range ps {
		if p.TenantID == tenantID {
			n++
			names[p.Name] = true
		}
	}
	return n, names
}

// AddTenantProvider adds one model for a tenant. Returns the stored config
// (without the key).
func (r *Router) AddTenantProvider(ctx context.Context, tenantID string, in types.ProviderConfig) (*types.ProviderConfig, error) {
	p, err := r.prepareTenantProvider(tenantID, in)
	if err != nil {
		return nil, err
	}
	if p.NeedsKey() && p.APIKey == "" {
		return nil, fmt.Errorf("paste your own API key for %s (it is stored encrypted and never shown again)", p.Name)
	}
	n, names := r.countOwned(ctx, tenantID)
	if names[p.Name] {
		return nil, fmt.Errorf("you already have %q; change its key or limits instead", p.Name)
	}
	if n >= r.tpolicy.MaxProviders {
		return nil, fmt.Errorf("limit reached: at most %d models per tenant", r.tpolicy.MaxProviders)
	}
	if err := r.SaveProvider(ctx, &p); err != nil {
		return nil, err
	}
	p.APIKey = ""
	return &p, nil
}

// ImportTenantProviders adds many providers from a parsed JSON file (the same
// format as `provider import`). Each entry succeeds or fails on its own.
func (r *Router) ImportTenantProviders(ctx context.Context, tenantID string, in []types.ProviderConfig) (added []string, failed map[string]string) {
	failed = map[string]string{}
	for i, p := range in {
		label := p.Name
		if label == "" {
			label = fmt.Sprintf("#%d %s", i+1, p.Model)
		}
		out, err := r.AddTenantProvider(ctx, tenantID, p)
		if err != nil {
			failed[label] = err.Error()
			continue
		}
		added = append(added, out.Name)
	}
	return added, failed
}

// AddTenantCatalog adds models of one catalog provider for a tenant, all using
// the tenant's own key. models are exact model ids; empty means every text
// model that provider has in the free pool.
func (r *Router) AddTenantCatalog(ctx context.Context, tenantID, presetName, apiKey string, models []string) (added, skipped []string, err error) {
	if _, ok := preset.Definitions[presetName]; !ok {
		return nil, nil, fmt.Errorf("unknown provider %q (have: %s)", presetName, strings.Join(preset.Names(), ", "))
	}
	all := len(models) == 0
	want := map[string]bool{}
	for _, m := range models {
		want[m] = true
	}
	for _, m := range preset.FreePool {
		if m.Preset != presetName || (all && m.Category != preset.CategoryText) || (!all && !want[m.Model]) {
			continue
		}
		cfg := m.ToProviderConfig()
		cfg.APIKeyEnv, cfg.APIKey = "", apiKey
		delete(want, m.Model)
		if _, err := r.AddTenantProvider(ctx, tenantID, cfg); err != nil {
			if strings.Contains(err.Error(), "paste your own API key") {
				return added, skipped, err
			}
			skipped = append(skipped, m.Model+": "+err.Error())
			continue
		}
		added = append(added, cfg.Name)
	}
	if !all {
		for m := range want { // asked for, but never matched (neither added nor skipped above)
			skipped = append(skipped, m+": not in the catalog for "+presetName+" (use JSON import)")
		}
	}
	return added, skipped, nil
}

// TenantLimits are the fields a tenant may change on its own model. nil = leave as is.
type TenantLimits struct {
	RPM, RPD, TPM, Workers, Priority *int
}

func (r *Router) UpdateTenantLimits(ctx context.Context, tenantID string, id int64, l TenantLimits) error {
	p, err := r.ownedProvider(ctx, tenantID, id)
	if err != nil {
		return err
	}
	set := func(dst *int, v *int) error {
		if v == nil {
			return nil
		}
		if *v < 0 {
			return errors.New("limits cannot be negative")
		}
		*dst = *v
		return nil
	}
	for _, e := range []error{set(&p.RPM, l.RPM), set(&p.RPD, l.RPD), set(&p.TPM, l.TPM), set(&p.Workers, l.Workers), set(&p.Priority, l.Priority)} {
		if e != nil {
			return e
		}
	}
	if p.Workers > maxTenantWorkers {
		p.Workers = maxTenantWorkers
	}
	return r.SaveProvider(ctx, &p)
}

func (r *Router) SetTenantProviderKey(ctx context.Context, tenantID string, id int64, key string) error {
	if _, err := r.ownedProvider(ctx, tenantID, id); err != nil {
		return err
	}
	return r.SetProviderKey(ctx, id, key)
}

func (r *Router) SetTenantProviderDisabled(ctx context.Context, tenantID string, id int64, disabled bool) error {
	if _, err := r.ownedProvider(ctx, tenantID, id); err != nil {
		return err
	}
	return r.SetDisabled(ctx, id, disabled)
}

func (r *Router) ResetTenantProvider(ctx context.Context, tenantID string, id int64) error {
	if _, err := r.ownedProvider(ctx, tenantID, id); err != nil {
		return err
	}
	return r.ClearPause(ctx, id)
}

func (r *Router) DeleteTenantProvider(ctx context.Context, tenantID string, id int64) error {
	if _, err := r.ownedProvider(ctx, tenantID, id); err != nil {
		return err
	}
	return r.DeleteProvider(ctx, id)
}

// SharedModel is what a tenant may see about the operator's shared pool.
type SharedModel struct {
	Name     string `json:"name"`
	Model    string `json:"model"`
	Category string `json:"category"`
	State    string `json:"state"`
}

// ListTenantProviders returns the tenant's own models with live status, and a
// read-only summary of the shared models that also serve its requests.
func (r *Router) ListTenantProviders(ctx context.Context, tenantID string) (own []ProviderStatus, shared []SharedModel) {
	own, shared = []ProviderStatus{}, []SharedModel{}
	for _, s := range r.Status(ctx, tenantID) {
		if s.TenantID == tenantID && tenantID != "" {
			own = append(own, s)
		} else if s.TenantID == "" {
			shared = append(shared, SharedModel{Name: s.Name, Model: s.Model, Category: s.Category, State: s.State})
		}
	}
	return own, shared
}

// ExportTenantProviders returns a tenant's models in the import-file format,
// without keys, so a config can be kept in a JSON file and re-imported.
func (r *Router) ExportTenantProviders(ctx context.Context, tenantID string) []types.ProviderConfig {
	ps, _ := r.store.ListProviders(ctx)
	out := []types.ProviderConfig{}
	for _, p := range ps {
		if p.TenantID == tenantID && tenantID != "" {
			p.ID, p.TenantID, p.APIKey, p.APIKeyEnv = 0, "", "", ""
			out = append(out, p)
		}
	}
	return out
}

// ---- Ollama -----------------------------------------------------------------

// OllamaModel is one model installed on the Ollama server.
type OllamaModel struct {
	Name   string `json:"name"`
	Params string `json:"parameter_size,omitempty"`
	SizeGB string `json:"size_gb,omitempty"`
}

// OllamaModels lists the models installed on the Ollama server. The URL is the
// operator's (Options.OllamaURL), never tenant-supplied.
func (r *Router) OllamaModels(ctx context.Context) ([]OllamaModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.ollamaURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama is not reachable at %s", r.ollamaURL)
	}
	defer resp.Body.Close()
	var d struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				Params string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&d) != nil {
		return nil, fmt.Errorf("unexpected reply from Ollama at %s", r.ollamaURL)
	}
	out := []OllamaModel{}
	for _, m := range d.Models {
		out = append(out, OllamaModel{Name: m.Name, Params: m.Details.Params, SizeGB: fmt.Sprintf("%.1f", float64(m.Size)/1e9)})
	}
	return out, nil
}

// AddTenantOllama adds installed Ollama models for a tenant (no key needed).
func (r *Router) AddTenantOllama(ctx context.Context, tenantID string, models []string) (added, skipped []string) {
	for _, m := range models {
		_, err := r.AddTenantProvider(ctx, tenantID, types.ProviderConfig{
			Preset: "ollama", Name: "ollama-" + m, Model: m, Priority: 90, Workers: 1,
		})
		if err != nil {
			skipped = append(skipped, m+": "+err.Error())
			continue
		}
		added = append(added, "ollama-"+m)
	}
	return added, skipped
}
