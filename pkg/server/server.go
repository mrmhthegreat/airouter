// Package server exposes the router over HTTP. The API is intentionally small:
//
//	POST /v1/generate                          tenant key   generate text (or JSON)
//	POST /v1/speak                             tenant key   text → audio (WAV bytes back)
//	POST /v1/transcribe                        tenant key   audio → text (multipart in, JSON out)
//	GET  /v1/usage                             tenant key   usage + live provider status
//	GET  /v1/presets                           public       list of built-in presets
//	GET  /v1/presets/free-pool                 public       curated free-tier model catalog
//	POST /v1/bootstrap                         admin        seed the DB from the free-pool catalog
//	GET/POST /v1/providers                     admin        list / add-or-update providers
//	DELETE /v1/providers/{id}                  admin        remove a provider
//	POST /v1/providers/{id}/enable|disable     admin        toggle a provider
//	POST /v1/providers/{id}/reset              admin        clear a pause
//	GET/POST /v1/tenants                       admin        list / create tenants
//	GET  /healthz                              public       liveness probe
//
// Auth: tenant endpoints take "Authorization: Bearer air_...". Admin endpoints
// take "Authorization: Bearer <admin token>" (set via AIROUTER_ADMIN_TOKEN).
// If the admin token is empty, admin endpoints return 403 — use the CLI instead.
package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// NewHandler returns the HTTP API. The same *router.Router can be shared
// between this handler and direct Go callers.
func NewHandler(r *router.Router, adminToken string) http.Handler {
	srv := &apiServer{r: r, admin: adminToken}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/presets", func(w http.ResponseWriter, _ *http.Request) {
		out := make(map[string]preset.Definition, len(preset.Definitions))
		for k, v := range preset.Definitions {
			out[k] = v
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /v1/presets/free-pool", func(w http.ResponseWriter, r *http.Request) {
		// Optional ?category=text,tts filter; empty = all.
		var categories []string
		catParam := r.URL.Query().Get("category")
		if catParam != "" && catParam != "all" {
			for _, c := range strings.Split(catParam, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					categories = append(categories, c)
				}
			}
		}
		writeJSON(w, 200, map[string]any{
			"models":      preset.FreePoolByCategory(categories),
			"verified_on": preset.CatalogVerifiedOn().Format("2006-01-02"),
			"summary":     preset.CatalogSummary(),
			"categories":  preset.Categories,
		})
	})

	mux.HandleFunc("POST /v1/generate", srv.generate)
	mux.HandleFunc("POST /v1/speak", srv.speak)
	mux.HandleFunc("POST /v1/transcribe", srv.transcribe)
	mux.HandleFunc("GET /v1/usage", srv.usage)
	mux.HandleFunc("GET /v1/whoami", srv.whoami)
	srv.registerMe(mux) // tenant self-service: own models, keys, limits

	// OpenAI-compatible surface: point any OpenAI SDK / editor plugin at
	// http://HOST/v1 with an airouter key and it just works.
	mux.HandleFunc("POST /v1/chat/completions", srv.chatCompletions)
	mux.HandleFunc("GET /v1/models", srv.models)
	mux.HandleFunc("POST /v1/audio/speech", srv.audioSpeech)
	mux.HandleFunc("POST /v1/audio/transcriptions", srv.transcribe)

	// Built-in web UI (chat, usage, API help, admin).
	mux.Handle("GET /{$}", uiHandler())

	mux.HandleFunc("GET /v1/providers", srv.adminOnly(srv.listProviders))
	mux.HandleFunc("POST /v1/providers", srv.adminOnly(srv.saveProvider))
	mux.HandleFunc("POST /v1/bootstrap", srv.adminOnly(srv.bootstrap))
	mux.HandleFunc("DELETE /v1/providers/{id}", srv.adminOnly(srv.deleteProvider))
	mux.HandleFunc("POST /v1/providers/{id}/{action}", srv.adminOnly(srv.toggleProvider))

	mux.HandleFunc("GET /v1/tenants", srv.adminOnly(srv.listTenants))
	mux.HandleFunc("POST /v1/tenants", srv.adminOnly(srv.createTenant))
	mux.HandleFunc("POST /v1/tenants/{id}/rotate-key", srv.adminOnly(srv.rotateTenantKey))

	return mux
}

type apiServer struct {
	r     *router.Router
	admin string
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string, extra map[string]any) {
	body := map[string]any{"error": msg}
	for k, v := range extra {
		body[k] = v
	}
	if ra, ok := extra["retry_after_seconds"].(int64); ok && ra > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(ra, 10))
	}
	writeJSON(w, code, body)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func (s *apiServer) isAdmin(r *http.Request) bool {
	tok := bearer(r)
	return s.admin != "" && tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.admin)) == 1
}

func (s *apiServer) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.admin == "" {
			writeErr(w, 403, "admin API disabled: start the server with AIROUTER_ADMIN_TOKEN set", nil)
			return
		}
		if !s.isAdmin(r) {
			writeErr(w, 401, "admin token required", nil)
			return
		}
		h(w, r)
	}
}

// resolveTenant resolves the caller: a tenant key, or the admin token acting as
// X-Tenant-ID / ?tenant= (defaulting to the default tenant).
func (s *apiServer) resolveTenant(w http.ResponseWriter, r *http.Request) (*types.Tenant, bool) {
	if s.isAdmin(r) {
		id := r.Header.Get("X-Tenant-ID")
		if id == "" {
			id = r.URL.Query().Get("tenant")
		}
		if id == "" {
			id = types.DefaultTenantID
		}
		t, err := s.r.Store().TenantByID(r.Context(), id)
		if err != nil || t == nil {
			writeErr(w, 404, "unknown tenant", nil)
			return nil, false
		}
		return t, true
	}
	key := bearer(r)
	if key == "" {
		writeErr(w, 401, "missing api key (Authorization: Bearer <key>)", nil)
		return nil, false
	}
	t, err := s.r.Authenticate(r.Context(), key)
	if err != nil {
		writeRouterErr(w, err)
		return nil, false
	}
	return t, true
}

func (s *apiServer) generate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	var req types.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
		return
	}
	req.TenantID = t.ID // callers can't spoof another tenant
	resp, err := s.r.Generate(r.Context(), req)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	w.Header().Set("X-AIRouter-Provider", resp.Provider)
	w.Header().Set("X-AIRouter-Model", resp.Model)
	if r.URL.Query().Get("format") == "text" || strings.Contains(r.Header.Get("Accept"), "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(resp.Text))
		return
	}
	writeJSON(w, 200, resp)
}

func (s *apiServer) usage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	rep, err := s.r.Usage(r.Context(), t.ID, r.URL.Query().Get("day"))
	if err != nil {
		writeErr(w, 500, err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{
		"usage":       rep,
		"providers":   s.r.Status(r.Context(), t.ID),
		"server_time": time.Now().UTC().Format(time.RFC3339),
	})
}

// speak handles POST /v1/speak — text → audio bytes.
//
// Request body is JSON: {"text":"...", "voice":"...", "format":"...", "language":"...", "provider":"..."}
// Response by default is the raw audio bytes with the right Content-Type
// (audio/wav, audio/mpeg, ...). Pass ?format=json (or Accept: application/json)
// to get a JSON envelope with base64-encoded audio instead — useful for clients
// that cannot easily consume binary responses.
func (s *apiServer) speak(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	var req types.SpeakRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
		return
	}
	req.TenantID = t.ID
	resp, err := s.r.Speak(r.Context(), req)
	if err != nil {
		writeSpeakTranscribeError(w, err)
		return
	}
	wantsJSON := r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json")
	if wantsJSON {
		// JSON envelope with base64 audio. Useful for clients that can't
		// easily consume binary responses (e.g. browser fetch + btoa).
		resp.AudioB64 = base64StdEncoding(resp.Audio)
		writeJSON(w, 200, resp)
		return
	}
	// Default: raw audio bytes.
	w.Header().Set("Content-Type", resp.ContentType)
	w.Header().Set("X-AIRouter-Provider", resp.Provider)
	w.Header().Set("X-AIRouter-Model", resp.Model)
	w.Header().Set("X-AIRouter-Voice", resp.Voice)
	_, _ = w.Write(resp.Audio)
}

// transcribe handles POST /v1/transcribe — audio bytes → text.
//
// Request body is multipart/form-data with a "file" field (audio bytes) plus
// optional "language", "prompt", "provider", and "model" fields.
//
// Response is JSON: {"text":"...", "provider":"...", "model":"...", "language":"..."}
// Pass ?format=text (or Accept: text/plain) to get just the transcribed text
// as plain text — useful for piping into another tool.
func (s *apiServer) transcribe(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	// 32 MB max upload — enough for ~30 minutes of compressed audio.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "invalid multipart form: "+err.Error(), nil)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "missing 'file' field in multipart form", nil)
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, 32<<20))
	if err != nil {
		writeErr(w, 400, "reading uploaded file: "+err.Error(), nil)
		return
	}
	req := types.TranscribeRequest{
		TenantID: t.ID,
		Audio:    audio,
		Filename: header.Filename,
		Language: r.FormValue("language"),
		Prompt:   r.FormValue("prompt"),
		Provider: r.FormValue("provider"),
	}
	resp, err := s.r.Transcribe(r.Context(), req)
	if err != nil {
		writeSpeakTranscribeError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "text" || strings.Contains(r.Header.Get("Accept"), "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-AIRouter-Provider", resp.Provider)
		w.Header().Set("X-AIRouter-Model", resp.Model)
		_, _ = w.Write([]byte(resp.Text))
		return
	}
	writeJSON(w, 200, resp)
}

// writeSpeakTranscribeError is kept for the speak/transcribe handlers.
func writeSpeakTranscribeError(w http.ResponseWriter, err error) { writeRouterErr(w, err) }

// base64StdEncoding returns the standard base64 encoding of b. Used to embed
// audio bytes in the JSON envelope response when the caller asks for ?format=json.
func base64StdEncoding(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func (s *apiServer) listProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.r.Status(r.Context(), ""))
}

func (s *apiServer) saveProvider(w http.ResponseWriter, r *http.Request) {
	var p types.ProviderConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
		return
	}
	if err := s.r.SaveProvider(r.Context(), &p); err != nil {
		writeErr(w, 400, err.Error(), nil)
		return
	}
	p.APIKey = "" // never echo secrets
	writeJSON(w, 201, p)
}

func (s *apiServer) bootstrap(w http.ResponseWriter, r *http.Request) {
	overwrite := r.URL.Query().Get("overwrite") == "1" || r.URL.Query().Get("overwrite") == "true"
	onlyPreset := r.URL.Query().Get("preset")
	categoryParam := r.URL.Query().Get("category") // comma-separated; empty = default (text only); "all" = everything

	// Distinguish three cases so the response label is honest:
	//   ""            → nil     → BootstrapProviders uses default (text only)
	//   "all"         → []string{} (empty, non-nil) → all categories
	//   "text,tts,.." → []string{"text","tts"}     → only those
	var categories []string
	switch categoryParam {
	case "":
		categories = nil
	case "all":
		categories = []string{}
	default:
		for _, c := range strings.Split(categoryParam, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				categories = append(categories, c)
			}
		}
		for _, c := range categories {
			if c == "all" {
				categories = []string{}
				break
			}
		}
	}
	storeKeys := r.URL.Query().Get("store_keys") == "1" || r.URL.Query().Get("store_keys") == "true"
	added, skipped, err := s.r.BootstrapProvidersOpts(r.Context(), router.BootstrapOptions{Overwrite: overwrite, Preset: onlyPreset, Categories: categories, StoreKeys: storeKeys})
	if err != nil {
		writeErr(w, 500, err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{
		"added":                added,
		"skipped":              skipped,
		"verified":             preset.CatalogVerifiedOn().Format("2006-01-02"),
		"categories_used":      categoriesLabel(categories),
		"available_categories": preset.Categories,
		"summary":              preset.CatalogSummary(),
		"next_step":            "set the env vars named in skipped[] to import those providers too",
	})
}

// categoriesLabel reports which categories BootstrapProviders actually used,
// distinguishing nil (default text) from empty (all) from a real list.
func categoriesLabel(cats []string) string {
	if cats == nil {
		return "text (default)"
	}
	if len(cats) == 0 {
		return "all"
	}
	return strings.Join(cats, ",")
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *apiServer) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id", nil)
		return
	}
	if err := s.r.DeleteProvider(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *apiServer) toggleProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	action := r.PathValue("action")
	if !ok {
		writeErr(w, 400, "bad id", nil)
		return
	}
	switch action {
	case "enable", "disable":
		if err := s.r.SetDisabled(r.Context(), id, action == "disable"); err != nil {
			code := 500
			if errors.Is(err, types.ErrNotFound) {
				code = 404
			}
			writeErr(w, code, err.Error(), nil)
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "disabled": action == "disable"})
	case "key":
		var body struct {
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
			return
		}
		if err := s.r.SetProviderKey(r.Context(), id, body.APIKey); err != nil {
			code := 400
			if errors.Is(err, types.ErrNotFound) {
				code = 404
			}
			writeErr(w, code, err.Error(), nil)
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "key_stored": true})
	case "reset":
		if err := s.r.ClearPause(r.Context(), id); err != nil {
			writeErr(w, 500, err.Error(), nil)
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "paused": false})
	default:
		writeErr(w, 400, "use /v1/providers/{id}/enable, /disable, /reset, or /key", nil)
	}
}

func (s *apiServer) listTenants(w http.ResponseWriter, r *http.Request) {
	ts, err := s.r.Store().ListTenants(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error(), nil)
		return
	}
	writeJSON(w, 200, ts)
}

func (s *apiServer) createTenant(w http.ResponseWriter, r *http.Request) {
	var t types.Tenant
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&t); err != nil {
		writeErr(w, 400, "invalid JSON body: "+err.Error(), nil)
		return
	}
	t, key, err := s.r.CreateTenant(r.Context(), t)
	if err != nil {
		writeErr(w, 400, err.Error(), nil)
		return
	}
	writeJSON(w, 201, map[string]any{
		"tenant":  t,
		"api_key": key,
		"note":    "store this key now — it is not shown again",
	})
}

func (s *apiServer) rotateTenantKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.r.RotateTenantKey(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"tenant":  r.PathValue("id"),
		"api_key": key,
		"note":    "the old key no longer works; store this one now — it is not shown again",
	})
}

// whoami tells a client (and the web UI) what its credential is.
func (s *apiServer) whoami(w http.ResponseWriter, r *http.Request) {
	role := "tenant"
	if s.isAdmin(r) {
		role = "admin"
	}
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"role": role, "tenant": t})
}
