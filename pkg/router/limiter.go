package router

import (
        "context"
        "sync"
        "time"

        "github.com/you/airouter/pkg/types"
)

// requestHit is one request recorded in the rolling 60-second window used to
// enforce RPM and TPM. The token count is filled in when the response arrives.
type requestHit struct {
        at     time.Time
        tokens int
}

// audioHit is one TTS/STT request recorded in the rolling 1-hour window used
// to enforce ASH (audio seconds per hour). The seconds count is filled in when
// the response arrives.
type audioHit struct {
        at   time.Time
        secs int
}

// providerState is the live (in-memory) counter state of one provider entry.
// Daily counters are hydrated from the Store, so restarts don't forget usage.
type providerState struct {
        mu            sync.Mutex
        hits          []requestHit // requests in the last 60s (rpm / tpm window)
        audioHits     []audioHit   // audio requests in the last 1h (ash window)
        day           string
        requestsToday int
        tokensToday   int
        audioSecToday int       // asd counter (resets at provider-local midnight)
        avgAudioSecs  int       // running average of audio seconds per request
        coolUntil     time.Time // short pause (per-minute 429, upstream hiccup)
        deadUntil     time.Time // long pause (daily limit until reset, bad key)
        deadReason    string
        inflight      int
        avgTokens     int
        bad400        int
        loaded        bool // pause state read from the store
}

// locFor returns the time.Location for a zone name, or UTC if unknown.
func locFor(tz string) *time.Location {
        if l, err := time.LoadLocation(tz); err == nil {
                return l
        }
        return time.UTC
}

// dayOf is the quota "day" of provider p at time t, in the provider's own reset
// timezone. Two requests on the same provider-day share the same RPD counter.
func dayOf(p types.ProviderConfig, t time.Time) string {
        return t.In(locFor(p.ResetTZ)).Format("2006-01-02")
}

// untilReset is the time left until p's daily quota resets (just after local
// midnight in the provider's reset timezone).
func untilReset(p types.ProviderConfig, t time.Time) time.Duration {
        now := t.In(locFor(p.ResetTZ))
        next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 1, 0, 0, now.Location())
        return next.Sub(now)
}

// defaultTokenGuess is used to estimate TPM usage before the response arrives.
const defaultTokenGuess = 1500

// defaultAudioSecGuess is used to estimate ASH/ASD usage before the response
// arrives. Picked as a rough midpoint between a short TTS clip (~5s) and a
// short STT clip (~30s); the real value is recorded in releaseAudio.
const defaultAudioSecGuess = 15

// hydrate loads persisted state into st. Caller holds st.mu.
func (r *Router) hydrate(ctx context.Context, p types.ProviderConfig, st *providerState, now time.Time) {
        if !st.loaded {
                st.loaded = true
                if until, why, _ := r.store.GetPause(ctx, p.ID); until.After(now) {
                        st.deadUntil, st.deadReason = until, why
                }
        }
        if day := dayOf(p, now); st.day != day {
                // New day in the provider's timezone → counters reset to whatever the
                // store has accumulated for the new day (usually 0).
                req, tok, _ := r.store.ProviderDayUsage(ctx, p.ID, day)
                st.day, st.requestsToday, st.tokensToday = day, req, tok
                st.audioSecToday = 0 // store doesn't persist audio seconds separately; resets each day
        }
}

// acquire checks every limit and, if the provider can take a request now,
// reserves a slot. When it can't, it returns how long to wait and whether the
// block is "daily" (a hard pause until reset) or transient.
//
// Limits that are 0 are not checked (0 = no cap). For audio providers (TTS/STT),
// ASH and ASD apply alongside RPM/RPD. TPM only applies to chat and TTS (where
// there are text tokens); for STT the TPM is the transcription output token
// count and is checked if set.
func (r *Router) acquire(ctx context.Context, p types.ProviderConfig, st *providerState, now time.Time) (ok bool, wait time.Duration, daily bool, reason string) {
        st.mu.Lock()
        defer st.mu.Unlock()

        r.hydrate(ctx, p, st, now)
        if now.Before(st.deadUntil) {
                return false, st.deadUntil.Sub(now), true, st.deadReason
        }
        if p.RPD > 0 && st.requestsToday >= p.RPD {
                return false, untilReset(p, now), true, "daily request limit reached"
        }
        if now.Before(st.coolUntil) {
                return false, st.coolUntil.Sub(now), false, "cooling down"
        }
        if st.inflight >= p.Workers {
                return false, 250 * time.Millisecond, false, "busy"
        }
        // RPM check: rolling 60-second window of request count.
        cutoff := now.Add(-time.Minute)
        kept := st.hits[:0]
        for _, h := range st.hits {
                if h.at.After(cutoff) {
                        kept = append(kept, h)
                }
        }
        st.hits = kept
        if p.RPM > 0 && len(st.hits) >= maxInt(1, p.RPM*9/10) {
                return false, st.hits[0].at.Add(time.Minute).Sub(now), false, "per-minute request limit"
        }
        // TPM check: rolling 60-second window of token count (only if TPM is set).
        if p.TPM > 0 && len(st.hits) > 0 {
                est := st.avgTokens
                if est == 0 {
                        est = defaultTokenGuess
                }
                sum := 0
                for _, h := range st.hits {
                        sum += h.tokens
                }
                if sum+est > p.TPM*9/10 {
                        return false, st.hits[0].at.Add(time.Minute).Sub(now), false, "per-minute token limit"
                }
        }
        // ASH check: rolling 1-hour window of audio seconds (TTS/STT only).
        if p.ASH > 0 {
                hourCutoff := now.Add(-time.Hour)
                keptAudio := st.audioHits[:0]
                sumSecs := 0
                for _, h := range st.audioHits {
                        if h.at.After(hourCutoff) {
                                keptAudio = append(keptAudio, h)
                                sumSecs += h.secs
                        }
                }
                st.audioHits = keptAudio
                est := st.avgAudioSecs
                if est == 0 {
                        est = defaultAudioSecGuess
                }
                if sumSecs+est > p.ASH*9/10 {
                        // Wait until the oldest audio hit ages out of the 1-hour window.
                        var wait time.Duration
                        if len(st.audioHits) > 0 {
                                wait = st.audioHits[0].at.Add(time.Hour).Sub(now)
                        } else {
                                wait = time.Hour
                        }
                        return false, wait, false, "per-hour audio seconds limit"
                }
        }
        // ASD check: daily audio seconds total (TTS/STT only).
        if p.ASD > 0 {
                est := st.avgAudioSecs
                if est == 0 {
                        est = defaultAudioSecGuess
                }
                if st.audioSecToday+est > p.ASD*9/10 {
                        return false, untilReset(p, now), true, "daily audio seconds limit"
                }
        }
        // Reserve the slot. The est token count is refined in release() when the
        // real response arrives.
        est := st.avgTokens
        if est == 0 {
                est = defaultTokenGuess
        }
        st.inflight++
        st.requestsToday++
        st.hits = append(st.hits, requestHit{now, est})
        return true, 0, false, ""
}

// release frees the slot and books the real token usage once the response is in.
// Call with tokens=0 for TTS/STT (no text tokens to record).
func (st *providerState) release(tokens int) {
        st.mu.Lock()
        defer st.mu.Unlock()
        st.inflight--
        if tokens > 0 {
                if n := len(st.hits); n > 0 {
                        st.hits[n-1].tokens = tokens
                }
                st.tokensToday += tokens
                if st.avgTokens == 0 {
                        st.avgTokens = tokens
                } else {
                        st.avgTokens = (st.avgTokens*7 + tokens*3) / 10
                }
        }
}

// releaseAudio books the real audio seconds used by a TTS or STT request.
// Called by Speak and Transcribe after the response is in. Pass secs=0 to
// skip (e.g. on error).
func (st *providerState) releaseAudio(secs int) {
        if secs <= 0 {
                return
        }
        st.mu.Lock()
        defer st.mu.Unlock()
        st.audioSecToday += secs
        st.audioHits = append(st.audioHits, audioHit{at: time.Now(), secs: secs})
        if st.avgAudioSecs == 0 {
                st.avgAudioSecs = secs
        } else {
                st.avgAudioSecs = (st.avgAudioSecs*7 + secs*3) / 10
        }
}

// ResolveKey is exposed as a convenience for callers who have a types.ProviderConfig
// but not the types package; the router itself calls p.ResolveKey() directly.
func ResolveKey(p types.ProviderConfig) string { return p.ResolveKey() }

func maxInt(a, b int) int {
        if a > b {
                return a
        }
        return b
}
