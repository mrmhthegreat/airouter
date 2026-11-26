package router_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/sqlopen"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// This test answers: "can several PROCESSES share one SQLite file under load?"
// It starts real OS processes (the test binary re-executed), each with its own
// Router, all hitting one database file and one fake provider.
//
// What it checks:
//   - no "database is locked" errors and no failed requests
//   - usage counters in the file add up exactly (no lost updates)
//   - every process sees the same tenants and providers
//
// What it only REPORTS (it is a known limit, see docs): the per-minute limiter
// and worker slots live in each process's memory, so N processes together can
// exceed a provider's RPM by up to N times.

const mpEnv = "AIROUTER_MULTIPROC_WORKER"

func TestMultiProcessHelper(t *testing.T) {
	if os.Getenv(mpEnv) == "" {
		t.Skip("helper process only")
	}
	dbPath, url := os.Getenv("MP_DB"), os.Getenv("MP_URL")
	n, _ := strconv.Atoi(os.Getenv("MP_N"))
	threads, _ := strconv.Atoi(os.Getenv("MP_THREADS"))
	st, _, err := sqlopen.Store(dbPath, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	r, err := router.New(router.Options{Store: st, MaxWait: 2 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "router:", err)
		os.Exit(1)
	}
	_ = url
	var wg sync.WaitGroup
	var failed atomic.Int64
	for g := 0; g < threads; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				tenant := []string{"t1", "t2", "t3"}[(g+i)%3]
				if _, err := r.Generate(context.Background(), types.Request{TenantID: tenant, Prompt: "hi"}); err != nil {
					failed.Add(1)
					fmt.Fprintln(os.Stderr, "generate:", err)
				}
			}
		}(g)
	}
	wg.Wait()
	if failed.Load() > 0 && os.Getenv("MP_TOLERATE") == "" {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMultiProcessSharedSQLite(t *testing.T) {
	if os.Getenv(mpEnv) != "" {
		t.Skip()
	}
	if testing.Short() {
		t.Skip("slow: spawns processes")
	}
	const procs, threads, perThread = 4, 4, 60
	var served atomic.Int64
	var minuteMu sync.Mutex
	var perSecond = map[int64]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		minuteMu.Lock()
		perSecond[time.Now().Unix()]++
		minuteMu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":5}}`))
	}))
	defer fake.Close()

	dbPath := filepath.Join(t.TempDir(), "shared.db")
	st, _, err := sqlopen.Store(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.New(router.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"t1", "t2", "t3"} {
		if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SaveProvider(ctx, &types.ProviderConfig{Name: "up", Kind: "openai", BaseURL: fake.URL, APIKey: "k", Model: "m", Workers: 8}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	outs := make([]strings.Builder, procs)
	errs := make([]error, procs)
	start := time.Now()
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMultiProcessHelper$")
			cmd.Env = append(os.Environ(), mpEnv+"=1", "MP_DB="+dbPath, "MP_URL="+fake.URL,
				"MP_N="+strconv.Itoa(perThread), "MP_THREADS="+strconv.Itoa(threads))
			cmd.Stderr = &outs[p]
			errs[p] = cmd.Run()
		}(p)
	}
	wg.Wait()
	for p := 0; p < procs; p++ {
		if errs[p] != nil {
			t.Errorf("process %d failed: %v\n%s", p, errs[p], firstLines(outs[p].String(), 8))
		}
		if strings.Contains(outs[p].String(), "locked") {
			t.Errorf("process %d hit a lock error:\n%s", p, firstLines(outs[p].String(), 8))
		}
	}

	want := procs * threads * perThread
	if got := int(served.Load()); got != want {
		t.Errorf("provider received %d requests, want %d", got, want)
	}
	// every request must be counted exactly once in the shared file
	total := 0
	for _, id := range []string{"t1", "t2", "t3"} {
		u, err := st.TenantDayUsage(ctx, id, time.Now().UTC().Format("2006-01-02"))
		if err != nil {
			t.Fatal(err)
		}
		total += u
	}
	if total != want {
		t.Errorf("usage in the shared DB = %d, want %d (lost updates!)", total, want)
	}
	peak := 0
	for _, c := range perSecond {
		if c > peak {
			peak = c
		}
	}
	t.Logf("%d processes x %d threads x %d requests = %d requests in %v; peak %d req/s at the provider",
		procs, threads, perThread, want, time.Since(start).Round(time.Millisecond), peak)
}

func firstLines(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, "\n")
}

// KNOWN LIMIT, measured: the per-minute limiter lives in each process's memory.
// With a provider limited to 10 requests/minute, N processes sharing one DB
// together send up to N x 10. The daily counters ARE shared (they live in the
// DB), so daily quotas stay correct; only the short per-minute window is not.
// If your two services share one SQLite file, give each its own RPM budget
// (set rpm to the provider's limit divided by the number of processes) or run
// one owner process. This test only reports; it does not fail on the overshoot.
func TestMultiProcessRPMIsPerProcess(t *testing.T) {
	if os.Getenv(mpEnv) != "" || testing.Short() {
		t.Skip()
	}
	const procs, rpm = 3, 10
	var served atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":5}}`))
	}))
	defer fake.Close()
	dbPath := filepath.Join(t.TempDir(), "rpm.db")
	st, _, err := sqlopen.Store(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := router.New(router.Options{Store: st})
	ctx := context.Background()
	if _, _, err := r.CreateTenant(ctx, types.Tenant{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveProvider(ctx, &types.ProviderConfig{Name: "up", Kind: "openai", BaseURL: fake.URL, APIKey: "k", Model: "m", RPM: rpm, Workers: 4}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMultiProcessHelper$")
			cmd.Env = append(os.Environ(), mpEnv+"=1", "MP_DB="+dbPath, "MP_N=25", "MP_THREADS=1", "MP_TOLERATE=1")
			_ = cmd.Run()
		}()
	}
	wg.Wait()
	t.Logf("provider limit = %d/min; %d processes together sent %d requests inside one minute", rpm, procs, served.Load())
	if int(served.Load()) < rpm {
		t.Fatalf("served only %d", served.Load())
	}
}
