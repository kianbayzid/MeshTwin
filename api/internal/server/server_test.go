package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"meshtwin/api/internal/store"
)

// Small fixtures in the contract's shapes.
var fixtures = map[string]string{
	"topology": `{` + validMeta + `,
	  "stats": {"receptions": 10, "flood_receptions": 8, "observers": 1, "nodes": 3, "links": 3, "ambiguous_hop_share": 0.1, "unresolved_hops": 0},
	  "nodes": [
	    {"id": "351FC1F9", "role": "repeater", "name": "SEA-RPT-01", "location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.01}, "observer": true, "degree": 2, "last_heard": "2026-11-09T13:58:12Z"},
	    {"id": "0562C16B", "role": "companion", "name": null, "location": {"lat": 47.54, "lon": -122.11, "grid_deg": 0.01}, "observer": false, "degree": 2, "last_heard": "2026-11-09T12:03:40Z"},
	    {"id": "C2", "role": "unknown", "name": null, "location": null, "observer": false, "degree": 2, "last_heard": "2026-11-09T12:00:00Z"}],
	  "links": [
	    {"a": "351FC1F9", "b": "0562C16B", "observations": 10, "confidence": 1.0, "snr_db": null, "rssi_dbm": null, "first_seen": "2026-11-09T12:00:00Z", "last_seen": "2026-11-09T13:00:00Z"},
	    {"a": "351FC1F9", "b": "C2", "observations": 5, "confidence": 0.8, "snr_db": null, "rssi_dbm": null, "first_seen": "2026-11-09T12:00:00Z", "last_seen": "2026-11-09T13:00:00Z"},
	    {"a": "0562C16B", "b": "C2", "observations": 2, "confidence": 0.5, "snr_db": null, "rssi_dbm": null, "first_seen": "2026-11-09T12:00:00Z", "last_seen": "2026-11-09T13:00:00Z"}]}`,
	"resilience": `{` + validMeta + `, "baseline": {"nodes": 3, "reachable_pairs": 3, "reachable_share": 1.0}, "single_points_of_failure": [], "bridges": []}`,
	"residuals": `{` + strings.Replace(validMeta, "2026-11-09T14:00:00Z", "2026-11-09T15:00:00Z", 1) + `,
	  "model": {"name": "fspl+knife-edge", "version": "1", "dem": "flat (no DEM)"},
	  "summary": {"links": 3, "median_abs_residual_db": 4.0, "rmse_db": 5.0, "bias_db": 1.0},
	  "links": [
	    {"a": "AA", "b": "B1", "residual_db": 2.0, "samples": 50},
	    {"a": "AA", "b": "B2", "residual_db": -9.0, "samples": 5},
	    {"a": "AA", "b": "B3", "residual_db": 4.0, "samples": 500}]}`,
	"surprising": `{` + validMeta + `, "observed_but_predicted_impossible": [], "predicted_strong_but_never_seen": []}`,
}

const placementFixture = `{` + validMeta + `,
  "method": {"grid_deg": 0.01, "candidates_scored": 3, "antenna_height_m": 10, "model": "fspl+knife-edge v1"},
  "candidates": [
    {"rank": 3, "location": {"lat": 47.6, "lon": -122.2, "grid_deg": 0.01}, "score": 0.1, "spofs_removed": [], "new_links": []},
    {"rank": 1, "location": {"lat": 47.68, "lon": -122.21, "grid_deg": 0.01}, "score": 0.8, "spofs_removed": ["351FC1F9"], "new_links": [{"to": "351FC1F9", "predicted_snr_db": 5.5}]},
    {"rank": 2, "location": {"lat": 47.61, "lon": -122.22, "grid_deg": 0.01}, "score": 0.5, "spofs_removed": [], "new_links": []}]}`

func writeSummary(t *testing.T, root, region, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "summaries", "region="+region)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestServer serves fixtures for SEA (placement only if withPlacement).
func newTestServer(t *testing.T, withPlacement bool) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range fixtures {
		writeSummary(t, root, "SEA", name, body)
	}
	if withPlacement {
		writeSummary(t, root, "SEA", "placement", placementFixture)
	}
	return New(store.Local{Root: root}, Embedded, time.Minute).Handler(), root
}

func get(t *testing.T, h http.Handler, url string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body)
	}
	return m
}

func TestStatusCodes(t *testing.T) {
	h, _ := newTestServer(t, false)
	for _, tc := range []struct {
		url      string
		status   int
		errCode  string // expected error.code, "" for success
		bodyWant string
	}{
		{"/healthz", 200, "", "ok"},
		{"/readyz", 200, "", "ready"},
		{"/api/v1/regions", 200, "", `"SEA"`},
		{"/api/v1/regions/SEA/topology", 200, "", `"links"`},
		{"/api/v1/regions/sea/topology", 200, "", `"links"`}, // upper-cased like the ingester
		{"/api/v1/regions/SEA/resilience", 200, "", `"baseline"`},
		{"/api/v1/regions/SEA/links/residuals", 200, "", `"model"`},
		{"/api/v1/regions/SEA/links/surprising", 200, "", `"predicted_strong_but_never_seen"`},
		{"/api/v1/regions/SEA/placement", 503, "not_ready", "no placement for region SEA yet"},
		{"/api/v1/regions/PDX/topology", 404, "not_found", "no summaries for region PDX"},
		{"/api/v1/regions/SEA/topology?min_confidence=0.4", 400, "bad_request", "min_confidence"},
		{"/api/v1/regions/SEA/topology?min_confidence=1.1", 400, "bad_request", "min_confidence"},
		{"/api/v1/regions/SEA/topology?min_confidence=NaN", 400, "bad_request", "min_confidence"},
		{"/api/v1/regions/SEA/topology?min_confidence=high", 400, "bad_request", "min_confidence"},
		{"/api/v1/regions/SEA/links/residuals?sort=name", 400, "bad_request", "sort"},
		{"/api/v1/regions/SEA/links/residuals?limit=0", 400, "bad_request", "limit"},
		{"/api/v1/regions/SEA/links/residuals?limit=1001", 400, "bad_request", "limit"},
		{"/api/v1/regions/SEA/links/residuals?limit=ten", 400, "bad_request", "limit"},
		{"/api/v1/regions/SEA/placement?top=51", 400, "bad_request", "top"},
		{"/api/v1/regions/SEA/placement?top=-1", 400, "bad_request", "top"},
		{"/api/v1/regions/S/topology", 400, "bad_request", "region"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			rec := get(t, h, tc.url)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.bodyWant) {
				t.Errorf("body %s does not contain %q", rec.Body, tc.bodyWant)
			}
			if tc.errCode != "" {
				e, _ := decode(t, rec)["error"].(map[string]any)
				if e["code"] != tc.errCode {
					t.Errorf("error = %v, want code %q", e, tc.errCode)
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("error Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
				}
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("security headers missing: %v", rec.Header())
			}
		})
	}
}

// spyStore records every storage call.
type spyStore struct {
	store.Store
	calls []string
}

func (s *spyStore) Get(ctx context.Context, region, name string) ([]byte, error) {
	s.calls = append(s.calls, region+"/"+name)
	return s.Store.Get(ctx, region, name)
}

func (s *spyStore) List(ctx context.Context) (map[string][]string, error) {
	s.calls = append(s.calls, "list")
	return s.Store.List(ctx)
}

func TestRegionValidationNeverReachesStore(t *testing.T) {
	_, root := newTestServer(t, false)
	spy := &spyStore{Store: store.Local{Root: root}}
	h := New(spy, Embedded, time.Minute).Handler()
	for _, url := range []string{
		"/api/v1/regions/%2E%2E/topology", // ".." after decoding
		"/api/v1/regions/../topology",     // cleaned and redirected by the mux
		"/api/v1/regions/sea!/topology",
		"/api/v1/regions/X:Y/topology",
		"/api/v1/regions/ABCDEFGHI/topology",
		"/api/v1/regions/%2E%2E%2Fx/resilience",
		"/api/v1/regions/SE%5CA/placement",
		"/api/v1/regions/%C5%BFEA/topology",    // "ſEA": strings.ToUpper would make it "SEA"
		"/api/v1/regions/%E2%84%AAEA/topology", // Kelvin sign + "EA": ToUpper -> "KEA"
	} {
		rec := get(t, h, url)
		if rec.Code < 300 {
			t.Errorf("%s: status %d, want an error", url, rec.Code)
		}
	}
	if len(spy.calls) != 0 {
		t.Errorf("storage was called: %v", spy.calls)
	}
}

func TestETag(t *testing.T) {
	h, _ := newTestServer(t, false)
	first := get(t, h, "/api/v1/regions/SEA/topology")
	etag := first.Header().Get("ETag")
	if etag == "" || first.Header().Get("Cache-Control") != "max-age=60" {
		t.Fatalf("headers: %v", first.Header())
	}
	if sum := sha256.Sum256(first.Body.Bytes()); etag != `"`+hex.EncodeToString(sum[:16])+`"` {
		t.Errorf("ETag %s is not the hash of the bytes served", etag)
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		rec := get(t, h, "/api/v1/regions/SEA/topology", "If-None-Match", inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: status %d, body %d bytes; want 304, empty", inm, rec.Code, rec.Body.Len())
		}
	}
	if rec := get(t, h, "/api/v1/regions/SEA/topology", "If-None-Match", `"stale"`); rec.Code != 200 {
		t.Errorf("stale ETag: status %d, want 200", rec.Code)
	}
	filtered := get(t, h, "/api/v1/regions/SEA/topology?min_confidence=0.9")
	if fe := filtered.Header().Get("ETag"); fe == "" || fe == etag {
		t.Errorf("filtered ETag %q should differ from %q", fe, etag)
	}
}

func linkPairs(t *testing.T, rec *httptest.ResponseRecorder, key string) []string {
	t.Helper()
	var out []string
	for _, l := range decode(t, rec)[key].([]any) {
		m := l.(map[string]any)
		out = append(out, m["a"].(string)+"-"+m["b"].(string))
	}
	return out
}

func TestTopologyMinConfidence(t *testing.T) {
	h, _ := newTestServer(t, false)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"", []string{"351FC1F9-0562C16B", "351FC1F9-C2", "0562C16B-C2"}},
		{"?min_confidence=0.5", []string{"351FC1F9-0562C16B", "351FC1F9-C2", "0562C16B-C2"}},
		{"?min_confidence=0.8", []string{"351FC1F9-0562C16B", "351FC1F9-C2"}},
		{"?min_confidence=1", []string{"351FC1F9-0562C16B"}},
	} {
		rec := get(t, h, "/api/v1/regions/SEA/topology"+tc.q)
		if got := linkPairs(t, rec, "links"); !slices.Equal(got, tc.want) {
			t.Errorf("%q: links %v, want %v", tc.q, got, tc.want)
		}
		doc := decode(t, rec)
		if n := len(doc["nodes"].([]any)); n != 3 {
			t.Errorf("%q: %d nodes, want all 3 (filter drops links only)", tc.q, n)
		}
		if doc["meta"] == nil || doc["stats"] == nil {
			t.Errorf("%q: meta/stats lost in rewrite", tc.q)
		}
	}
}

// A summary with no list at all filters to an empty list, not a 500.
func TestMissingListIsEmpty(t *testing.T) {
	h, root := newTestServer(t, false)
	writeSummary(t, root, "SEA", "topology", `{`+validMeta+`, "nodes": []}`)
	rec := get(t, h, "/api/v1/regions/SEA/topology?min_confidence=0.9")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"links":[]`) {
		t.Errorf("status %d body %s, want 200 with empty links", rec.Code, rec.Body)
	}
}

func TestResidualsSortLimit(t *testing.T) {
	h, _ := newTestServer(t, false)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"", []string{"AA-B2", "AA-B3", "AA-B1"}}, // abs_residual: 9, 4, 2
		{"?sort=residual", []string{"AA-B3", "AA-B1", "AA-B2"}},
		{"?sort=observations", []string{"AA-B3", "AA-B1", "AA-B2"}},
		{"?sort=abs_residual&limit=1", []string{"AA-B2"}},
		{"?limit=1000", []string{"AA-B2", "AA-B3", "AA-B1"}},
	} {
		rec := get(t, h, "/api/v1/regions/SEA/links/residuals"+tc.q)
		if got := linkPairs(t, rec, "links"); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestPlacementTop(t *testing.T) {
	h, _ := newTestServer(t, true)
	for _, tc := range []struct {
		q    string
		want []float64
	}{
		{"", []float64{1, 2, 3}},
		{"?top=2", []float64{1, 2}},
		{"?top=50", []float64{1, 2, 3}},
	} {
		rec := get(t, h, "/api/v1/regions/SEA/placement"+tc.q)
		var ranks []float64
		for _, c := range decode(t, rec)["candidates"].([]any) {
			ranks = append(ranks, c.(map[string]any)["rank"].(float64))
		}
		if !slices.Equal(ranks, tc.want) {
			t.Errorf("%q: ranks %v, want %v", tc.q, ranks, tc.want)
		}
	}
}

func TestRegions(t *testing.T) {
	h, root := newTestServer(t, false)
	writeSummary(t, root, "PDX", "placement", strings.Replace(placementFixture, `"region": "SEA"`, `"region": "PDX"`, 1))
	var got struct {
		Regions []regionInfo `json:"regions"`
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/regions").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Regions) != 2 || got.Regions[0].Region != "PDX" || got.Regions[1].Region != "SEA" {
		t.Fatalf("regions = %+v", got.Regions)
	}
	pdx, sea := got.Regions[0], got.Regions[1]
	if pdx.Nodes != nil || !slices.Equal(pdx.Summaries, []string{"placement"}) {
		t.Errorf("PDX = %+v; no topology means null nodes", pdx)
	}
	if !slices.Equal(sea.Summaries, []string{"topology", "resilience", "residuals", "surprising"}) ||
		sea.Nodes == nil || *sea.Nodes != 3 || *sea.Links != 3 ||
		sea.UpdatedAt.Format(time.RFC3339) != "2026-11-09T15:00:00Z" { // newest: the residuals fixture
		t.Errorf("SEA = %+v (updated_at %v)", sea, sea.UpdatedAt)
	}
}

func TestInvalidSummaryNotServed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"raw coordinate", strings.Replace(fixtures["topology"], `"lat": 47.62`, `"lat": 47.6205`, 1)},
		{"companion with name", strings.Replace(fixtures["topology"], `"role": "companion", "name": null`, `"role": "companion", "name": "Alice"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, root := newTestServer(t, false)
			writeSummary(t, root, "SEA", "topology", tc.body)
			before := testutil.ToFloat64(validationFailures.WithLabelValues("topology"))
			rec := get(t, h, "/api/v1/regions/SEA/topology")
			if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "Alice") || strings.Contains(rec.Body.String(), "47.6205") {
				t.Fatalf("status %d body %s: a bad file must not be served", rec.Code, rec.Body)
			}
			if e := decode(t, rec)["error"].(map[string]any); e["code"] != "not_ready" {
				t.Errorf("error = %v", e)
			}
			get(t, h, "/api/v1/regions/SEA/topology") // cached: validated once
			if d := testutil.ToFloat64(validationFailures.WithLabelValues("topology")) - before; d != 1 {
				t.Errorf("validation failures metric grew by %v, want 1", d)
			}
			// /regions names only what the API will serve.
			if strings.Contains(get(t, h, "/api/v1/regions").Body.String(), `"topology"`) {
				t.Error("rejected topology still listed in /regions")
			}
		})
	}
}

func TestOversizeSummaryNotServed(t *testing.T) {
	h, root := newTestServer(t, false)
	// Valid JSON padded past the cap: the size alone must reject it.
	writeSummary(t, root, "SEA", "topology", fixtures["topology"]+strings.Repeat(" ", store.MaxBytes))
	before := testutil.ToFloat64(validationFailures.WithLabelValues("topology"))
	rec := get(t, h, "/api/v1/regions/SEA/topology")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if d := testutil.ToFloat64(validationFailures.WithLabelValues("topology")) - before; d != 1 {
		t.Errorf("validation failures metric grew by %v, want 1", d)
	}
}

func TestReadyzStoreDown(t *testing.T) {
	h := New(store.Local{Root: filepath.Join(t.TempDir(), "missing")}, Embedded, time.Minute).Handler()
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503", rec.Code)
	}
	if rec := get(t, h, "/api/v1/regions"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("regions = %d, want 503", rec.Code)
	}
}

func TestMapPage(t *testing.T) {
	h, _ := newTestServer(t, false)
	for _, tc := range []struct{ url, want string }{
		{"/", `integrity="sha384-`},
		{"/static/app.js", "OpenStreetMap</a> contributors"},
		{"/static/app.js", "Copernicus WorldDEM-30"},
		{"/static/style.css", "#map"},
	} {
		rec := get(t, h, tc.url)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: status %d, missing %q", tc.url, rec.Code, tc.want)
		}
	}
	csp := get(t, h, "/").Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "https://unpkg.com", "https://tile.openstreetmap.org"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	h, _ := newTestServer(t, false)
	get(t, h, "/api/v1/regions/SEA/resilience")
	get(t, h, "/no/such/path/abc123")
	body := get(t, h, "/metrics").Body.String()
	if !strings.Contains(body, `route="GET /api/v1/regions/{region}/resilience"`) {
		t.Error("route pattern label missing")
	}
	if strings.Contains(body, "abc123") || strings.Contains(body, "regions/SEA") {
		t.Error("raw path leaked into metric labels")
	}
}

func TestSummaryGeneratedGauge(t *testing.T) {
	h := New(store.Local{Root: "../../testdata"}, Embedded, time.Minute).Handler()
	for _, name := range []string{"topology", "resilience", "residuals", "surprising", "placement"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../testdata/summaries/region=SEA", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var head struct {
				Meta struct {
					GeneratedAt time.Time `json:"generated_at"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				t.Fatal(err)
			}
			url := "/api/v1/regions/SEA/" + name
			if name == "residuals" || name == "surprising" {
				url = "/api/v1/regions/SEA/links/" + name
			}
			if rec := get(t, h, url); rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			got := testutil.ToFloat64(summaryGenerated.WithLabelValues("SEA", name))
			if want := float64(head.Meta.GeneratedAt.Unix()); got != want {
				t.Errorf("gauge = %v, want %v", got, want)
			}
		})
	}
}

func TestSummaryGeneratedGaugeNotSetWhenRejected(t *testing.T) {
	// A region no other test uses, so the gauge's global state is clean.
	root := t.TempDir()
	body := strings.Replace(fixtures["topology"], `"lat": 47.62`, `"lat": 47.6205`, 1)
	writeSummary(t, root, "REJ", "topology", strings.Replace(body, `"region": "SEA"`, `"region": "REJ"`, 1))
	h := New(store.Local{Root: root}, Embedded, time.Minute).Handler()
	if rec := get(t, h, "/api/v1/regions/REJ/topology"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	get(t, h, "/api/v1/regions") // also loads every listed summary
	// Delete reports whether the series existed; it must not.
	if summaryGenerated.DeleteLabelValues("REJ", "topology") {
		t.Error("gauge set for a summary that failed validation")
	}
}

func BenchmarkTopology(b *testing.B) {
	h := New(store.Local{Root: "../../testdata"}, Embedded, time.Minute).Handler()
	for _, url := range []string{"/api/v1/regions/SEA/topology", "/api/v1/regions/SEA/topology?min_confidence=0.9"} {
		b.Run(url, func(b *testing.B) {
			for b.Loop() {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
				if rec.Code != 200 {
					b.Fatal(rec.Code)
				}
			}
		})
	}
}
