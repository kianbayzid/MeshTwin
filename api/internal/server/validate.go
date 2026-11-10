package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
)

// What the validator guarantees for every summary it serves: only the
// contract's fields, of the contract's types; locations are 0.01° cells within
// lat/lon range; ids are 2-8 hex prefixes, never full keys; only repeaters and
// rooms carry names; counts are integers and confidences/shares are in [0, 1];
// times are RFC 3339; nothing beyond the decoded document is served.
//
// What it does not guarantee: the content of free-text strings (repeater
// names, possible_reasons, model.name, source). A repeater could advertise a
// name that encodes a location; names are public adverts of public
// infrastructure, and policing free text is out of scope (a design limit).
// It also does not check that values are true, only that they are shaped
// and precise enough not to reveal more than the contract allows.

// validate checks a stored summary against the contract and its privacy
// rules, and returns the bytes to serve: the validated document re-encoded,
// never the raw file. Whatever the file contains beyond what was decoded
// (trailing data, the earlier copy of a duplicated key) therefore cannot
// reach a client. The analytics jobs enforce the same rules; this is defence
// in depth, so a bug there or a hand-edited file never leaks a raw coordinate.
//
// Rules:
//   - shape: every key is one plain name (no ".", "[" or "]"), every key path
//     is in the allow-list for this summary, and its value has the allowed
//     kind (or is null); "time" values must parse as RFC 3339;
//   - any object carrying lat or lon is a 0.01° grid cell: both present, at
//     most two decimals, within [-90, 90] / [-180, 180], and grid_deg 0.01;
//   - count fields (intFields) are integers; unitFields are in [0, 1];
//   - every node id or link endpoint (id, a, b, to, spofs_removed[]) is 2-8 hex chars;
//   - distance_km has at most one decimal (0.1 km, from snapped cells);
//   - role is one of the contract's roles, and only repeaters and rooms
//     (named public infrastructure) may have a non-null name;
//   - meta.schema_version is 1 and meta.region matches the requested region.
//
// Duplicate keys are not rejected: the decoder keeps the last one, that is the
// value checked, and it is the only one re-encoded and served.
func validate(region, name string, data []byte) ([]byte, error) {
	paths, ok := allowed[name]
	if !ok {
		return nil, fmt.Errorf("no allow-list for summary %q", name)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keep numbers as written, so "47.123" is not rounded before we look at it
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON document")
	}
	meta, _ := doc["meta"].(map[string]any)
	if meta == nil {
		return nil, errors.New("missing meta")
	}
	if v, _ := meta["schema_version"].(json.Number); v != "1" {
		return nil, fmt.Errorf("meta.schema_version is %v, want 1", meta["schema_version"])
	}
	if r, _ := meta["region"].(string); r != region {
		return nil, fmt.Errorf("meta.region is %q, want %q", r, region)
	}
	for k, v := range doc {
		if err := walkKey(paths, "", k, v); err != nil {
			return nil, err
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode: %w", err)
	}
	return out, nil
}

// walk checks the value at path and everything below it.
func walk(paths map[string]string, path string, v any) error {
	want, ok := paths[path]
	if !ok {
		return fmt.Errorf("%s: field not in the allow-list", path)
	}
	switch k := kindOf(v); {
	case want == "time" && k == "string":
		if _, err := time.Parse(time.RFC3339, v.(string)); err != nil {
			return fmt.Errorf("%s = %q is not an RFC 3339 time", path, v)
		}
	case k != "null" && k != want:
		return fmt.Errorf("%s: is a %s, want %s", path, k, want)
	}
	switch v := v.(type) {
	case map[string]any:
		if path != "meta.window" { // window.from/to are times, not link endpoints
			if err := checkObject(path, v); err != nil {
				return err
			}
		}
		for k, child := range v {
			if err := walkKey(paths, path, k, child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := walk(paths, path+"[]", child); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkKey walks the value under key k of the object at path. A key must be
// one plain name: "nodes[].location.lat" as a literal key would otherwise
// match an allowed path while skipping the object checks one level down.
func walkKey(paths map[string]string, path, k string, v any) error {
	if strings.ContainsAny(k, ".[]") {
		return fmt.Errorf("%s: key %q is not a plain name", path, k)
	}
	if path != "" {
		k = path + "." + k
	}
	return walk(paths, k, v)
}

// kindOf names a decoded JSON value's kind, as used in the allow-list.
func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number, float64:
		return "number"
	case bool:
		return "bool"
	}
	return "null"
}

// Fields matched by key name wherever they appear as numbers. "nodes" and
// "links" are also list names; only their numeric uses (stats, baseline,
// summary counts) are checked here. antenna_height_m is a measurement, not a
// count, so it may have decimals.
var (
	intFields = []string{"degree", "observations", "samples", "nodes", "links", "receptions", "flood_receptions",
		"observers", "unresolved_hops", "reachable_pairs", "reachable_pairs_lost", "nodes_cut_off", "rank",
		"coverage_gain_nodes", "reachable_pairs_gained", "candidates_scored", "spofs_checked_top"}
	unitFields = []string{"confidence", "score", "reachable_share", "reachable_share_after", "ambiguous_hop_share"}
)

// number is v as a float64; NaN when v is not a JSON number, so every range
// comparison with it fails.
func number(v any) float64 {
	n, ok := v.(json.Number)
	if !ok {
		return math.NaN()
	}
	f, err := n.Float64()
	if err != nil {
		return math.NaN()
	}
	return f
}

var roles = []string{"repeater", "companion", "room", "sensor", "unknown"}

func checkObject(path string, o map[string]any) error {
	_, hasLat := o["lat"]
	_, hasLon := o["lon"]
	if hasLat || hasLon {
		for _, k := range []string{"lat", "lon"} {
			if !maxDecimals(o[k], 2) {
				return fmt.Errorf("%s.%s = %v is not on the 0.01° grid", path, k, o[k])
			}
		}
		// Range catches a missing unit conversion: raw microdegrees (47606213)
		// have no decimals at all and would otherwise pass the grid check.
		if lat := number(o["lat"]); !(lat >= -90 && lat <= 90) { // !(...) also rejects NaN
			return fmt.Errorf("%s.lat = %v is out of range", path, o["lat"])
		}
		if lon := number(o["lon"]); !(lon >= -180 && lon <= 180) {
			return fmt.Errorf("%s.lon = %v is out of range", path, o["lon"])
		}
		if g, _ := o["grid_deg"].(json.Number); g != "0.01" {
			return fmt.Errorf("%s.grid_deg = %v, want 0.01", path, o["grid_deg"])
		}
	}
	for k, v := range o {
		n, isNum := v.(json.Number)
		switch {
		case !isNum:
		case slices.Contains(intFields, k) && strings.ContainsAny(string(n), ".eE"):
			return fmt.Errorf("%s.%s = %v is not an integer", path, k, v)
		case slices.Contains(unitFields, k) && !(number(n) >= 0 && number(n) <= 1):
			return fmt.Errorf("%s.%s = %v is not in [0, 1]", path, k, v)
		}
	}
	for _, k := range []string{"id", "a", "b", "to"} {
		if v, ok := o[k]; ok && !nodeID(v) {
			return fmt.Errorf("%s.%s = %v is not a 2-8 hex node id", path, k, v)
		}
	}
	if list, ok := o["spofs_removed"].([]any); ok {
		for _, v := range list {
			if !nodeID(v) {
				return fmt.Errorf("%s.spofs_removed has %v, not a 2-8 hex node id", path, v)
			}
		}
	}
	if d, ok := o["distance_km"]; ok && d != nil && !maxDecimals(d, 1) {
		return fmt.Errorf("%s.distance_km = %v has more than one decimal", path, d)
	}
	if role, ok := o["role"]; ok {
		if s, _ := role.(string); !slices.Contains(roles, s) {
			return fmt.Errorf("%s.role = %v is not one of %v", path, role, roles)
		}
	}
	// model.name is the propagation model's name; every other name is a node's.
	if path != "model" && o["name"] != nil && o["role"] != "repeater" && o["role"] != "room" {
		return fmt.Errorf("%s: name set for role %v (only repeater and room may have one)", path, o["role"])
	}
	return nil
}

// maxDecimals reports whether v is a JSON number with at most n decimals,
// read from the text as written: exact, with no float rounding to argue about.
func maxDecimals(v any, n int) bool {
	num, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(num), "eE") {
		return false
	}
	_, frac, _ := strings.Cut(string(num), ".")
	return len(frac) <= n
}

func nodeID(v any) bool {
	s, ok := v.(string)
	if !ok || len(s) < 2 || len(s) > 8 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'F' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
