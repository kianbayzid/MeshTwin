package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"meshtwin/api/internal/store"
)

const profilesFixture = `{` + validMeta + `,
  "model": {"name": "fspl+knife-edge", "version": "1", "dem": "flat (no DEM)"},
  "params": {"freq_mhz": 910.525, "k_factor": 1.333, "grid_deg": 0.01},
  "profiles": [
    {"a": "351FC1F9", "b": "0562C16B", "distance_km": 8.4, "antenna_a_m": 10.0, "antenna_b_m": 2.0,
     "ground_m": [52.1, 54.0, 61.3, 58.2], "bulge_m": [0.0, 1.5, 1.5, 0.0], "fresnel_r1_m": [0.0, 15.4, 15.4, 0.0],
     "line_of_sight": false, "fresnel_clearance": -0.31, "diffraction_loss_db": 8.9},
    {"a": "351FC1F9", "b": "C2", "distance_km": 0.0, "antenna_a_m": 10, "antenna_b_m": 10,
     "ground_m": [-3, 9000], "bulge_m": [0, 0], "fresnel_r1_m": [0, 0],
     "line_of_sight": true, "fresnel_clearance": 1.5, "diffraction_loss_db": 0}]}`

func TestProfileRoute(t *testing.T) {
	h, root := newTestServer(t, false)
	const url = "/api/v1/regions/SEA/links/351FC1F9/0562C16B/profile"
	if rec := get(t, h, url); rec.Code != 503 || !strings.Contains(rec.Body.String(), "no profiles for region SEA yet") {
		t.Fatalf("before the job ran: status %d %s, want 503 not_ready", rec.Code, rec.Body)
	}
	writeSummary(t, root, "SEA", "profiles", profilesFixture)
	h = New(store.Local{Root: root}, Embedded, time.Minute).Handler() // the first one cached the miss

	for _, tc := range []struct {
		url    string
		status int
		want   string // profile.a (stored orientation) for a 200, error code otherwise, "" to skip
	}{
		{"/api/v1/regions/SEA/links/351FC1F9/0562C16B/profile", 200, "351FC1F9"},
		{"/api/v1/regions/SEA/links/0562C16B/351FC1F9/profile", 200, "351FC1F9"}, // reversed
		{"/api/v1/regions/sea/links/0562c16b/351fc1f9/profile", 200, "351FC1F9"}, // hex is case-insensitive
		{"/api/v1/regions/SEA/links/C2/351FC1F9/profile", 200, "351FC1F9"},
		{"/api/v1/regions/SEA/links/0562C16B/C2/profile", 404, "not_found"},
		{"/api/v1/regions/SEA/links/351FC1F9/351FC1F9/profile", 404, "not_found"},
		{"/api/v1/regions/PDX/links/351FC1F9/0562C16B/profile", 404, "not_found"},
		{"/api/v1/regions/SEA/links/351FC1F9/G562C16B/profile", 400, "bad_request"},
		{"/api/v1/regions/SEA/links/351FC1F9/0562C16B00/profile", 400, "bad_request"}, // 10 hex: longer than a prefix
		{"/api/v1/regions/SEA/links/3/0562C16B/profile", 400, "bad_request"},
		{"/api/v1/regions/SEA/links/%2E%2E/0562C16B/profile", 400, "bad_request"},
		{"/api/v1/regions/S/links/351FC1F9/0562C16B/profile", 400, "bad_request"},
		{"/api/v1/regions/SEA/profiles", 404, ""}, // never served whole
	} {
		t.Run(tc.url, func(t *testing.T) {
			rec := get(t, h, tc.url)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.want == "" {
				return
			}
			m := decode(t, rec)
			if tc.status != 200 {
				if e, _ := m["error"].(map[string]any); e["code"] != tc.want {
					t.Errorf("error = %v, want code %q", e, tc.want)
				}
				return
			}
			p, _ := m["profile"].(map[string]any)
			if p["a"] != tc.want || m["meta"] == nil || m["model"] == nil || m["params"] == nil || len(m) != 4 {
				t.Errorf("body %s: want {meta, model, params, profile} with profile.a = %s", rec.Body, tc.want)
			}
			if rec.Header().Get("Cache-Control") != "max-age=60" || rec.Header().Get("ETag") == "" {
				t.Errorf("headers %v, want max-age=60 and an ETag", rec.Header())
			}
		})
	}

	first := get(t, h, url)
	if rec := get(t, h, url, "If-None-Match", first.Header().Get("ETag")); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: status %d, want 304", rec.Code)
	}
	if !strings.Contains(get(t, h, "/api/v1/regions").Body.String(), `"profiles"`) {
		t.Error("regions: want profiles listed")
	}
}

func TestProfileRejectedFileIsNotReady(t *testing.T) {
	h, root := newTestServer(t, false)
	writeSummary(t, root, "SEA", "profiles", strings.Replace(profilesFixture, `"distance_km": 8.4`, `"distance_km": 8.42`, 1))
	rec := get(t, h, "/api/v1/regions/SEA/links/351FC1F9/0562C16B/profile")
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "8.42") {
		t.Errorf("status %d %s, want 503 not_ready with no detail", rec.Code, rec.Body)
	}
	if strings.Contains(get(t, h, "/api/v1/regions").Body.String(), `"profiles"`) {
		t.Error("a rejected profiles.json must not be listed")
	}
}

func TestValidateProfiles(t *testing.T) {
	edit := func(old, repl string) string {
		if !strings.Contains(profilesFixture, old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(profilesFixture, old, repl, 1)
	}
	const (
		ground = `"ground_m": [52.1, 54.0, 61.3, 58.2]`
		second = `"ground_m": [-3, 9000], "bulge_m": [0, 0], "fresnel_r1_m": [0, 0]`
	)
	for _, tc := range []struct {
		name, doc, wantErr string // wantErr "" = valid
	}{
		{"fixture", profilesFixture, ""},
		{"no profiles yet", profilesFixture[:strings.Index(profilesFixture, `"profiles": [`)] + `"profiles": []}`, ""},
		{"256 samples", edit(second, samples(256)), ""},

		// params and meta
		{"freq too low", edit(`"freq_mhz": 910.525`, `"freq_mhz": 99`), "params.freq_mhz"},
		{"freq missing", edit(`"freq_mhz": 910.525, `, ``), "params.freq_mhz"},
		{"k too high", edit(`"k_factor": 1.333`, `"k_factor": 5.1`), "params.k_factor"},
		{"grid 0.001", edit(`"grid_deg": 0.01`, `"grid_deg": 0.001`), "params.grid_deg"},
		{"null window", edit(`"window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}`, `"window": null`), "meta.window"},
		{"null profiles", profilesFixture[:strings.Index(profilesFixture, `"profiles": [`)] + `"profiles": null}`, "profiles is missing"},

		// privacy: no locations anywhere
		{"lat in profile", edit(`"distance_km": 8.4`, `"distance_km": 8.4, "lat": 47.62`), "profiles[].l"}, // rejected as a location, whichever key is checked first
		{"lon in params", edit(`"grid_deg": 0.01}`, `"grid_deg": 0.01, "lon": -122.35}`), "params.l"},
		{"path points", edit(`"line_of_sight": false`, `"line_of_sight": false, "path": [{"lat": 47.62, "lon": -122.35}]`), "profiles[].path"},

		// ids
		{"a == b", edit(`"b": "0562C16B"`, `"b": "351FC1F9"`), "two different"},
		{"a == b any case", edit(`"b": "0562C16B"`, `"b": "351fc1f9"`), "two different"},
		{"full key", edit(`"b": "0562C16B"`, `"b": "0562C16B0562C16B"`), "node id"},
		{"missing b", edit(`"b": "0562C16B", `, ``), "two different"},
		{"duplicate pair", edit(`"b": "C2"`, `"b": "0562C16B"`), "duplicate pair"},
		{"duplicate pair reversed", edit(`{"a": "351FC1F9", "b": "C2"`, `{"a": "0562c16b", "b": "351FC1F9"`), "duplicate pair"},

		// scalars
		{"distance 2 decimals", edit(`"distance_km": 8.4`, `"distance_km": 8.42`), "distance_km"},
		{"negative distance", edit(`"distance_km": 8.4`, `"distance_km": -0.1`), "distance_km"},
		{"antenna 201", edit(`"antenna_b_m": 2.0`, `"antenna_b_m": 201`), "antenna_b_m"},
		{"antenna missing", edit(`"antenna_a_m": 10.0, `, ``), "antenna_a_m"},
		{"los null", edit(`"line_of_sight": false`, `"line_of_sight": null`), "line_of_sight"},
		{"los string", edit(`"line_of_sight": false`, `"line_of_sight": "no"`), "line_of_sight"},
		{"clearance overflows", edit(`"fresnel_clearance": -0.31`, `"fresnel_clearance": 1e400`), "fresnel_clearance"},
		{"loss missing", edit(`, "diffraction_loss_db": 8.9`, ``), "diffraction_loss_db"},

		// arrays
		{"length mismatch", edit(`"bulge_m": [0.0, 1.5, 1.5, 0.0]`, `"bulge_m": [0.0, 1.5, 0.0]`), "bulge_m has 3 samples"},
		{"one sample", edit(second, `"ground_m": [1], "bulge_m": [0], "fresnel_r1_m": [0]`), "ground_m has 1 samples"},
		{"257 samples", edit(second, samples(257)), "ground_m has 257 samples"},
		{"missing array", edit(`, "fresnel_r1_m": [0.0, 15.4, 15.4, 0.0]`, ``), "fresnel_r1_m has 0 samples"},
		{"2 decimals", edit(ground, `"ground_m": [52.13, 54.0, 61.3, 58.2]`), "ground_m has 52.13"},
		{"exponent", edit(ground, `"ground_m": [5.2e1, 54.0, 61.3, 58.2]`), "ground_m has 5.2e1"},
		{"huge ground", edit(ground, `"ground_m": [90000, 54.0, 61.3, 58.2]`), "ground_m has 90000"},
		{"below sea floor", edit(ground, `"ground_m": [-501, 54.0, 61.3, 58.2]`), "ground_m has -501"},
		{"overflowing ground", edit(ground, `"ground_m": [1`+strings.Repeat("0", 400)+`, 54.0, 61.3, 58.2]`), "ground_m has"},
		{"negative bulge", edit(`"bulge_m": [0.0, 1.5`, `"bulge_m": [0.0, -1.5`), "bulge_m has -1.5"},
		{"huge r1", edit(`"fresnel_r1_m": [0.0, 15.4`, `"fresnel_r1_m": [0.0, 10000.1`), "fresnel_r1_m has 10000.1"},
		{"null sample", edit(ground, `"ground_m": [null, 54.0, 61.3, 58.2]`), "ground_m has <nil>"},
		{"nested array", edit(ground, `"ground_m": [[52.1], 54.0, 61.3, 58.2]`), "ground_m[]: is a array"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate("SEA", "profiles", []byte(tc.doc))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// samples writes the three arrays with n valid samples each.
func samples(n int) string {
	s := "[" + strings.TrimSuffix(strings.Repeat("1.5, ", n), ", ") + "]"
	return `"ground_m": ` + s + `, "bulge_m": ` + s + `, "fresnel_r1_m": ` + s
}
