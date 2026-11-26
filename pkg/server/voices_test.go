package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/store"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// fakeElevenLabs is a tiny ElevenLabs account for HTTP tests.
func fakeElevenLabs(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	voices := []map[string]any{{"voice_id": "premade0000000000001", "name": "Rachel", "category": "premade"}}
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/elevenlabs.io")
		switch {
		case r.Method == "GET" && p == "/v2/voices":
			_ = json.NewEncoder(w).Encode(map[string]any{"voices": voices})
		case r.Method == "POST" && p == "/v1/voices/add":
			_ = r.ParseMultipartForm(1 << 20)
			n++
			id := fmt.Sprintf("clone%015d", n)
			voices = append(voices, map[string]any{"voice_id": id, "name": r.FormValue("name"), "category": "cloned"})
			_ = json.NewEncoder(w).Encode(map[string]any{"voice_id": id})
		case r.Method == "DELETE":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case r.Method == "POST" && strings.HasPrefix(p, "/v1/text-to-speech/"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(append([]byte("ID3"), make([]byte, 32)...))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func multipartBody(t *testing.T, fields map[string]string, files ...string) (*bytes.Buffer, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for i, f := range files {
		fw, _ := mw.CreateFormFile("files", fmt.Sprintf("s%d.mp3", i))
		_, _ = fw.Write([]byte(f))
	}
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

func call(t *testing.T, srv *httptest.Server, method, path, token string, body *bytes.Buffer, ct string) (int, map[string]any) {
	t.Helper()
	if body == nil {
		body = &bytes.Buffer{}
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestVoiceEndpoints(t *testing.T) {
	el := fakeElevenLabs(t)
	r, err := router.New(router.Options{Store: store.NewMemoryStore(), MaxWait: 50 * time.Millisecond,
		TenantPolicy: router.TenantPolicy{AllowSharedVoiceClone: true}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)

	keys := map[string]string{}
	for _, id := range []string{"alice", "bob"} {
		c, m, _ := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"`+id+`"}`)
		if c != 201 {
			t.Fatalf("tenant %s: %d", id, c)
		}
		keys[id] = m["api_key"].(string)
	}
	if c, _, b := do(t, srv, "POST", "/v1/providers", "admin",
		`{"preset":"elevenlabs","name":"el","base_url":"`+el.URL+`/elevenlabs.io","api_key":"k","model":"eleven_multilingual_v2","category":"tts"}`); c != 201 {
		t.Fatalf("provider: %d %s", c, b)
	}

	// no consent -> 400 and nothing is created
	body, ct := multipartBody(t, map[string]string{"name": "AliceVoice"}, "audio-bytes")
	if c, m := call(t, srv, "POST", "/v1/voices/clone", keys["alice"], body, ct); c != 400 || !strings.Contains(fmt.Sprint(m["error"]), "ConsentConfirmed") {
		t.Fatalf("no consent: %d %v", c, m)
	}
	// with consent -> 201
	body, ct = multipartBody(t, map[string]string{"name": "AliceVoice", "consent": "true", "labels": `{"gender":"female"}`}, "audio-bytes", "more-audio")
	c, v := call(t, srv, "POST", "/v1/voices/clone", keys["alice"], body, ct)
	if c != 201 || v["cloned"] != true || v["name"] != "AliceVoice" {
		t.Fatalf("clone: %d %v", c, v)
	}
	id := v["id"].(string)
	// bad labels JSON -> 400
	body, ct = multipartBody(t, map[string]string{"name": "X", "consent": "true", "labels": "nope"}, "a")
	if c, _ := call(t, srv, "POST", "/v1/voices/clone", keys["alice"], body, ct); c != 400 {
		t.Fatalf("bad labels: %d", c)
	}

	// alice sees 2 voices (Rachel + hers); bob sees only Rachel
	c, m := call(t, srv, "GET", "/v1/voices", keys["alice"], nil, "")
	if c != 200 || len(m["voices"].([]any)) != 2 {
		t.Fatalf("alice list: %d %v", c, m)
	}
	c, m = call(t, srv, "GET", "/v1/voices", keys["bob"], nil, "")
	if c != 200 || len(m["voices"].([]any)) != 1 {
		t.Fatalf("bob must not see alice's clone: %d %v", c, m)
	}
	if c, m := call(t, srv, "GET", "/v1/voices?search=rach", keys["bob"], nil, ""); c != 200 || len(m["voices"].([]any)) != 1 {
		t.Fatalf("search: %d %v", c, m)
	}

	// speak: alice by name works (mp3 native), bob gets 404 voice_not_found
	c, _, raw := do(t, srv, "POST", "/v1/speak?format=json", keys["alice"], `{"text":"hi","voice":"AliceVoice","format":"mp3"}`)
	if c != 200 || !strings.Contains(raw, `"format": "mp3"`) {
		t.Fatalf("alice speak: %d %s", c, raw)
	}
	if c, _, raw := do(t, srv, "POST", "/v1/speak", keys["bob"], `{"text":"hi","voice":"`+id+`"}`); c != 404 {
		t.Fatalf("bob speaking with alice's voice: %d %s", c, raw)
	}
	// OpenAI-compatible speech route carries the voice too
	if c, _, raw := do(t, srv, "POST", "/v1/audio/speech", keys["bob"], `{"model":"auto","input":"hi","voice":"AliceVoice"}`); c != 404 {
		t.Fatalf("openai route leaked the voice: %d %s", c, raw)
	}
	// delete: bob 404, alice 200 then gone
	if c, _ := call(t, srv, "DELETE", "/v1/voices/"+id, keys["bob"], nil, ""); c != 404 {
		t.Fatalf("bob delete: %d", c)
	}
	if c, m := call(t, srv, "DELETE", "/v1/voices/"+id, keys["alice"], nil, ""); c != 200 || m["deleted"] != true {
		t.Fatalf("alice delete: %d %v", c, m)
	}
	if c, _ := call(t, srv, "DELETE", "/v1/voices/"+id, keys["alice"], nil, ""); c != 404 {
		t.Fatalf("second delete: %d", c)
	}
	// no key -> 401
	if c, _ := call(t, srv, "GET", "/v1/voices", "", nil, ""); c != 401 {
		t.Fatalf("no key: %d", c)
	}
}

func TestPatchTenantAndOverview(t *testing.T) {
	up := fakeProvider(t, "hello")
	r := newTestRouter(t)
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	c, m, _ := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"acme","daily_request_limit":5}`)
	key := m["api_key"].(string)
	if c != 201 {
		t.Fatal(c)
	}
	do(t, srv, "POST", "/v1/providers", "admin", `{"preset":"openai","name":"up","base_url":"`+up.URL+`","api_key":"k","model":"m"}`)

	// partial update: only pool_only_cap changes, the limit stays 5
	c, m, b := do(t, srv, "PATCH", "/v1/tenants/acme", "admin", `{"pool_only_cap":true}`)
	if c != 200 || m["pool_only_cap"] != true || m["daily_request_limit"].(float64) != 5 {
		t.Fatalf("patch: %d %s", c, b)
	}
	if c, _, _ := do(t, srv, "PATCH", "/v1/tenants/nope", "admin", `{"disabled":true}`); c != 404 {
		t.Fatalf("unknown tenant: %d", c)
	}
	if c, _, _ := do(t, srv, "PATCH", "/v1/tenants/acme", key, `{"daily_request_limit":9999}`); c != 401 {
		t.Fatalf("a tenant must not raise its own cap: %d", c)
	}

	do(t, srv, "POST", "/v1/generate", key, `{"prompt":"x"}`)
	c, m, b = do(t, srv, "GET", "/v1/overview", key, "")
	if c != 200 {
		t.Fatalf("overview: %d %s", c, b)
	}
	cp := m["cap"].(map[string]any)
	if cp["limit"].(float64) != 5 || cp["used"].(float64) != 1 || cp["remaining"].(float64) != 4 || cp["pool_only"] != true || cp["resets_in_seconds"].(float64) <= 0 {
		t.Fatalf("cap block: %v", cp)
	}
	if m["usage"] == nil || len(m["shared"].([]any)) != 1 || m["server_time"] == nil {
		t.Fatalf("overview is missing parts: %s", b)
	}
	if strings.Contains(b, `"k"`) || strings.Contains(b, up.URL) {
		t.Fatalf("overview leaks provider key or url: %s", b)
	}

	// disable via PATCH -> generate is refused
	do(t, srv, "PATCH", "/v1/tenants/acme", "admin", `{"disabled":true}`)
	if c, _, _ := do(t, srv, "POST", "/v1/generate", key, `{"prompt":"x"}`); c != 403 {
		t.Fatalf("disabled tenant: %d", c)
	}
	_ = types.DefaultTenantID
}

func TestMaxWaitParam(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "5": 5 * time.Second, "1500ms": 1500 * time.Millisecond, "abc": 0, "-3": 0, "9999": 2 * time.Minute} {
		r := httptest.NewRequest("POST", "/v1/generate?max_wait="+in, nil)
		if got := maxWaitParam(r); got != want {
			t.Errorf("max_wait=%q: got %v want %v", in, got, want)
		}
	}
}

func TestShareVoiceEndpoint(t *testing.T) {
	el := fakeElevenLabs(t)
	r, err := router.New(router.Options{Store: store.NewMemoryStore(), MaxWait: 50 * time.Millisecond,
		TenantPolicy: router.TenantPolicy{AllowSharedVoiceClone: true}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(r, "admin"))
	t.Cleanup(srv.Close)
	_, m, _ := do(t, srv, "POST", "/v1/tenants", "admin", `{"id":"alice"}`)
	alice := m["api_key"].(string)
	if c, _, b := do(t, srv, "POST", "/v1/providers", "admin",
		`{"preset":"elevenlabs","name":"el","base_url":"`+el.URL+`/elevenlabs.io","api_key":"k","model":"eleven_multilingual_v2","category":"tts"}`); c != 201 {
		t.Fatalf("provider: %d %s", c, b)
	}
	// the operator clones as the default tenant (admin token, no X-Tenant-ID)
	body, ct := multipartBody(t, map[string]string{"name": "HouseVoice", "consent": "true"}, "audio")
	c, v := call(t, srv, "POST", "/v1/voices/clone", "admin", body, ct)
	if c != 201 {
		t.Fatalf("operator clone: %d %v", c, v)
	}
	id := v["id"].(string)
	listed := func(token string) (found bool, shared any) {
		_, m := call(t, srv, "GET", "/v1/voices", token, nil, "")
		for _, x := range m["voices"].([]any) {
			if vv := x.(map[string]any); vv["id"] == id {
				return true, vv["shared"]
			}
		}
		return false, nil
	}
	if found, _ := listed(alice); found {
		t.Fatal("a new clone must be private")
	}
	// only the admin token may share
	if c, _ := call(t, srv, "POST", "/v1/voices/"+id+"/share", alice, bytes.NewBufferString(`{"shared":true}`), "application/json"); c != 401 {
		t.Fatalf("tenant key must not share: %d", c)
	}
	for name, tc := range map[string]struct {
		id, body string
		code     int
	}{"no body": {id, ``, 400}, "no field": {id, `{}`, 400}, "unknown voice": {"nosuchvoice0000000000", `{"shared":true}`, 404}} {
		if c, _ := call(t, srv, "POST", "/v1/voices/"+tc.id+"/share", "admin", bytes.NewBufferString(tc.body), "application/json"); c != tc.code {
			t.Errorf("%s: got %d want %d", name, c, tc.code)
		}
	}
	if c, m := call(t, srv, "POST", "/v1/voices/"+id+"/share", "admin", bytes.NewBufferString(`{"shared":true}`), "application/json"); c != 200 || m["shared"] != true {
		t.Fatalf("share: %d %v", c, m)
	}
	if found, sh := listed(alice); !found || sh != true {
		t.Fatalf("alice should now see the shared voice with shared=true: %v %v", found, sh)
	}
	if c, _, raw := do(t, srv, "POST", "/v1/speak?format=json", alice, `{"text":"hi","voice":"HouseVoice","format":"mp3"}`); c != 200 {
		t.Fatalf("alice speaking with the shared voice: %d %s", c, raw)
	}
	if c, _ := call(t, srv, "POST", "/v1/voices/"+id+"/share", "admin", bytes.NewBufferString(`{"shared":false}`), "application/json"); c != 200 {
		t.Fatal("unshare")
	}
	if found, _ := listed(alice); found {
		t.Fatal("alice still sees an unshared voice")
	}
}
