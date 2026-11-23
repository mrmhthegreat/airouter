package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// /v1/me/*  — self-service for ONE tenant: its own models, keys and limits.
//
// Auth: the tenant's own API key. An admin may act for a tenant by adding
// X-Tenant-ID (or ?tenant=). Whatever the caller, only providers owned by that
// tenant can be read or changed; others answer 404.
//
//	GET    /v1/me                              who am I (name, tz, quota)
//	PATCH  /v1/me                              {"name": "...", "tz": "Asia/Karachi"}  (quota is operator-only)
//	POST   /v1/me/rotate-key                   new API key; the old one stops working
//	GET    /v1/me/usage?day=YYYY-MM-DD         my usage and live model status
//	POST   /v1/me/bootstrap                    {"keys":{"GROQ_API_KEY":"..."}, "preset":"groq"}  add free-pool models with MY keys
//	GET    /v1/me/providers                    own models (live status) + shared models (read-only)
//	POST   /v1/me/providers                    add one model  {preset, model, api_key, rpm, rpd, tpm, ...}
//	POST   /v1/me/providers/{id}/enable|disable|reset
//	POST   /v1/me/providers/{id}/key           {"api_key": "..."}
//	POST   /v1/me/providers/{id}/limits        {"rpm": 30, "rpd": 1000, "tpm": 6000, "workers": 2, "priority": 10}
//	DELETE /v1/me/providers/{id}
//	GET    /v1/me/catalog                      providers + free-pool models, marked with what you already added
//	POST   /v1/me/catalog/add                  {"preset":"groq","api_key":"...","models":["id",...]}
//	GET    /v1/me/ollama                       models installed on the server's Ollama
//	POST   /v1/me/ollama/add                   {"models":["gemma3:4b"]}
//	POST   /v1/me/import                       {"providers":[...]}  (same JSON as `provider import`)
//	GET    /v1/me/export                       your models as an importable JSON file (no keys)

func (s *apiServer) registerMe(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me", s.me(s.meGet))
	mux.HandleFunc("PATCH /v1/me", s.me(s.mePatch))
	mux.HandleFunc("POST /v1/me/rotate-key", s.me(s.meRotateKey))
	mux.HandleFunc("GET /v1/me/usage", s.me(s.meUsage))
	mux.HandleFunc("POST /v1/me/bootstrap", s.me(s.meBootstrap))
	mux.HandleFunc("GET /v1/me/providers", s.me(s.meList))
	mux.HandleFunc("POST /v1/me/providers", s.me(s.meAdd))
	mux.HandleFunc("POST /v1/me/providers/{id}/{action}", s.me(s.meAction))
	mux.HandleFunc("DELETE /v1/me/providers/{id}", s.me(s.meDelete))
	mux.HandleFunc("GET /v1/me/catalog", s.me(s.meCatalog))
	mux.HandleFunc("POST /v1/me/catalog/add", s.me(s.meCatalogAdd))
	mux.HandleFunc("GET /v1/me/ollama", s.me(s.meOllama))
	mux.HandleFunc("POST /v1/me/ollama/add", s.me(s.meOllamaAdd))
	mux.HandleFunc("POST /v1/me/import", s.me(s.meImport))
	mux.HandleFunc("GET /v1/me/export", s.me(s.meExport))
}

type meHandler func(w http.ResponseWriter, r *http.Request, t *types.Tenant)

func (s *apiServer) me(h meHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := s.resolveTenant(w, r)
		if !ok {
			return
		}
		h(w, r, t)
	}
}

func meErr(w http.ResponseWriter, err error) {
	code := 400
	switch {
	case errors.Is(err, types.ErrNotFound):
		code = 404
	case errors.Is(err, types.ErrUnauthorized), errors.Is(err, types.ErrUnknownTenant):
		code = 401
	}
	writeErr(w, code, err.Error(), nil)
}

func decode(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v); err != nil {
		writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
		return false
	}
	return true
}

func (s *apiServer) meList(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	own, shared := s.r.ListTenantProviders(r.Context(), t.ID)
	writeJSON(w, 200, map[string]any{"tenant": t.ID, "own": own, "shared": shared})
}

func (s *apiServer) meAdd(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var in types.ProviderConfig
	if !decode(w, r, &in, 64<<10) {
		return
	}
	p, err := s.r.AddTenantProvider(r.Context(), t.ID, in)
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 201, p)
}

func (s *apiServer) meAction(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id", nil)
		return
	}
	var err error
	switch r.PathValue("action") {
	case "enable":
		err = s.r.SetTenantProviderDisabled(r.Context(), t.ID, id, false)
	case "disable":
		err = s.r.SetTenantProviderDisabled(r.Context(), t.ID, id, true)
	case "reset":
		err = s.r.ResetTenantProvider(r.Context(), t.ID, id)
	case "key":
		var b struct {
			APIKey string `json:"api_key"`
		}
		if !decode(w, r, &b, 64<<10) {
			return
		}
		err = s.r.SetTenantProviderKey(r.Context(), t.ID, id, b.APIKey)
	case "limits":
		var b struct {
			RPM, RPD, TPM, Workers, Priority *int
		}
		// accept lower-case JSON names
		var raw map[string]*int
		if !decode(w, r, &raw, 4<<10) {
			return
		}
		b.RPM, b.RPD, b.TPM, b.Workers, b.Priority = raw["rpm"], raw["rpd"], raw["tpm"], raw["workers"], raw["priority"]
		err = s.r.UpdateTenantLimits(r.Context(), t.ID, id, router.TenantLimits{RPM: b.RPM, RPD: b.RPD, TPM: b.TPM, Workers: b.Workers, Priority: b.Priority})
	default:
		writeErr(w, 404, "use enable, disable, reset, key or limits", nil)
		return
	}
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "ok": true})
}

func (s *apiServer) meDelete(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id", nil)
		return
	}
	if err := s.r.DeleteTenantProvider(r.Context(), t.ID, id); err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

type catalogModel struct {
	Preset     string `json:"preset"`
	Model      string `json:"model"`
	Category   string `json:"category"`
	RPM        int    `json:"rpm"`
	RPD        int    `json:"rpd"`
	TPM        int    `json:"tpm"`
	Notes      string `json:"notes,omitempty"`
	VerifiedOn string `json:"verified_on,omitempty"`
	Added      bool   `json:"added"`
}

type catalogProvider struct {
	Preset   string `json:"preset"`
	Kind     string `json:"kind"`
	KeyEnv   string `json:"key_env,omitempty"`
	NeedsKey bool   `json:"needs_key"`
}

func (s *apiServer) meCatalog(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	own, _ := s.r.ListTenantProviders(r.Context(), t.ID)
	have := map[string]bool{}
	for _, o := range own {
		have[strings.TrimRight(o.BaseURL, "/")+"|"+o.Model] = true
	}
	models, provs := []catalogModel{}, []catalogProvider{}
	seen := map[string]bool{}
	for _, m := range preset.FreePool {
		def := preset.Definitions[m.Preset]
		url := strings.TrimRight(def.BaseURL, "/")
		if def.Kind == "ollama" {
			continue // Ollama models come from /v1/me/ollama (what is actually installed)
		}
		models = append(models, catalogModel{Preset: m.Preset, Model: m.Model, Category: m.Category, RPM: m.RPM, RPD: m.RPD, TPM: m.TPM,
			Notes: m.Notes, VerifiedOn: m.VerifiedOn, Added: have[url+"|"+m.Model]})
		if !seen[m.Preset] {
			seen[m.Preset] = true
			provs = append(provs, catalogProvider{Preset: m.Preset, Kind: def.Kind, KeyEnv: def.APIKeyEnv, NeedsKey: def.Kind != "ollama"})
		}
	}
	sort.SliceStable(provs, func(i, j int) bool { return provs[i].Preset < provs[j].Preset })
	writeJSON(w, 200, map[string]any{"providers": provs, "models": models, "verified_on": preset.CatalogVerifiedOn().Format("2006-01-02")})
}

func (s *apiServer) meCatalogAdd(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var b struct {
		Preset string   `json:"preset"`
		APIKey string   `json:"api_key"`
		Models []string `json:"models"`
	}
	if !decode(w, r, &b, 64<<10) {
		return
	}
	added, skipped, err := s.r.AddTenantCatalog(r.Context(), t.ID, b.Preset, strings.TrimSpace(b.APIKey), b.Models)
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped})
}

func (s *apiServer) meOllama(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	ms, err := s.r.OllamaModels(r.Context())
	if err != nil {
		writeJSON(w, 200, map[string]any{"available": false, "error": err.Error(), "models": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "models": ms})
}

func (s *apiServer) meOllamaAdd(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var b struct {
		Models []string `json:"models"`
	}
	if !decode(w, r, &b, 16<<10) {
		return
	}
	added, skipped := s.r.AddTenantOllama(r.Context(), t.ID, b.Models)
	writeJSON(w, 200, map[string]any{"added": added, "skipped": skipped})
}

func (s *apiServer) meImport(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var f struct {
		Providers []types.ProviderConfig `json:"providers"`
	}
	if !decode(w, r, &f, 1<<20) {
		return
	}
	if len(f.Providers) == 0 {
		writeErr(w, 400, `expected {"providers":[ ... ]}`, nil)
		return
	}
	added, failed := s.r.ImportTenantProviders(r.Context(), t.ID, f.Providers)
	writeJSON(w, 200, map[string]any{"added": added, "failed": failed})
}

func (s *apiServer) meExport(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	w.Header().Set("Content-Disposition", `attachment; filename="airouter-providers-`+t.ID+`.json"`)
	writeJSON(w, 200, map[string]any{"providers": s.r.ExportTenantProviders(r.Context(), t.ID)})
}

func (s *apiServer) meGet(w http.ResponseWriter, _ *http.Request, t *types.Tenant) {
	writeJSON(w, 200, map[string]any{"role": "tenant", "tenant": t})
}

// PATCH /v1/me changes only name and tz. daily_request_limit and disabled are
// set by the operator; a tenant must not be able to lift its own quota.
func (s *apiServer) mePatch(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var in struct {
		Name string `json:"name"`
		TZ   string `json:"tz"`
	}
	if !decode(w, r, &in, 16<<10) {
		return
	}
	u, err := s.r.UpdateTenantProfile(r.Context(), t.ID, in.Name, in.TZ)
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"role": "tenant", "tenant": u})
}

func (s *apiServer) meRotateKey(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	key, err := s.r.RotateTenantKey(r.Context(), t.ID)
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"tenant":  t.ID,
		"api_key": key,
		"note":    "the old key no longer works; store this one now, it is not shown again",
	})
}

func (s *apiServer) meUsage(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	rep, err := s.r.Usage(r.Context(), t.ID, r.URL.Query().Get("day"))
	if err != nil {
		writeErr(w, 500, err.Error(), nil)
		return
	}
	own, shared := s.r.ListTenantProviders(r.Context(), t.ID)
	writeJSON(w, 200, map[string]any{"tenant": t.ID, "usage": rep, "own": own, "shared": shared})
}

// meBootstrap adds the free-pool models for the presets whose keys the caller
// supplies. It never reads the server's environment variables.
func (s *apiServer) meBootstrap(w http.ResponseWriter, r *http.Request, t *types.Tenant) {
	var in struct {
		Keys      map[string]string `json:"keys"`
		Preset    string            `json:"preset"`
		Overwrite bool              `json:"overwrite"`
	}
	if !decode(w, r, &in, 64<<10) {
		return
	}
	if len(in.Keys) == 0 {
		writeErr(w, 400, `send your own keys: {"keys":{"GROQ_API_KEY":"gsk_..."}}`, nil)
		return
	}
	added, skipped, err := s.r.BootstrapProvidersOpts(r.Context(), router.BootstrapOptions{
		Overwrite: in.Overwrite, Preset: in.Preset, TenantID: t.ID, Keys: in.Keys, NoEnv: true,
	})
	if err != nil {
		meErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tenant": t.ID, "added": added, "skipped": skipped})
}
