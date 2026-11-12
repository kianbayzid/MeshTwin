package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"meshtwin/api/internal/store"
)

// consoleFS has the layout of a Vite build (web/dist).
var consoleFS = fstest.MapFS{
	"index.html":                 {Data: []byte(`<!doctype html><script type="module" src="/assets/index-abc123.js"></script>`)},
	"assets/index-abc123.js":     {Data: []byte("console.log(1)")},
	"assets/index-abc123.css":    {Data: []byte("body{}")},
	"assets/inter-abc123.woff2":  {Data: []byte("wOF2....")},
	"assets/inter-abc123.woff":   {Data: []byte("wOFF....")},
	"assets/_helper-abc123.js":   {Data: []byte("export {}")},
	"assets/sub/nested-abc12.js": {Data: []byte("export {}")},
}

func TestConsoleRoutes(t *testing.T) {
	h := New(store.Local{Root: t.TempDir()}, consoleFS, time.Minute).Handler()
	const immutable = "public, max-age=31536000, immutable"
	for _, tc := range []struct {
		url                string
		code               int
		ctype, cache, body string
	}{
		{"/", 200, "text/html; charset=utf-8", "no-cache", "/assets/index-abc123.js"},
		{"/assets/index-abc123.js", 200, "text/javascript; charset=utf-8", immutable, "console.log(1)"},
		{"/assets/index-abc123.css", 200, "text/css; charset=utf-8", immutable, "body{}"},
		{"/assets/inter-abc123.woff2", 200, "font/woff2", immutable, "wOF2"},
		{"/assets/inter-abc123.woff", 200, "font/woff", immutable, "wOFF"},
		{"/assets/_helper-abc123.js", 200, "text/javascript; charset=utf-8", immutable, "export"},
		{"/assets/missing.js", 404, "application/json", "no-store", "not_found"},
		{"/assets/", 404, "application/json", "no-store", "not_found"},    // no listing
		{"/assets/sub", 404, "application/json", "no-store", "not_found"}, // no listing
		{"/static/app.js", 404, "", "", ""},                               // the Leaflet route is gone
		{"/index.js", 404, "", "", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			rec := get(t, h, tc.url)
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d", rec.Code, tc.code)
			}
			if tc.ctype != "" && rec.Header().Get("Content-Type") != tc.ctype {
				t.Errorf("Content-Type %q, want %q", rec.Header().Get("Content-Type"), tc.ctype)
			}
			if tc.cache != "" && rec.Header().Get("Cache-Control") != tc.cache {
				t.Errorf("Cache-Control %q, want %q", rec.Header().Get("Cache-Control"), tc.cache)
			}
			if !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("body %q lacks %q", rec.Body, tc.body)
			}
		})
	}
}

// TestEmbeddedPlaceholder: the committed console/index.html builds into the
// binary and passes the CSP (no external or inline script/style).
func TestEmbeddedPlaceholder(t *testing.T) {
	rec := get(t, New(store.Local{Root: t.TempDir()}, Embedded, time.Minute).Handler(), "/")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Console not built") {
		t.Fatalf("status %d body %q", rec.Code, body)
	}
	for _, bad := range []string{"<script", "<style", "style=", "http://", "https://"} {
		if strings.Contains(body, bad) {
			t.Errorf("placeholder contains %q", bad)
		}
	}
}

// TestCSPMatchesContract compares the header with the policy written in
// docs/api-contract.md (whitespace normalised: the doc wraps it).
func TestCSPMatchesContract(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/api-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("CSP for all responses: `([^`]+)`").FindStringSubmatch(string(doc))
	if m == nil {
		t.Fatal("CSP not found in the contract")
	}
	want := strings.Join(strings.Fields(m[1]), " ")
	h := New(store.Local{Root: t.TempDir()}, consoleFS, time.Minute).Handler()
	for _, url := range []string{"/", "/assets/index-abc123.js", "/api/v1/regions", "/nope"} {
		if got := get(t, h, url).Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: CSP\n %q\nwant\n %q", url, got, want)
		}
	}
}

func TestBasemap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seattle-v1.pmtiles"), []byte("PMTiles\x03rest-of-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.pmtiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.pmtiles"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink out of the directory must not be followed (needs privileges on Windows; skipped there if refused).
	_ = os.Symlink(filepath.Join(outside, "x.pmtiles"), filepath.Join(dir, "escape.pmtiles"))

	off := New(store.Local{Root: t.TempDir()}, consoleFS, time.Minute)
	if rec := get(t, off.Handler(), "/basemap/seattle-v1.pmtiles"); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("basemap off: %d %s, want 404 not_found", rec.Code, rec.Body)
	}

	if err := off.SetBasemapDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("SetBasemapDir accepted a missing directory")
	}
	s := New(store.Local{Root: t.TempDir()}, consoleFS, time.Minute)
	if err := s.SetBasemapDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.basemap.Close() }) // Windows cannot remove an open directory
	h := s.Handler()

	rec := get(t, h, "/basemap/seattle-v1.pmtiles", "Range", "bytes=0-6")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "PMTiles" {
		t.Fatalf("Range: %d %q, want 206 \"PMTiles\"", rec.Code, rec.Body)
	}
	for k, want := range map[string]string{
		"Accept-Ranges": "bytes", "Content-Type": "application/octet-stream",
		"Cache-Control": "public, max-age=86400", "Content-Range": "bytes 0-6/23",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if rec := get(t, h, "/basemap/seattle-v1.pmtiles"); rec.Code != 200 || rec.Body.Len() != 23 {
		t.Errorf("full GET: %d, %d bytes", rec.Code, rec.Body.Len())
	}

	for _, tc := range []struct {
		url  string
		code int
	}{
		{"/basemap/..%2Fx.pmtiles", 404},
		{"/basemap/..%5Cx.pmtiles", 404},
		{"/basemap/../x.pmtiles", http.StatusTemporaryRedirect}, // the mux cleans the path first
		{"/basemap/A.pmtiles", 404},
		{"/basemap/x.mbtiles", 404},
		{"/basemap/" + strings.Repeat("a", 33) + ".pmtiles", 404},
		{"/basemap/.pmtiles", 404},
		{"/basemap/missing.pmtiles", 404},
		{"/basemap/dir.pmtiles", 404},
		{"/basemap/escape.pmtiles", 404},
		{"/basemap/", 404},
	} {
		rec := get(t, h, tc.url)
		if rec.Code != tc.code || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: %d %q, want %d", tc.url, rec.Code, rec.Body, tc.code)
		}
	}
}
