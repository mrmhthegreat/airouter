package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// mapErr turns any router error into (HTTP status, OpenAI-style error type,
// extra fields). It is the single place that decides status codes, so the
// native API, the OpenAI-compatible API and the audio endpoints agree.
//
//	400 bad_request          caller must fix the request (never retried)
//	401 unauthorized         missing/invalid key, unknown tenant
//	403 forbidden            tenant disabled
//	429 rate_limit           the TENANT's own daily cap (+ Retry-After)
//	502 upstream_error       providers were tried and all failed
//	503 providers_busy       every provider is limited / cooling (+ Retry-After)
//	504 timeout              the whole request ran out of time
func mapErr(err error) (code int, typ string, extra map[string]any) {
	var ex *types.ExhaustedError
	var af *types.AllFailedError
	var tl *types.TenantLimitError
	switch {
	case errors.As(err, &ex):
		return 503, "providers_busy", map[string]any{"retry_after_seconds": int64(ex.RetryAfter.Seconds()) + 1, "reasons": ex.Reasons}
	case errors.As(err, &tl):
		return 429, "rate_limit", map[string]any{"retry_after_seconds": int64(tl.RetryAfter.Seconds()) + 1}
	case errors.Is(err, types.ErrBadRequest):
		var br *types.BadRequestError
		if errors.As(err, &br) {
			return 400, "bad_request", map[string]any{"attempts": br.Attempts}
		}
		return 400, "bad_request", nil
	case errors.As(err, &af):
		return 502, "upstream_error", map[string]any{"attempts": af.Attempts}
	case errors.Is(err, types.ErrTimeout):
		return 504, "timeout", nil
	case errors.Is(err, types.ErrNoProviders):
		return 503, "no_providers", nil
	case errors.Is(err, types.ErrUnauthorized), errors.Is(err, types.ErrUnknownTenant):
		return 401, "unauthorized", nil
	case errors.Is(err, types.ErrTenantDisabled):
		return 403, "forbidden", nil
	case errors.Is(err, types.ErrVoiceNotFound):
		return 404, "voice_not_found", nil
	case errors.Is(err, types.ErrConsentRequired):
		return 400, "consent_required", nil
	case errors.Is(err, types.ErrVoiceCloneNotAllowed):
		return 403, "forbidden", nil
	case errors.Is(err, types.ErrNotFound):
		return 404, "not_found", nil
	case errors.Is(err, context.Canceled):
		return 499, "client_closed_request", nil
	}
	return 500, "internal_error", nil
}

func writeRouterErr(w http.ResponseWriter, err error) {
	code, _, extra := mapErr(err)
	writeErr(w, code, err.Error(), extra)
}
