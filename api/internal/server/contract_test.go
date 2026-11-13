package server

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"meshtwin/api/internal/store"
)

// contractExamples reads docs/api-contract.md at test time and returns its
// complete JSON examples by what they are, so the test follows the doc.
func contractExamples(t *testing.T) map[string]map[string]any {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/api-contract.md")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	// Identify each example by a key only it has. ("model" is in residuals and
	// the profile response, so residuals is found by "summary".)
	marker := map[string]string{
		"regions": "regions", "stats": "topology", "baseline": "resilience", "summary": "residuals",
		"observed_but_predicted_impossible": "surprising", "params": "profiles", "candidates": "placement",
		"grid": "coverage", "encoding": "terrain", "ingest": "status",
	}
	out := map[string]map[string]any{}
	for _, m := range regexp.MustCompile("(?s)```json\r?\n(.*?)```").FindAllStringSubmatch(string(doc), -1) {
		var v map[string]any
		if json.Unmarshal([]byte(m[1]), &v) != nil {
			continue // the meta fragment is not a whole document
		}
		for k, name := range marker {
			if _, ok := v[k]; ok {
				out[name] = v
			}
		}
	}
	if len(out) != len(marker) {
		t.Fatalf("found examples %v, want all of %v", slices.Sorted(maps.Keys(out)), slices.Sorted(maps.Values(marker)))
	}
	return out
}

// keyPaths lists every object key path in v ("links[].snr_db.median");
// arrays contribute the union of their elements' keys.
// storedForm is the file a job writes for name. Every example is that file,
// except profiles: the example is one route response, and a profiles shard
// holds its entries in a list.
func storedForm(ex map[string]map[string]any, name string) map[string]any {
	p := ex[name]
	if name != "profiles" {
		return p
	}
	return map[string]any{"meta": p["meta"], "model": p["model"], "params": p["params"], "profiles": []any{p["profile"]}}
}

func keyPaths(prefix string, v any, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			out[prefix+k] = true
			keyPaths(prefix+k+".", child, out)
		}
	case []any:
		for _, child := range v {
			keyPaths(prefix[:len(prefix)-1]+"[].", child, out)
		}
	}
}

func sameKeys(t *testing.T, what string, want, got any) {
	t.Helper()
	w, g := map[string]bool{}, map[string]bool{}
	keyPaths("", want, w)
	keyPaths("", got, g)
	if !maps.Equal(w, g) {
		t.Errorf("%s: served keys differ from the contract\n contract: %v\n served:   %v",
			what, slices.Sorted(maps.Keys(w)), slices.Sorted(maps.Keys(g)))
	}
}

// TestContractExamples stores the contract's own examples as summaries and
// checks the API serves documents with exactly the same keys. The examples
// also pass the privacy validation, which keeps the doc honest.
func TestContractExamples(t *testing.T) {
	ex := contractExamples(t)
	root := t.TempDir()
	for _, name := range store.Names {
		if name == "profiles" {
			// One shard per character, each holding only its own pairs (most are empty).
			for i := range len(store.ProfileShards) {
				doc := storedForm(ex, name)
				mine := []any{} // [] rather than null when empty
				for _, p := range doc["profiles"].([]any) {
					o := p.(map[string]any)
					if pairKey(o["a"].(string), o["b"].(string))[0][0] == store.ProfileShards[i] {
						mine = append(mine, p)
					}
				}
				doc["profiles"] = mine
				b, _ := json.Marshal(doc)
				writeSummary(t, root, "SEA", store.ProfileShard(store.ProfileShards[i]), string(b))
			}
			continue
		}
		b, _ := json.Marshal(storedForm(ex, name))
		writeSummary(t, root, "SEA", name, string(b))
	}
	h := New(store.Local{Root: root}, Embedded, time.Minute).Handler()
	for name, url := range map[string]string{
		"topology":   "/api/v1/regions/SEA/topology?min_confidence=0.5",
		"resilience": "/api/v1/regions/SEA/resilience",
		"residuals":  "/api/v1/regions/SEA/links/residuals",
		"surprising": "/api/v1/regions/SEA/links/surprising",
		"profiles":   "/api/v1/regions/SEA/links/3491927B/351FC1F9/profile",
		"placement":  "/api/v1/regions/SEA/placement",
		"coverage":   "/api/v1/regions/SEA/coverage",
		"terrain":    "/api/v1/regions/SEA/terrain.json",
	} {
		rec := get(t, h, url)
		if rec.Code != 200 {
			t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body)
			continue
		}
		sameKeys(t, name, ex[name], decode(t, rec))
	}
	rec := get(t, h, "/api/v1/regions")
	sameKeys(t, "regions", ex["regions"], decode(t, rec))
	if !strings.Contains(rec.Body.String(), `"profiles"`) {
		t.Errorf("regions: want profiles listed (all 16 shards valid): %s", rec.Body)
	}
}

// TestRealAnalyticsOutput serves summaries the analytics CLI actually wrote
// (api/testdata, generated from testdata/parquet/sea-seed1-2h-hash1): they
// must pass validation and be served as stored.
func TestRealAnalyticsOutput(t *testing.T) {
	h := New(store.Local{Root: "../../testdata"}, Embedded, time.Minute).Handler()
	// coverage.json is added once the placement job writes it; until then it is 503 (not_ready) and unlisted.
	summaries, coverageStatus := 7, 503
	if _, err := os.Stat("../../testdata/summaries/region=SEA/coverage.json"); err == nil {
		summaries, coverageStatus = 8, 200
	}
	for _, tc := range []struct {
		url    string
		status int
	}{
		{"/api/v1/regions/SEA/coverage", coverageStatus},
		{"/api/v1/regions/SEA/topology", 200},
		{"/api/v1/regions/SEA/topology?min_confidence=0.9", 200},
		{"/api/v1/regions/SEA/resilience", 200},
		{"/api/v1/regions/SEA/links/residuals?sort=observations&limit=5", 200},
		{"/api/v1/regions/SEA/links/surprising", 200},
		{"/api/v1/regions/SEA/placement?top=3", 200},
		{"/api/v1/regions/SEA/terrain.json", 200},
		{"/api/v1/regions/SEA/terrain/8/40/89.png", 200},
		{"/api/v1/regions/SEA/links/04080AC9/3491927B/profile", 200},
		{"/api/v1/regions/SEA/links/351FC1F9/0562C16B/profile", 200},
	} {
		if rec := get(t, h, tc.url); rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %.200s", tc.url, rec.Code, tc.status, rec.Body)
		}
	}
	stored, err := os.ReadFile("../../testdata/summaries/region=SEA/topology.json")
	if err != nil {
		t.Fatal(err)
	}
	// Served bytes are re-encoded from the validated document: same content, not the same bytes.
	var want, got any
	if err := json.Unmarshal(stored, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/regions/SEA/topology").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Error("unfiltered topology content differs from the stored file")
	}
	var regions struct {
		Regions []regionInfo `json:"regions"`
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/regions").Body.Bytes(), &regions); err != nil || len(regions.Regions) != 1 {
		t.Fatalf("regions: %v %+v", err, regions)
	}
	if r := regions.Regions[0]; r.Region != "SEA" || len(r.Summaries) != summaries || r.Nodes == nil {
		t.Errorf("SEA = %+v", r)
	}
}

// shapeOf lists key path -> JSON kind for v, in the allow-list's notation.
func shapeOf(path string, v any, out map[string]string) {
	if path != "" {
		k := kindOf(v)
		if str, ok := v.(string); ok {
			if _, err := time.Parse(time.RFC3339, str); err == nil {
				k = "time" // the contract's timestamps
			}
		}
		if k != "null" || out[path] == "" {
			out[path] = k
		}
	}
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			shapeOf(strings.TrimPrefix(path+"."+k, "."), child, out)
		}
	case []any:
		for _, child := range v {
			shapeOf(path+"[]", child, out)
		}
	}
}

// TestAllowListMatchesContract keeps the allow-list and the contract's
// examples identical, so neither can change without the other.
func TestAllowListMatchesContract(t *testing.T) {
	ex := contractExamples(t)
	for _, name := range store.Names {
		want := map[string]string{}
		shapeOf("", storedForm(ex, name), want)
		if !maps.Equal(allowed[name], want) {
			for p, k := range want {
				if allowed[name][p] != k {
					t.Errorf("%s: contract has %s (%s), allow-list has %q", name, p, k, allowed[name][p])
				}
			}
			for p, k := range allowed[name] {
				if _, ok := want[p]; !ok {
					t.Errorf("%s: allow-list has %s (%s), not in the contract", name, p, k)
				}
			}
		}
	}
	if len(allowed) != len(store.Names) {
		t.Errorf("allow-list has %d summaries, want %d", len(allowed), len(store.Names))
	}
}
