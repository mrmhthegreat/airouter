package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Failure kinds recorded on types.Attempt.Kind.
const (
	kindAuth        = "auth"
	kindRateLimit   = "rate_limit"
	kindDailyLimit  = "daily_limit"
	kindBilling     = "billing"
	kindBadRequest  = "bad_request"
	kindTimeout     = "timeout"
	kindTransient   = "transient"
	kindBadResponse = "bad_response"
)

// failure is the outcome of classifying one failed upstream call.
type failure struct {
	kind   string
	status int
}

// badResponseError is a 200 that was unusable (empty text, JSON requested but
// not returned). The provider is healthy, so it gets no cooldown.
type badResponseError struct{ msg string }

func (e *badResponseError) Error() string { return e.msg }
func errBadResponse(msg string) error     { return &badResponseError{msg} }

const (
	maxCooldown       = 5 * time.Minute  // above this a 429 is treated as a daily limit
	baseBackoff       = 20 * time.Second // first transient failure
	maxBackoff        = 5 * time.Minute  // backoff ceiling
	defaultRateWindow = 30 * time.Second // 429 with no Retry-After hint
)

// allFailed builds the right error once every candidate has failed: if every
// attempt was rejected as a bad request, that is the caller's fault (HTTP 400),
// not a provider outage (HTTP 502).
func (r *Router) allFailed(attempts []types.Attempt) error {
	if len(attempts) > 0 {
		all := true
		for _, a := range attempts {
			if a.Kind != kindBadRequest {
				all = false
				break
			}
		}
		if all {
			return &types.BadRequestError{Message: attempts[0].Error, Attempts: attempts}
		}
	}
	return &types.AllFailedError{Attempts: attempts}
}

// observe records rate-limit headers a provider sent (on success OR failure)
// and persists any pause they imply.
func (r *Router) observe(ctx context.Context, c *candidate, h http.Header, now time.Time) {
	if h == nil {
		return
	}
	ri, ok := parseRateHeaders(h, now, c.cfg.BaseURL)
	if !ok {
		return
	}
	st := c.state
	st.mu.Lock()
	est := st.avgTokens
	if est == 0 {
		est = defaultTokenGuess
	}
	delta := st.learn(ri, now, est)
	until, why := st.deadUntil, st.deadReason
	st.mu.Unlock()
	if delta != 0 { // keep the stored daily total honest too (shared keys, restarts)
		_ = r.store.AddUsage(ctx, "_sync", c.cfg.ID, dayOf(c.cfg, now), delta, 0, 0)
	}
	if until.After(now) {
		_ = r.store.SetPause(ctx, c.cfg.ID, until, why)
	}
}

// isConfigBody reports whether a 400 body looks like a provider-side
// misconfiguration (wrong model id, unsupported json mode / system role)
// rather than a problem with the user's prompt.
func isConfigBody(low string) bool {
	for _, k := range []string{"response_format", "json_mode", "system role", "system message", "developer instruction", "unknown model", "model_not_found", "invalid model", "not a valid model", "does not exist", "unsupported", "failed_precondition", "free tier is not available"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

func isDailyBody(low string) bool {
	for _, k := range []string{"perday", "per day", "daily", "per_day", "requests per day", "rpd"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// backoff returns base*2^streak capped at maxBackoff.
func backoff(streak int) time.Duration {
	d := baseBackoff
	for i := 0; i < streak && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isNetworkErr(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "connection") || strings.Contains(low, "eof") || strings.Contains(low, "no such host")
}

// classify turns an upstream failure into the right penalty for that provider
// and tells the caller what kind of failure it was.
//
//	401 / 403 / 404          pause 24h   (key or model id is wrong)
//	402                      pause 1h    (out of credits / billing)
//	429 + daily hint         pause until the provider's local midnight
//	429                      cool down for Retry-After / retryDelay (else 30s)
//	400 / 413 / 422          NO penalty — it is the caller's request. Only a
//	                         400 that looks like misconfiguration counts, and
//	                         3 in a row (reset by any success) pause 24h.
//	408 / 5xx / timeout /    exponential backoff 20s → 40s → … → 5min,
//	network error            reset by the next success
//	unusable 200             no penalty (try the next provider)
func (r *Router) classify(ctx context.Context, c *candidate, err error) failure {
	state, cfg, now := c.state, c.cfg, r.now()
	state.mu.Lock()
	defer state.mu.Unlock()
	defer func() {
		// Persist long pauses so other processes / restarts respect them.
		if state.deadUntil.After(now) {
			_ = r.store.SetPause(ctx, cfg.ID, state.deadUntil, state.deadReason)
		}
	}()

	var he *types.HTTPError
	if !errors.As(err, &he) {
		var br *badResponseError
		switch {
		case errors.As(err, &br):
			return failure{kind: kindBadResponse}
		case isTimeout(err):
			state.coolUntil = now.Add(backoff(state.failStreak))
			state.failStreak++
			r.logf("provider %s timed out; backing off until %s", cfg.Name, state.coolUntil.Format(time.TimeOnly))
			return failure{kind: kindTimeout}
		case cfg.Kind == "ollama" || isNetworkErr(err):
			state.coolUntil = now.Add(backoff(state.failStreak))
			state.failStreak++
			return failure{kind: kindTransient}
		}
		return failure{kind: kindBadResponse}
	}

	low := strings.ToLower(he.Body)
	f := failure{status: he.Code}
	switch {
	case he.Code == 401 || he.Code == 403 || he.Code == 404:
		f.kind = kindAuth
		state.deadUntil, state.deadReason = now.Add(24*time.Hour), "HTTP "+strconv.Itoa(he.Code)+" (check key/model)"
		r.logf("provider %s paused for 24h: %s", cfg.Name, he.Error())
	case he.Code == 402:
		f.kind = kindBilling
		state.deadUntil, state.deadReason = now.Add(time.Hour), "HTTP 402 (out of credits / billing)"
		r.logf("provider %s paused for 1h: out of credits", cfg.Name)
	case he.Code == 429:
		ra := he.RetryAfter
		if ra == 0 {
			ra = retryDelayFromBody(he.Body)
		}
		if ra == 0 && strings.Contains(low, "1302") {
			ra = 2 * time.Second // Z.AI: "concurrency limit", the single slot frees as soon as the running call ends
		}
		ri, _ := parseRateHeaders(he.Header, now, cfg.BaseURL)
		headerDaily := ri.RemainingRequests == 0 && (ri.ResetRequests > maxCooldown || ri.RequestsWindow == "day" && ri.ResetRequests > 0)
		gDaily, gRetry, gOK := quotaDetail(he.Body)
		if ra == 0 && gRetry > 0 {
			ra = gRetry
		}
		bodyDaily := gDaily || !gOK && isDailyBody(low)
		if ra > maxCooldown || bodyDaily || headerDaily {
			f.kind = kindDailyLimit
			wait := untilReset(cfg, now) // the provider's own midnight (Gemini: Pacific time)
			if headerDaily && ri.ResetRequests > 0 {
				wait = ri.ResetRequests + time.Second // the provider told us exactly
			}
			state.deadUntil, state.deadReason = now.Add(wait), "daily limit"
			r.logf("provider %s hit its daily limit; paused until reset", cfg.Name)
		} else {
			f.kind = kindRateLimit
			if ra <= 0 {
				ra = defaultRateWindow
			}
			if ra < time.Second {
				ra = time.Second
			}
			state.coolUntil = now.Add(ra)
		}
	case he.Code == 400 || he.Code == 413 || he.Code == 422:
		f.kind = kindBadRequest
		if he.Code == 400 && isConfigBody(low) {
			state.bad400++
			if state.bad400 >= 3 {
				state.deadUntil, state.deadReason = now.Add(24*time.Hour), "repeated HTTP 400 (check model / json_mode / system_role)"
			}
		}
	default: // 408, 425, 5xx, 529 and anything unexpected
		f.kind = kindTransient
		state.coolUntil = now.Add(backoff(state.failStreak))
		state.failStreak++
	}
	return f
}
