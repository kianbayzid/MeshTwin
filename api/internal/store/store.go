// Package store reads the JSON summaries the analytics jobs write to
// <root>/summaries/region=<REGION>/<name>.json, from a local directory or S3.
package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ErrNotFound means the summary or tile does not exist (yet).
var ErrNotFound = errors.New("summary not found")

// MaxBytes caps one summary read. Real summaries are well under 1 MB; the cap
// stops a huge or corrupt file from exhausting memory.
const MaxBytes = 8 << 20

// ErrTooLarge means the summary is over MaxBytes; it is not served.
var ErrTooLarge = errors.New("summary larger than the size cap")

// MaxTileBytes caps one terrain tile (docs/api-contract.md: 1 MB). A real
// 256x256 Terrain-RGB PNG is tens of KB.
const MaxTileBytes = 1 << 20

// readCapped reads r up to limit bytes. It reads one byte more to tell
// "exactly at the cap" from "over it".
func readCapped(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Names are the summaries the API serves, in display order. Any other file
// under summaries/ is ignored, so a stray object never becomes a route.
// "profiles" is stored only as shards (ProfileShard), never as profiles.json.
var Names = []string{"topology", "resilience", "residuals", "surprising", "profiles", "placement", "terrain"}

// ProfileShards are the shard characters of the profiles summary: the first
// hex digit of a link's smaller (upper-cased) id (docs/api-contract.md).
const ProfileShards = "0123456789ABCDEF"

// ProfileShard is the summary name of one profiles shard, "profiles/<c>",
// stored at summaries/region=<REGION>/profiles/<c>.json. Callers pass a
// character of ProfileShards, so the key has no other syntax.
func ProfileShard(c byte) string {
	return "profiles/" + string(c)
}

// validName reports whether name is a stored summary: a name in Names other
// than "profiles", or one profiles shard.
func validName(name string) bool {
	if s, ok := strings.CutPrefix(name, "profiles/"); ok {
		return len(s) == 1 && strings.Contains(ProfileShards, s)
	}
	return name != "profiles" && slices.Contains(Names, name)
}

// summaryKey is the object key of one summary, relative to the root.
func summaryKey(region, name string) string {
	return "summaries/region=" + region + "/" + name + ".json"
}

// tileKey is the object key of one terrain tile. Built only from a validated
// region and integers, so it cannot contain "..", separators or other syntax.
func tileKey(region string, z, x, y int) string {
	return fmt.Sprintf("summaries/region=%s/terrain/%d/%d/%d.png", region, z, x, y)
}

// Store is the read side of the summary bucket. Callers validate region and
// name before calling: both become part of a path or object key.
type Store interface {
	// Get returns the raw bytes of one summary, or ErrNotFound.
	Get(ctx context.Context, region, name string) ([]byte, error)
	// Tile returns the raw bytes of one terrain tile (capped at MaxTileBytes), or ErrNotFound.
	Tile(ctx context.Context, region string, z, x, y int) ([]byte, error)
	// List returns region -> stored summary names present (names in Names,
	// with profiles as its shards "profiles/0".."profiles/F").
	// It doubles as the readiness check: it fails when storage is unreachable.
	List(ctx context.Context) (map[string][]string, error)
}

// ValidRegion accepts 2-8 upper-case ASCII letters or digits (IATA codes),
// the same rule as the ingester. The region becomes part of a path or object
// key, so this is a trust boundary: it blocks separators, "..", and ':'.
func ValidRegion(r string) bool {
	if len(r) < 2 || len(r) > 8 {
		return false
	}
	for _, c := range r {
		if !('A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// parseKey splits "summaries/region=SEA/topology.json" (relative to the
// root) into its region and name, and "summaries/region=SEA/profiles/0.json"
// into SEA and "profiles/0"; ok is false for anything else.
func parseKey(key string) (region, name string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) == 4 && parts[2] == "profiles" {
		parts = []string{parts[0], parts[1], "profiles/" + parts[3]}
	}
	if len(parts) != 3 || parts[0] != "summaries" {
		return "", "", false
	}
	region, found := strings.CutPrefix(parts[1], "region=")
	name, isJSON := strings.CutSuffix(parts[2], ".json")
	if !found || !isJSON || !ValidRegion(region) || !validName(name) {
		return "", "", false
	}
	return region, name, true
}

// addKey records one listed key in m, keeping names in Names order (shards
// sorted under "profiles").
func addKey(m map[string][]string, key string) {
	region, name, ok := parseKey(key)
	if !ok {
		return
	}
	m[region] = append(m[region], name)
	slices.SortFunc(m[region], func(a, b string) int {
		return cmp.Or(slices.Index(Names, base(a))-slices.Index(Names, base(b)), strings.Compare(a, b))
	})
}

// base is a summary name without its shard ("profiles/0" -> "profiles").
func base(name string) string {
	b, _, _ := strings.Cut(name, "/")
	return b
}
