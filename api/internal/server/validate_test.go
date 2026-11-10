package server

import (
	"strings"
	"testing"
)

const validMeta = `"meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T14:00:00Z",
  "window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}, "source": "topology-job"}`

const (
	repeaterCell = `"location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.01}`
	companion    = `"role": "companion", "name": null`
)

func TestValidate(t *testing.T) {
	// edit returns the topology fixture with one substring replaced.
	edit := func(old, repl string) string {
		if !strings.Contains(fixtures["topology"], old) {
			t.Fatalf("fixture has no %q", old)
		}
		return strings.Replace(fixtures["topology"], old, repl, 1)
	}
	for _, tc := range []struct {
		name    string
		region  string
		doc     string
		wantErr string // "" = valid
	}{
		{"fixture as is", "SEA", fixtures["topology"], ""},
		{"one-decimal cell", "SEA", edit(repeaterCell, `"location": {"lat": 47.6, "lon": -122, "grid_deg": 0.01}`), ""},
		{"null optional values", "SEA", edit(repeaterCell, `"location": null`), ""},

		// Coordinates.
		{"raw coordinate", "SEA", edit(repeaterCell, `"location": {"lat": 47.6062, "lon": -122.3321, "grid_deg": 0.01}`), "0.01° grid"},
		{"exponent hides decimals", "SEA", edit(repeaterCell, `"location": {"lat": 4.76062e1, "lon": -122.35, "grid_deg": 0.01}`), "0.01° grid"},
		{"lat without lon", "SEA", edit(repeaterCell, `"location": {"lat": 47.62, "grid_deg": 0.01}`), "0.01° grid"},
		{"coordinate as string", "SEA", edit(repeaterCell, `"location": {"lat": "47.6062", "lon": -122.35, "grid_deg": 0.01}`), "0.01° grid"},
		{"raw microdegrees", "SEA", edit(repeaterCell, `"location": {"lat": 47606213, "lon": -122332071, "grid_deg": 0.01}`), "lat = 47606213 is out of range"},
		{"lat x100", "SEA", edit(repeaterCell, `"location": {"lat": 4760.62, "lon": -122.35, "grid_deg": 0.01}`), "out of range"},
		{"lon out of range", "SEA", edit(repeaterCell, `"location": {"lat": 47.62, "lon": -222.35, "grid_deg": 0.01}`), "lon = -222.35 is out of range"},
		{"range edges", "SEA", edit(repeaterCell, `"location": {"lat": -90, "lon": 180, "grid_deg": 0.01}`), ""},

		// Integers and [0, 1] values.
		{"fractional degree", "SEA", edit(`"degree": 2, "last_heard": "2026-11-09T13:58:12Z"`, `"degree": 2.5, "last_heard": "2026-11-09T13:58:12Z"`), "degree = 2.5 is not an integer"},
		{"count with exponent", "SEA", edit(`"receptions": 10`, `"receptions": 1e1`), "not an integer"},
		{"stats.links fractional", "SEA", edit(`"links": 3,`, `"links": 3.0,`), "links = 3.0 is not an integer"},
		{"confidence above 1", "SEA", edit(`"confidence": 0.8`, `"confidence": 1.2`), "not in [0, 1]"},
		{"negative share", "SEA", edit(`"ambiguous_hop_share": 0.1`, `"ambiguous_hop_share": -0.1`), "not in [0, 1]"},
		{"finer grid", "SEA", edit(repeaterCell, `"location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.001}`), "grid_deg"},

		// Verifier bypasses (each was served before the allow-list).
		{"a: trailing JSON", "SEA", fixtures["topology"] + `{"lat": 47.6062, "lon": -122.3321}`, "trailing data"},
		{"b: point inside meta", "SEA", edit(`"source": "topology-job"`, `"source": "topology-job", "location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.01}`), "meta.location: field not in the allow-list"},
		{"b: point inside meta.window", "SEA", edit(`"to": "2026-11-09T14:00:00Z"`, `"to": "2026-11-09T14:00:00Z", "lat": 47.6062`), "meta.window.lat: field not in the allow-list"},
		{"d: latitude key", "SEA", edit(repeaterCell, repeaterCell+`, "latitude": 47.6062`), "nodes[].latitude: field not in the allow-list"},
		{"d: lng key", "SEA", edit(`"lon": -122.35`, `"lon": -122.35, "lng": -122.3321`), "location.lng: field not in the allow-list"},
		{"d: coords array", "SEA", edit(repeaterCell, repeaterCell+`, "coords": [47.6062, -122.3321]`), "nodes[].coords: field not in the allow-list"},
		{"d: numbers hidden in an allowed scalar", "SEA", edit(`"degree": 2, "last_heard": "2026-11-09T13:58:12Z"`, `"degree": [47.6062, -122.3321], "last_heard": "2026-11-09T13:58:12Z"`), "is a array, want number"},
		{"e: capitalised Companion with name", "SEA", edit(companion, `"role": "Companion", "name": "Alice"`), "role"},
		{"e: full public key", "SEA", edit(`"id": "351FC1F9"`, `"id": "351FC1F9", "pubkey": "351FC1F9AB12CD34EF56351FC1F9AB12CD34EF56351FC1F9AB12CD34EF561234"`), "nodes[].pubkey: field not in the allow-list"},

		// Re-verification: keys that contain path syntax (N1-N4).
		{"N1: dotted top-level coordinates", "SEA", edit(`"stats": {`, `"nodes[].location.lat": 47.606213, "nodes[].location.lon": -122.332071, "stats": {`), "not a plain name"},
		{"N2: dotted key inside a node", "SEA", edit(`"id": "C2", "role": "unknown"`, `"id": "C2", "location.lat": 47.606213, "location.lon": -122.332071, "role": "unknown"`), "not a plain name"},
		{"N3: dotted top-level full key", "SEA", edit(`"stats": {`, `"nodes[].id": "351FC1F9AB12CD34EF56351FC1F9AB12CD34EF56351FC1F9AB12CD34EF561234", "stats": {`), "not a plain name"},
		{"N4: dotted top-level companion name", "SEA", edit(`"stats": {`, `"nodes[].role": "companion", "nodes[].name": "Alice Smith", "stats": {`), "not a plain name"},
		{"bracket in key", "SEA", edit(`"stats": {`, `"x[0]": 1, "stats": {`), "not a plain name"},

		// Names only for repeaters and rooms (N10, N20, N21).
		{"N10: name without role", "SEA", edit(`"id": "C2", "role": "unknown", "name": null`, `"id": "C2", "name": "Alice Smith"`), "name set for role <nil>"},
		{"N20: unknown role with name", "SEA", edit(`"id": "C2", "role": "unknown", "name": null`, `"id": "C2", "role": "unknown", "name": "Alice Smith"`), "name set for role unknown"},
		{"N21: sensor with name", "SEA", edit(`"id": "C2", "role": "unknown", "name": null`, `"id": "C2", "role": "sensor", "name": "Alice Smith"`), "name set for role sensor"},
		{"room with name", "SEA", edit(`"id": "C2", "role": "unknown", "name": null`, `"id": "C2", "role": "room", "name": "SEA-ROOM"`), ""},

		// Timestamps.
		{"bad last_heard", "SEA", edit(`"last_heard": "2026-11-09T13:58:12Z"`, `"last_heard": "yesterday at 47.6062,-122.3321"`), "not an RFC 3339 time"},
		{"bad generated_at", "SEA", edit(`"generated_at": "2026-11-09T14:00:00Z"`, `"generated_at": "2026-11-09 14:00"`), "not an RFC 3339 time"},
		{"time as number", "SEA", edit(`"first_seen": "2026-11-09T12:00:00Z"`, `"first_seen": 1762689600`), "is a number, want time"},

		// Ids, names, roles.
		{"companion with name", "SEA", edit(companion, `"role": "companion", "name": "Alice's phone"`), "name set for role companion"},
		{"null role", "SEA", edit(companion, `"role": null, "name": "Alice"`), "role"},
		{"full key as id", "SEA", edit(`"id": "351FC1F9"`, `"id": "351FC1F9AB12CD34EF56"`), "not a 2-8 hex node id"},
		{"non-hex id", "SEA", edit(`"id": "C2"`, `"id": "ZZ"`), "not a 2-8 hex node id"},
		{"bad link endpoint", "SEA", edit(`"b": "C2"`, `"b": "not-hex"`), "not a 2-8 hex node id"},

		// Envelope and syntax.
		{"region mismatch", "PDX", fixtures["topology"], "meta.region"},
		{"schema version 2", "SEA", edit(`"schema_version": 1`, `"schema_version": 2`), "schema_version"},
		{"no meta", "SEA", `{"nodes": []}`, "missing meta"},
		{"not JSON", "SEA", `nope`, "not a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate(tc.region, "topology", []byte(tc.doc))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// Bypass (c): with a duplicated key the decoder keeps the last value. That is
// the value checked, and the response is re-encoded from it, so the first
// (raw) value never reaches a client.
func TestValidateDuplicateKeyNotServed(t *testing.T) {
	doc := strings.Replace(fixtures["topology"], `"lat": 47.62`, `"lat": 47.6062, "lat": 47.62`, 1)
	out, err := validate("SEA", "topology", []byte(doc))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(out), "47.6062") {
		t.Errorf("served bytes contain the shadowed raw coordinate: %s", out)
	}
}

func TestValidateEveryFixture(t *testing.T) {
	docs := map[string]string{"placement": placementFixture}
	for name, body := range fixtures {
		docs[name] = body
	}
	for name, body := range docs {
		if _, err := validate("SEA", name, []byte(body)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := validate("SEA", "secrets", []byte(fixtures["topology"])); err == nil {
		t.Error("a summary name without an allow-list must be rejected")
	}
}

func TestValidateDistance(t *testing.T) {
	residuals := func(d string) string {
		return strings.Replace(fixtures["residuals"], `"residual_db": 2.0`, `"distance_km": `+d+`, "residual_db": 2.0`, 1)
	}
	for _, tc := range []struct {
		d  string
		ok bool
	}{{"8.4", true}, {"12", true}, {"8.43", false}, {"8.4e0", false}} {
		_, err := validate("SEA", "residuals", []byte(residuals(tc.d)))
		if (err == nil) != tc.ok {
			t.Errorf("distance_km %s: err = %v, want ok=%v", tc.d, err, tc.ok)
		}
	}
}
