package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// listVoices handles GET /v1/voices?provider=&search= (ElevenLabs voices this tenant can use).
func (s *apiServer) listVoices(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	vs, err := s.r.ListVoices(r.Context(), t.ID, router.ListVoicesOptions{
		Provider: r.URL.Query().Get("provider"), Search: r.URL.Query().Get("search"),
	})
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"voices": vs})
}

// cloneVoice handles POST /v1/voices/clone (multipart/form-data).
//
// Fields: name (required), consent=true (required), files (1 to 25 audio files,
// repeat the field), description, labels (JSON object of strings),
// remove_background_noise, provider.
func (s *apiServer) cloneVoice(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 60<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, 400, "invalid multipart form: "+err.Error(), nil)
		return
	}
	req := types.CloneVoiceRequest{
		TenantID:              t.ID,
		Name:                  r.FormValue("name"),
		Description:           r.FormValue("description"),
		Provider:              r.FormValue("provider"),
		ConsentConfirmed:      truthy(r.FormValue("consent")),
		RemoveBackgroundNoise: truthy(r.FormValue("remove_background_noise")),
	}
	if lb := strings.TrimSpace(r.FormValue("labels")); lb != "" {
		if err := json.Unmarshal([]byte(lb), &req.Labels); err != nil {
			writeErr(w, 400, `labels must be a JSON object of strings, e.g. {"gender":"female"}`, nil)
			return
		}
	}
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				writeErr(w, 400, "reading "+fh.Filename+": "+err.Error(), nil)
				return
			}
			data, err := io.ReadAll(io.LimitReader(f, 13<<20))
			f.Close()
			if err != nil {
				writeErr(w, 400, "reading "+fh.Filename+": "+err.Error(), nil)
				return
			}
			req.Samples = append(req.Samples, types.VoiceSample{Filename: fh.Filename, Data: data})
		}
	}
	v, err := s.r.CloneVoice(r.Context(), req)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	writeJSON(w, 201, v)
}

// deleteVoice handles DELETE /v1/voices/{id} (only voices this tenant cloned).
func (s *apiServer) deleteVoice(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTenant(w, r)
	if !ok {
		return
	}
	if err := s.r.DeleteVoice(r.Context(), t.ID, r.PathValue("id")); err != nil {
		writeRouterErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func truthy(v string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(v))
	return b
}

// shareVoice handles POST /v1/voices/{id}/share (admin token). Body {"shared":true}
// shares an operator-cloned voice with every tenant; {"shared":false} makes it
// private again. Only voices cloned by the default (operator) tenant can be shared.
func (s *apiServer) shareVoice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Shared *bool `json:"shared"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Shared == nil {
		writeErr(w, 400, `send {"shared": true} or {"shared": false}`, nil)
		return
	}
	id := r.PathValue("id")
	if err := s.r.ShareVoice(r.Context(), types.DefaultTenantID, id, *in.Shared); err != nil {
		writeRouterErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "shared": *in.Shared})
}
