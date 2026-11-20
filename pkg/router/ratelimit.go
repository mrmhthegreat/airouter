package router

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// RateInfo is what a provider told us about its own limits in response
// headers. Fields are -1 when the provider did not send them.
//
// Header families understood (all optional, best-effort):
//
//	Groq / OpenAI / Mistral style:  x-ratelimit-{limit,remaining,reset}-{requests,tokens}
//	Cerebras style:                 x-ratelimit-{limit,remaining,reset}-{requests-day,tokens-minute}
//	Everyone:                       retry-after (seconds or an HTTP date)
//
// Gemini's OpenAI-compatible endpoint sends no such headers; its 429 body
// carries a "retryDelay" instead (see retryDelayFromBody).
// DailyRequestHostHints lists upstream hosts whose plain
// x-ratelimit-*-requests headers describe a PER-DAY window (Groq). For every
// other host the plain names are treated as per-minute (OpenAI style), and the
// explicit "-day" / "-minute" header names (Cerebras style) are always trusted.
// Append to it if you use another provider that follows Groq's convention.
var DailyRequestHostHints = []string{"groq.com"}

type RateInfo struct {
	// RequestsWindow says what LimitRequests/RemainingRequests count: "day",
	// "minute", or "" when unknown (then they are only used for pausing).
	RequestsWindow    string        `json:"requests_window,omitempty"`
	LimitRequests     int           `json:"limit_requests"`
	RemainingRequests int           `json:"remaining_requests"`
	ResetRequests     time.Duration `json:"-"`
	LimitTokens       int           `json:"limit_tokens"`
	RemainingTokens   int           `json:"remaining_tokens"`
	ResetTokens       time.Duration `json:"-"`
	ResetRequestsSec  int64         `json:"reset_requests_seconds"`
	ResetTokensSec    int64         `json:"reset_tokens_seconds"`
	At                time.Time     `json:"as_of"`
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return int(f)
}

var durPart = regexp.MustCompile(`(\d+(?:\.\d+)?)(ms|d|h|m|s)`)

// parseResetDuration accepts "7.66s", "2m59.56s", "1h2m", "1d", "250ms" or a
// bare number of seconds ("42").
func parseResetDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(f * float64(time.Second))
	}
	var total time.Duration
	found := false
	for _, m := range durPart.FindAllStringSubmatch(s, -1) {
		f, _ := strconv.ParseFloat(m[1], 64)
		unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		total += time.Duration(f * float64(unit))
		found = true
	}
	if !found {
		return -1
	}
	return total
}

// parseRetryAfter reads Retry-After as seconds or as an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

var retryDelayRe = regexp.MustCompile(`"retryDelay"\s*:\s*"([0-9.]+)s"`)

// retryDelayFromBody pulls Gemini's "retryDelay": "34s" out of a 429 body.
func retryDelayFromBody(body string) time.Duration {
	if m := retryDelayRe.FindStringSubmatch(body); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 0
}

// parseRateHeaders extracts RateInfo; ok is false when nothing useful was sent.
func parseRateHeaders(h http.Header, now time.Time, baseURL string) (RateInfo, bool) {
	ri := RateInfo{
		LimitRequests:     atoiOr(firstHeader(h, "x-ratelimit-limit-requests", "x-ratelimit-limit-requests-day"), -1),
		RemainingRequests: atoiOr(firstHeader(h, "x-ratelimit-remaining-requests", "x-ratelimit-remaining-requests-day"), -1),
		ResetRequests:     parseResetDuration(firstHeader(h, "x-ratelimit-reset-requests", "x-ratelimit-reset-requests-day")),
		LimitTokens:       atoiOr(firstHeader(h, "x-ratelimit-limit-tokens", "x-ratelimit-limit-tokens-minute"), -1),
		RemainingTokens:   atoiOr(firstHeader(h, "x-ratelimit-remaining-tokens", "x-ratelimit-remaining-tokens-minute"), -1),
		ResetTokens:       parseResetDuration(firstHeader(h, "x-ratelimit-reset-tokens", "x-ratelimit-reset-tokens-minute")),
		At:                now,
	}
	switch {
	case h.Get("x-ratelimit-limit-requests-day") != "" || h.Get("x-ratelimit-remaining-requests-day") != "":
		ri.RequestsWindow = "day"
	case h.Get("x-ratelimit-limit-requests-minute") != "":
		ri.RequestsWindow = "minute"
	case ri.LimitRequests >= 0 || ri.RemainingRequests >= 0:
		ri.RequestsWindow = "minute"
		for _, host := range DailyRequestHostHints {
			if strings.Contains(baseURL, host) {
				ri.RequestsWindow = "day"
			}
		}
	}
	ok := ri.LimitRequests >= 0 || ri.RemainingRequests >= 0 || ri.LimitTokens >= 0 || ri.RemainingTokens >= 0
	if ri.ResetRequests > 0 {
		ri.ResetRequestsSec = int64(ri.ResetRequests.Seconds())
	}
	if ri.ResetTokens > 0 {
		ri.ResetTokensSec = int64(ri.ResetTokens.Seconds())
	}
	return ri, ok
}

// learn records what a provider reported about its own limits and makes the
// router follow it. The provider is the source of truth, because it also sees
// usage from OTHER apps or processes sharing the same key:
//
//   - a per-day request window overwrites our local daily counter and replaces
//     the configured RPD with the real limit (so a paid tier or a changed free
//     tier needs no config edit);
//   - the token limit replaces the configured TPM;
//   - "0 remaining" pauses the provider until its own reported reset time.
//
// It returns how much the daily request counter moved so the caller can persist
// the correction. Caller must hold st.mu.
func (st *providerState) learn(ri RateInfo, now time.Time, estTokens int) (delta int) {
	cp := ri
	st.upstream = &cp
	if ri.RequestsWindow == "day" && ri.LimitRequests > 0 && ri.RemainingRequests >= 0 {
		st.learnedRPD = ri.LimitRequests
		want := ri.LimitRequests - ri.RemainingRequests + st.inflight
		if want != st.requestsToday {
			delta, st.requestsToday = want-st.requestsToday, want
		}
	}
	if ri.LimitTokens > 0 {
		st.learnedTPM = ri.LimitTokens
	}
	if ri.RemainingRequests == 0 && ri.ResetRequests > 0 {
		if ri.ResetRequests > 5*time.Minute { // a long window: park until it resets
			st.deadUntil, st.deadReason = now.Add(ri.ResetRequests+time.Second), "limit reached (reported by provider)"
		} else if until := now.Add(ri.ResetRequests + 200*time.Millisecond); until.After(st.coolUntil) {
			st.coolUntil = until
		}
	}
	if ri.RemainingTokens >= 0 && ri.RemainingTokens < estTokens && ri.ResetTokens > 0 && ri.ResetTokens <= 5*time.Minute {
		if until := now.Add(ri.ResetTokens + 200*time.Millisecond); until.After(st.coolUntil) {
			st.coolUntil = until
		}
	}
	return delta
}

// effRPD / effTPM: the limit actually enforced. A limit the provider reported
// beats the configured one.
func (st *providerState) effRPD(p types.ProviderConfig) int {
	if st.learnedRPD > 0 {
		return st.learnedRPD
	}
	return p.RPD
}

func (st *providerState) effTPM(p types.ProviderConfig) int {
	if st.learnedTPM > 0 {
		return st.learnedTPM
	}
	return p.TPM
}

// quotaDetail digs the structured part out of a Google-style 429 body:
//
//	{"error":{"status":"RESOURCE_EXHAUSTED","details":[
//	  {"@type":".../google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},
//	  {"@type":".../google.rpc.RetryInfo","retryDelay":"34s"}]}}
//
// daily is true when any violated quota is a PerDay quota.
func quotaDetail(body string) (daily bool, retry time.Duration, ok bool) {
	var env struct {
		Error struct {
			Details []struct {
				Violations []struct {
					QuotaID string `json:"quotaId"`
				} `json:"violations"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &env) != nil {
		return false, 0, false
	}
	for _, d := range env.Error.Details {
		for _, v := range d.Violations {
			ok = true
			if strings.Contains(strings.ToLower(v.QuotaID), "perday") {
				daily = true
			}
		}
		if d.RetryDelay != "" {
			if f, err := strconv.ParseFloat(strings.TrimSuffix(d.RetryDelay, "s"), 64); err == nil {
				retry, ok = time.Duration(f*float64(time.Second)), true
			}
		}
	}
	return daily, retry, ok
}
