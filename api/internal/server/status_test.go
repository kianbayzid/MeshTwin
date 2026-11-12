package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"meshtwin/api/internal/store"
)

// fakeProm answers instant queries from values (PromQL -> sample string; a
// missing query gets an empty vector) and records every query it receives.
type fakeProm struct {
	mu      sync.Mutex
	queries []string
	values  map[string]string
	handler http.HandlerFunc // overrides the normal answer when set (use setHandler once serving)
	gate    chan struct{}    // when set, every query waits for it to close
}

func (f *fakeProm) setHandler(h http.HandlerFunc) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func (f *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	handler := f.handler
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	if r.URL.Path != "/prom/api/v1/query" {
		http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
		return
	}
	if handler != nil {
		handler(w, r)
		return
	}
	result := "[]"
	if v, ok := f.values[q]; ok {
		result = fmt.Sprintf(`[{"metric":{},"value":[1762896600.123,%q]}]`, v)
	}
	_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":%s}}`, result)
}

func (f *fakeProm) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

var asOf = time.Date(2026, 11, 11, 21, 30, 0, 0, time.UTC)

// newStatusServer returns an API whose /status reads from fake (mounted
// under /prom to check a base path is kept).
func newStatusServer(t *testing.T, fake *fakeProm) (*Server, *httptest.Server) {
	t.Helper()
	prom := httptest.NewServer(fake)
	t.Cleanup(prom.Close)
	s := New(store.Local{Root: t.TempDir()}, Embedded, time.Minute)
	s.now = func() time.Time { return asOf }
	if err := s.SetPrometheus(prom.URL + "/prom"); err != nil {
		t.Fatal(err)
	}
	return s, prom
}

func allValues(storageUp string) map[string]string {
	return map[string]string{
		statusQueries[0].promql: "10.43",
		statusQueries[1].promql: "412.04",
		statusQueries[2].promql: "1610000000.4",
		statusQueries[3].promql: "2147483648",
		statusQueries[4].promql: storageUp,
		statusQueries[5].promql: "1",
	}
}

func TestStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"all present", allValues("0"),
			`{"as_of":"2026-11-11T21:30:00Z","ingest":{"messages_per_s":10.4,"sync_lag_s":412,"spool_bytes":1610000000,"spool_max_bytes":2147483648,"storage_up":false,"mqtt_connected":true}}`},
		{"storage up", allValues("1"),
			`{"as_of":"2026-11-11T21:30:00Z","ingest":{"messages_per_s":10.4,"sync_lag_s":412,"spool_bytes":1610000000,"spool_max_bytes":2147483648,"storage_up":true,"mqtt_connected":true}}`},
		{"empty vectors are null", map[string]string{},
			`{"as_of":"2026-11-11T21:30:00Z","ingest":{"messages_per_s":null,"sync_lag_s":null,"spool_bytes":null,"spool_max_bytes":null,"storage_up":null,"mqtt_connected":null}}`},
		{"NaN and Inf are null", map[string]string{statusQueries[0].promql: "NaN", statusQueries[1].promql: "+Inf", statusQueries[4].promql: "-Inf"},
			`{"as_of":"2026-11-11T21:30:00Z","ingest":{"messages_per_s":null,"sync_lag_s":null,"spool_bytes":null,"spool_max_bytes":null,"storage_up":null,"mqtt_connected":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeProm{values: tc.values}
			s, _ := newStatusServer(t, fake)
			rec := get(t, s.Handler(), "/api/v1/status")
			if rec.Code != 200 || rec.Body.String() != tc.want {
				t.Fatalf("got %d %s\nwant %s", rec.Code, rec.Body, tc.want)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "max-age=10" {
				t.Errorf("Cache-Control = %q", cc)
			}
			if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("ETag") == "" {
				t.Errorf("missing CSP or ETag: %v", rec.Header())
			}
			want := make([]string, len(statusQueries))
			for i, q := range statusQueries {
				want[i] = q.promql
			}
			if got := slices.Sorted(slices.Values(fake.received())); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
				t.Errorf("queries sent:\n %q\nwant\n %q", got, want)
			}
		})
	}
}

func TestStatusNotReady(t *testing.T) {
	statusTimeout = 100 * time.Millisecond
	t.Cleanup(func() { statusTimeout = 2 * time.Second })
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"prometheus 500", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "secret internal detail", http.StatusInternalServerError)
		}},
		{"invalid JSON", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret internal detail {")) }},
		{"error status", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"secret internal detail"}`))
		}},
		{"matrix result", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		}},
		{"two series", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]},{"value":[1,"2"]}]}}`))
		}},
		{"value not a number", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"abc"]}]}}`))
		}},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("moved") == "" { // followed, the redirect would reach a valid answer
				http.Redirect(w, r, r.URL.Path+"?moved=1&"+r.URL.RawQuery, http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}},
		{"oversize body", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"success","pad":"` + strings.Repeat("x", promMaxBody) + `"}`))
		}},
		{"timeout", func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStatusServer(t, &fakeProm{handler: tc.handler})
			rec := get(t, s.Handler(), "/api/v1/status")
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"not_ready"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("Prometheus' body leaked to the client: %s", rec.Body)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q", cc)
			}
		})
	}
	t.Run("not configured", func(t *testing.T) {
		h := New(store.Local{Root: t.TempDir()}, Embedded, time.Minute).Handler()
		rec := get(t, h, "/api/v1/status")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"not_ready"`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		s, prom := newStatusServer(t, &fakeProm{})
		prom.Close()
		if rec := get(t, s.Handler(), "/api/v1/status"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
}

func TestStatusCaching(t *testing.T) {
	fake := &fakeProm{values: allValues("1")}
	s, _ := newStatusServer(t, fake)
	now := asOf
	s.now = func() time.Time { return now }
	h := s.Handler()
	n := len(statusQueries)

	first := get(t, h, "/api/v1/status")
	now = now.Add(9 * time.Second)
	if rec := get(t, h, "/api/v1/status"); rec.Body.String() != first.Body.String() || len(fake.received()) != n {
		t.Fatalf("within 10 s: %d queries sent, want %d (cached)", len(fake.received()), n)
	}
	if rec := get(t, h, "/api/v1/status", "If-None-Match", first.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
	now = now.Add(time.Second)
	if get(t, h, "/api/v1/status"); len(fake.received()) != 2*n {
		t.Fatalf("after 10 s: %d queries sent, want %d (refreshed)", len(fake.received()), 2*n)
	}

	// A failure is cached only briefly.
	fake.setHandler(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", 500) })
	now = now.Add(10 * time.Second)
	get(t, h, "/api/v1/status")
	sent := len(fake.received())
	now = now.Add(time.Second)
	if get(t, h, "/api/v1/status"); len(fake.received()) != sent {
		t.Errorf("failure retried after 1 s")
	}
	now = now.Add(statusFailureTTL)
	if rec := get(t, h, "/api/v1/status"); len(fake.received()) == sent || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("failure not retried after %v (code %d)", statusFailureTTL+time.Second, rec.Code)
	}
}

func TestStatusConcurrentRequestsShareOneRefresh(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeProm{values: allValues("1"), gate: release}
	s, _ := newStatusServer(t, fake)
	h := s.Handler()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if rec := get(t, h, "/api/v1/status"); rec.Code != 200 {
				t.Errorf("status %d", rec.Code)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := len(fake.received()); got != len(statusQueries) {
		t.Errorf("%d requests to Prometheus for 10 concurrent clients, want one refresh", got)
	}
}

func TestSetPrometheus(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"", true},
		{"http://meshtwin-prometheus:9090", true},
		{"https://prom.example.com/prefix/", true},
		{"meshtwin-prometheus:9090", false},
		{"/prometheus", false},
		{"ftp://prom:9090", false},
		{"http://", false},
		{"http://user:pass@prom:9090", false},
		{"http://prom:9090/?query=up", false},
		{"http://prom:9090/#x", false},
		{"http://prom:9090/%zz", false},
	} {
		s := New(store.Local{}, Embedded, time.Minute)
		if err := s.SetPrometheus(tc.raw); (err == nil) != tc.ok {
			t.Errorf("SetPrometheus(%q) = %v, want ok=%v", tc.raw, err, tc.ok)
		}
	}
}

// TestStatusMatchesContract keeps the code's queries and response keys equal
// to docs/api-contract.md: the field table's PromQL (the booleans' "== 1" is
// applied in Go, see statusQueries) and the example's keys.
func TestStatusMatchesContract(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/api-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(doc)[strings.Index(string(doc), "### `GET /api/v1/status`"):]
	section = section[:strings.Index(section[1:], "\n### ")+1]
	table := map[string]string{}
	for _, m := range regexp.MustCompile("(?m)^\\| `(\\w+)` \\| `([^`]+)`").FindAllStringSubmatch(section, -1) {
		table[m[1]] = strings.TrimSuffix(m[2], " == 1")
	}
	if len(table) != len(statusQueries) {
		t.Fatalf("contract table has %d fields, code has %d", len(table), len(statusQueries))
	}
	for _, q := range statusQueries {
		if table[q.field] != q.promql {
			t.Errorf("%s: contract %q, code %q", q.field, table[q.field], q.promql)
		}
	}

	s, _ := newStatusServer(t, &fakeProm{values: allValues("1")})
	sameKeys(t, "status", contractExamples(t)["status"], decode(t, get(t, s.Handler(), "/api/v1/status")))
}
