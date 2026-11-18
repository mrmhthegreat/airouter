package router

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

func status(code int, hdr map[string]string, body string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func stateOf(r *Router, name string) string {
	for _, s := range r.Status(context.Background(), "") {
		if s.Name == name {
			return s.State
		}
	}
	return "?"
}

// A user's bad prompt (400) must never take healthy providers offline.
func TestBadRequestDoesNotPoisonProviders(t *testing.T) {
	bad, hits := fakeProvider(t, status(400, nil, `{"error":"maximum context length exceeded"}`))
	r := newRouter(t, nil)
	add(t, r, "a", bad.URL, 10, 0, nil)
	for i := 0; i < 10; i++ {
		_, err := gen(r, types.Request{})
		if !errors.Is(err, types.ErrBadRequest) {
			t.Fatalf("want ErrBadRequest (HTTP 400 to the caller), got %v", err)
		}
	}
	if got := stateOf(r, "a"); got != "ok" {
		t.Fatalf("provider state after 10 user-side 400s = %q, want ok", got)
	}
	if atomic.LoadInt32(hits) != 10 {
		t.Fatalf("provider should still be tried every time, hits=%d", atomic.LoadInt32(hits))
	}
}

// A 400 that only one provider gives is not the caller's fault: fail over.
func TestOne400FailsOverToNextProvider(t *testing.T) {
	bad, _ := fakeProvider(t, status(400, nil, `{"error":"response_format unsupported"}`))
	good, _ := fakeProvider(t, alwaysOK("fine"))
	r := newRouter(t, nil)
	add(t, r, "a", bad.URL, 10, 0, nil)
	add(t, r, "b", good.URL, 20, 0, nil)
	resp, err := gen(r, types.Request{})
	if err != nil || resp.Text != "fine" || resp.Attempts[0].Kind != "bad_request" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestBillingPausesLongAndAuthPauses24h(t *testing.T) {
	pay, _ := fakeProvider(t, status(402, nil, `{"error":"no credits"}`))
	auth, _ := fakeProvider(t, status(401, nil, `{"error":"bad key"}`))
	good, _ := fakeProvider(t, alwaysOK("ok"))
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := newRouter(t, clk)
	add(t, r, "pay", pay.URL, 1, 0, nil)
	add(t, r, "auth", auth.URL, 2, 0, nil)
	add(t, r, "good", good.URL, 3, 0, nil)
	if resp, err := gen(r, types.Request{}); err != nil || resp.Provider != "good" {
		t.Fatalf("%v %v", resp, err)
	}
	clk.t = clk.t.Add(30 * time.Minute)
	if s := stateOf(r, "pay"); s == "ok" {
		t.Fatalf("402 provider should still be paused after 30m, state=%q", s)
	}
	clk.t = clk.t.Add(45 * time.Minute)
	if s := stateOf(r, "pay"); s != "ok" && s != "cooling down" {
		t.Fatalf("402 pause should have lifted after ~1h, state=%q", s)
	}
	if s := stateOf(r, "auth"); s == "ok" {
		t.Fatalf("401 provider should be paused 24h, state=%q", s)
	}
}

// Retry-After is honoured instead of a fixed 30s; Gemini's body hint too.
func TestRetryAfterAndGeminiRetryDelayAreHonoured(t *testing.T) {
	ra, _ := fakeProvider(t, status(429, map[string]string{"Retry-After": "3"}, `{}`))
	gm, _ := fakeProvider(t, status(429, nil, `{"error":{"details":[{"retryDelay":"7s"}]}}`))
	good, _ := fakeProvider(t, alwaysOK("ok"))
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := newRouter(t, clk)
	add(t, r, "ra", ra.URL, 1, 0, nil)
	add(t, r, "gm", gm.URL, 2, 0, nil)
	add(t, r, "good", good.URL, 3, 0, nil)
	if _, err := gen(r, types.Request{}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(4 * time.Second)
	if s := stateOf(r, "ra"); s != "ok" {
		t.Fatalf("Retry-After: 3 should be over after 4s, state=%q", s)
	}
	if s := stateOf(r, "gm"); s != "cooling down" {
		t.Fatalf("retryDelay 7s should still apply after 4s, state=%q", s)
	}
	clk.t = clk.t.Add(4 * time.Second)
	if s := stateOf(r, "gm"); s != "ok" {
		t.Fatalf("after 8s gm should be ok, state=%q", s)
	}
}

// A hanging provider gets a cooldown instead of being retried (and waited on) every request.
func TestTimeoutGetsBackoff(t *testing.T) {
	slow, hits := fakeProvider(t, func(w http.ResponseWriter, _ map[string]any) { time.Sleep(300 * time.Millisecond) })
	good, _ := fakeProvider(t, alwaysOK("ok"))
	r := newRouter(t, nil)
	r.attemptTimeout = 50 * time.Millisecond
	add(t, r, "slow", slow.URL, 1, 0, nil)
	add(t, r, "good", good.URL, 2, 0, nil)
	for i := 0; i < 3; i++ {
		if resp, err := gen(r, types.Request{}); err != nil || resp.Provider != "good" {
			t.Fatalf("%v %v", resp, err)
		}
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("slow provider should be skipped after its first timeout, hits=%d", atomic.LoadInt32(hits))
	}
}

// Headers tell us a provider is out BEFORE we earn a 429.
func TestRateHeadersCauseProactivePause(t *testing.T) {
	hdr := map[string]string{"x-ratelimit-remaining-requests": "0", "x-ratelimit-reset-requests": "2h", "x-ratelimit-limit-requests": "1000"}
	a, hitsA := fakeProvider(t, func(w http.ResponseWriter, b map[string]any) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		_, _ = w.Write([]byte(okBody("last one")))
	})
	b, _ := fakeProvider(t, alwaysOK("from b"))
	r := newRouter(t, nil)
	add(t, r, "a", a.URL, 1, 0, nil)
	add(t, r, "b", b.URL, 2, 0, nil)
	if resp, _ := gen(r, types.Request{}); resp.Text != "last one" {
		t.Fatal("first call should use a")
	}
	if resp, _ := gen(r, types.Request{}); resp.Provider != "b" || atomic.LoadInt32(hitsA) != 1 {
		t.Fatalf("a reported 0 remaining; next call must go to b without hitting a (hits=%d)", *hitsA)
	}
}

func TestParseHelpers(t *testing.T) {
	for in, want := range map[string]time.Duration{"7.66s": 7660 * time.Millisecond, "2m59.56s": 179560 * time.Millisecond, "1h2m": 62 * time.Minute, "42": 42 * time.Second, "1d": 24 * time.Hour} {
		if got := parseResetDuration(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d < 89*time.Second || d > 91*time.Second {
		t.Errorf("http-date retry-after: %v", d)
	}
}

func withHeaders(h map[string]string) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) {
		for k, v := range h {
			w.Header().Set(k, v)
		}
		_, _ = w.Write([]byte(okBody("ok")))
	}
}

func statusRow(t *testing.T, r *Router, name string) ProviderStatus {
	t.Helper()
	for _, s := range r.Status(context.Background(), "") {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no provider %s", name)
	return ProviderStatus{}
}

// Groq-style headers: the provider's numbers replace the configured ones and
// correct our counter (e.g. another app shares the same key).
func TestGroqHeadersOverrideConfigAndSyncCounter(t *testing.T) {
	DailyRequestHostHints = append(DailyRequestHostHints, "127.0.0.1")
	t.Cleanup(func() { DailyRequestHostHints = DailyRequestHostHints[:len(DailyRequestHostHints)-1] })
	up, _ := fakeProvider(t, withHeaders(map[string]string{
		"x-ratelimit-limit-requests": "14400", "x-ratelimit-remaining-requests": "14370", "x-ratelimit-reset-requests": "2m59.56s",
		"x-ratelimit-limit-tokens": "18000", "x-ratelimit-remaining-tokens": "17997", "x-ratelimit-reset-tokens": "7.66s",
	}))
	r := newRouter(t, nil)
	add(t, r, "g", up.URL, 1, 1000, nil) // catalog says 1000/day
	if _, err := gen(r, types.Request{}); err != nil {
		t.Fatal(err)
	}
	s := statusRow(t, r, "g")
	if s.RPD != 14400 || s.TPM != 18000 || s.LimitsSource != "reported" {
		t.Fatalf("limits not taken from provider: rpd=%d tpm=%d src=%s", s.RPD, s.TPM, s.LimitsSource)
	}
	if s.UsedToday != 30 || s.RemainingToday == nil || *s.RemainingToday != 14370 {
		t.Fatalf("counter not synced to provider: used=%d rem=%v", s.UsedToday, s.RemainingToday)
	}
	if s.Upstream == nil || s.Upstream.RequestsWindow != "day" {
		t.Fatalf("upstream info: %+v", s.Upstream)
	}
}

// Provider says almost no tokens are left this minute: skip it until its own reset.
func TestLowRemainingTokensSkipsUntilReset(t *testing.T) {
	a, hitsA := fakeProvider(t, withHeaders(map[string]string{"x-ratelimit-remaining-tokens": "10", "x-ratelimit-reset-tokens": "30s", "x-ratelimit-limit-tokens": "6000"}))
	b, _ := fakeProvider(t, alwaysOK("b"))
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	r := newRouter(t, clk)
	add(t, r, "a", a.URL, 1, 0, nil)
	add(t, r, "b", b.URL, 2, 0, nil)
	_, _ = gen(r, types.Request{})
	if resp, _ := gen(r, types.Request{}); resp.Provider != "b" {
		t.Fatal("should skip a while it reports no tokens left")
	}
	clk.t = clk.t.Add(35 * time.Second)
	if resp, _ := gen(r, types.Request{}); resp.Provider != "a" || atomic.LoadInt32(hitsA) != 2 {
		t.Fatalf("after its reset time a should be used again (hits=%d)", atomic.LoadInt32(hitsA))
	}
}

// Gemini: the structured 429 body says which quota was hit.
func TestGeminiQuotaIdDecidesDailyVersusMinute(t *testing.T) {
	day := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"20s"}]}}`
	min := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]}}`
	d, _ := fakeProvider(t, status(429, nil, day))
	m, _ := fakeProvider(t, status(429, nil, min))
	ok, _ := fakeProvider(t, alwaysOK("ok"))
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)} // 04:00 Pacific
	r := newRouter(t, clk)
	pt := func(p *types.ProviderConfig) { p.ResetTZ = "America/Los_Angeles" }
	add(t, r, "day", d.URL, 1, 0, pt)
	add(t, r, "min", m.URL, 2, 0, pt)
	add(t, r, "ok", ok.URL, 3, 0, nil)
	if _, err := gen(r, types.Request{}); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(r, "day"); s != "paused: daily limit" {
		t.Fatalf("PerDay quota should park the provider until Pacific midnight, state=%q", s)
	}
	if row := statusRow(t, r, "day"); row.ResetsInSec < 19*3600 || row.ResetsInSec > 20*3600+120 {
		t.Fatalf("reset should be ~20h away (04:00 PT -> 00:00 PT), got %ds", row.ResetsInSec)
	}
	clk.t = clk.t.Add(8 * time.Second)
	if s := stateOf(r, "min"); s != "ok" {
		t.Fatalf("PerMinute quota with retryDelay 7s should be over after 8s, state=%q", s)
	}
}
