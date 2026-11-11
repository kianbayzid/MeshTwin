package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"meshtwin/api/internal/store"
)

const terrainFixture = `{"meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-11T03:00:00Z",
  "window": null, "source": "terrain-job"},
  "encoding": "mapbox", "tile_size": 256, "minzoom": 8, "maxzoom": 12,
  "bounds": [-122.6, 47.3, -121.8, 47.9], "dem": "Copernicus GLO-30", "tiles": 112}`

// pngBytes encodes a blank w x h PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTile(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(root, "summaries", "region=SEA", "terrain", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTile(t *testing.T) {
	h, root := newTestServer(t, false)
	tile := pngBytes(t, 256, 256)
	writeTile(t, root, "8/40/89.png", tile)
	writeTile(t, root, "0/0/0.png", tile)
	writeTile(t, root, "14/16383/16383.png", tile)
	writeTile(t, root, "1/0/0.png", []byte("GIF89a not a png"))
	writeTile(t, root, "1/0/1.png", pngBytes(t, 512, 512))
	writeTile(t, root, "1/1/0.png", append(tile, make([]byte, store.MaxTileBytes)...)) // valid header, over the cap

	for _, tc := range []struct {
		url     string
		status  int
		errCode string // "" for a served tile
	}{
		{"/api/v1/regions/SEA/terrain/8/40/89.png", 200, ""},
		{"/api/v1/regions/sea/terrain/8/40/89.png", 200, ""},
		{"/api/v1/regions/SEA/terrain/0/0/0.png", 200, ""},
		{"/api/v1/regions/SEA/terrain/14/16383/16383.png", 200, ""},

		// Missing: inside the grid but not written (outside bounds/zooms), or an unknown region.
		{"/api/v1/regions/SEA/terrain/8/40/90.png", 404, "not_found"},
		{"/api/v1/regions/PDX/terrain/8/40/89.png", 404, "not_found"},

		// Stored but not servable.
		{"/api/v1/regions/SEA/terrain/1/0/0.png", 503, "not_ready"}, // not a PNG
		{"/api/v1/regions/SEA/terrain/1/0/1.png", 503, "not_ready"}, // wrong size
		{"/api/v1/regions/SEA/terrain/1/1/0.png", 503, "not_ready"}, // over 1 MB

		// Bad coordinates.
		{"/api/v1/regions/SEA/terrain/-1/0/0.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/-1/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40/-0.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/+8/40/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/%2B40/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/08/40/89.png", 400, "bad_request"}, // leading zero: one URL per tile
		{"/api/v1/regions/SEA/terrain/8/040/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40/00.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/15/0/0.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/256/89.png", 400, "bad_request"}, // x = 2^z
		{"/api/v1/regions/SEA/terrain/8/40/256.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/0/1/0.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/14/16384/0.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/99999999999999999999/89.png", 400, "bad_request"}, // overflows int64
		{"/api/v1/regions/SEA/terrain/8/4294967336/89.png", 400, "bad_request"},           // 2^32+40: wraps to 40 in uint32
		{"/api/v1/regions/SEA/terrain/8/0x28/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/4e1/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40.0/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/%2040/89.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/%D9%A4/89.png", 400, "bad_request"}, // Arabic-Indic digit four
		{"/api/v1/regions/SEA/terrain/8/40/89", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40/89.PNG", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40/89.png.png", 400, "bad_request"},
		{"/api/v1/regions/SEA/terrain/8/40/.png", 400, "bad_request"},
		{"/api/v1/regions/S/terrain/8/40/89.png", 400, "bad_request"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			rec := get(t, h, tc.url)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.errCode == "" {
				if ct := rec.Header().Get("Content-Type"); ct != "image/png" || !bytes.Equal(rec.Body.Bytes(), tile) {
					t.Errorf("Content-Type %q, %d bytes; want image/png and the stored tile", ct, rec.Body.Len())
				}
				return
			}
			if e, _ := decode(t, rec)["error"].(map[string]any); e["code"] != tc.errCode {
				t.Errorf("error = %v, want code %q", e, tc.errCode)
			}
		})
	}
}

func TestTileBadInputNeverReachesStore(t *testing.T) {
	_, root := newTestServer(t, false)
	spy := &spyStore{Store: store.Local{Root: root}}
	h := New(spy, Embedded, time.Minute).Handler()
	for _, url := range []string{
		"/api/v1/regions/%2E%2E/terrain/0/0/0.png",
		"/api/v1/regions/SEA/terrain/0/0/%2E%2E%2F%2E%2E%2Fx.png",
		"/api/v1/regions/SEA/terrain/0/%2E%2E/0.png",
		"/api/v1/regions/SEA/terrain/99/0/0.png",
	} {
		if rec := get(t, h, url); rec.Code < 300 {
			t.Errorf("%s: status %d, want an error", url, rec.Code)
		}
	}
	if len(spy.calls) != 0 {
		t.Errorf("storage was called: %v", spy.calls)
	}
}

func TestTileCachingAndMetrics(t *testing.T) {
	h, root := newTestServer(t, false)
	tile := pngBytes(t, 256, 256)
	writeTile(t, root, "8/40/89.png", tile)
	writeTile(t, root, "8/40/88.png", []byte("not a png"))
	url := "/api/v1/regions/SEA/terrain/8/40/89.png"

	first := get(t, h, url)
	sum := sha256.Sum256(tile)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	if first.Header().Get("ETag") != etag || first.Header().Get("Cache-Control") != "max-age=3600" {
		t.Fatalf("headers %v; want ETag %s and max-age=3600", first.Header(), etag)
	}
	if rec := get(t, h, url, "If-None-Match", etag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: status %d, %d bytes; want 304, empty", rec.Code, rec.Body.Len())
	}

	before := testutil.ToFloat64(validationFailures.WithLabelValues("terrain_tile"))
	get(t, h, "/api/v1/regions/SEA/terrain/8/40/88.png")
	if d := testutil.ToFloat64(validationFailures.WithLabelValues("terrain_tile")) - before; d != 1 {
		t.Errorf("validation failures grew by %v, want 1", d)
	}
	body := get(t, h, "/metrics").Body.String()
	if !strings.Contains(body, `route="GET /api/v1/regions/{region}/terrain/{z}/{x}/{file}"`) || strings.Contains(body, "40/89") {
		t.Error("tile requests must be labelled by route pattern only")
	}
}

func TestTerrainSummary(t *testing.T) {
	h, root := newTestServer(t, false)
	if rec := get(t, h, "/api/v1/regions/SEA/terrain.json"); rec.Code != 503 {
		t.Errorf("before the job ran: status %d, want 503 not_ready", rec.Code)
	}
	writeSummary(t, root, "SEA", "terrain", terrainFixture)
	h = New(store.Local{Root: root}, Embedded, time.Minute).Handler() // the first one cached the miss
	rec := get(t, h, "/api/v1/regions/SEA/terrain.json")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, %v", rec.Code, rec.Header())
	}
	if meta := decode(t, rec)["meta"].(map[string]any); meta["window"] != nil {
		t.Errorf("meta.window = %v, want null", meta["window"])
	}
	regions := get(t, h, "/api/v1/regions").Body.String()
	if !strings.Contains(regions, `"terrain"`) || strings.Contains(regions, "2026-11-11") {
		t.Errorf("regions %s: want terrain listed, and its generated_at not used as updated_at", regions)
	}
}

func TestValidateTerrain(t *testing.T) {
	edit := func(old, repl string) string {
		if !strings.Contains(terrainFixture, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(terrainFixture, old, repl, 1)
	}
	const bounds = `"bounds": [-122.6, 47.3, -121.8, 47.9]`
	for _, tc := range []struct {
		name, summary, doc, wantErr string // wantErr "" = valid
	}{
		{"fixture", "terrain", terrainFixture, ""},
		{"no window key", "terrain", edit(`"window": null, `, ``), ""},
		{"one zoom", "terrain", edit(`"minzoom": 8, "maxzoom": 12`, `"minzoom": 12, "maxzoom": 12`), ""},
		{"integer bounds", "terrain", edit(bounds, `"bounds": [-123, 47, -121, 48]`), ""},
		{"zero tiles", "terrain", edit(`"tiles": 112`, `"tiles": 0`), ""},

		// meta.window: null only for terrain.
		{"terrain with a window", "terrain", edit(`"window": null`, `"window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}`), "meta.window: is a object, want null"},
		{"topology with null window", "topology", strings.Replace(fixtures["topology"], `"window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}`, `"window": null`, 1), "meta.window is missing or null"},

		{"wrong encoding", "terrain", edit(`"mapbox"`, `"terrarium"`), "encoding"},
		{"tile size 512", "terrain", edit(`"tile_size": 256`, `"tile_size": 512`), "tile_size"},
		{"tile size 256.0", "terrain", edit(`"tile_size": 256`, `"tile_size": 256.0`), "tile_size"},
		{"null dem", "terrain", edit(`"Copernicus GLO-30"`, `null`), "dem"},
		{"empty dem", "terrain", edit(`"Copernicus GLO-30"`, `""`), "dem"},
		{"min above max", "terrain", edit(`"minzoom": 8`, `"minzoom": 13`), "minzoom"},
		{"maxzoom 15", "terrain", edit(`"maxzoom": 12`, `"maxzoom": 15`), "maxzoom"},
		{"negative minzoom", "terrain", edit(`"minzoom": 8`, `"minzoom": -1`), "minzoom"},
		{"fractional zoom", "terrain", edit(`"maxzoom": 12`, `"maxzoom": 12.5`), "maxzoom"},
		{"missing zoom", "terrain", edit(`"minzoom": 8, `, ``), "minzoom"},
		{"negative tiles", "terrain", edit(`"tiles": 112`, `"tiles": -1`), "tiles"},
		{"fractional tiles", "terrain", edit(`"tiles": 112`, `"tiles": 1e2`), "tiles"},
		{"three bounds", "terrain", edit(bounds, `"bounds": [-122.6, 47.3, -121.8]`), "bounds"},
		{"null bounds", "terrain", edit(bounds, `"bounds": null`), "bounds"},
		{"bound as string", "terrain", edit(bounds, `"bounds": ["-122.6", 47.3, -121.8, 47.9]`), "bounds"},
		{"raw coordinate in bounds", "terrain", edit(bounds, `"bounds": [-122.6062, 47.3, -121.8, 47.9]`), "0.1° grid"},
		{"west > east", "terrain", edit(bounds, `"bounds": [-121.8, 47.3, -122.6, 47.9]`), "bounds"},
		{"south = north", "terrain", edit(bounds, `"bounds": [-122.6, 47.3, -121.8, 47.3]`), "bounds"},
		{"lat out of range", "terrain", edit(bounds, `"bounds": [-122.6, 47.3, -121.8, 97.9]`), "bounds"},
		{"lon out of range", "terrain", edit(bounds, `"bounds": [-182.6, 47.3, -121.8, 47.9]`), "bounds"},
		{"unknown field", "terrain", edit(`"tiles": 112`, `"tiles": 112, "center": {"lat": 47.6062, "lon": -122.3321}`), "center: field not in the allow-list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate("SEA", tc.summary, []byte(tc.doc))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
