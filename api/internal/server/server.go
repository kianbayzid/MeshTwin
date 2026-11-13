// Package server is the read-only REST API and the console it serves (docs/api-contract.md).
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"meshtwin/api/internal/store"
)

// all: so Vite chunks whose names start with "_" are not silently dropped.
//
//go:embed all:console
var embedded embed.FS

// Embedded is the console compiled into the binary: index.html plus assets/
// (the image build copies web/dist over the committed placeholder). console/
// is checked at compile time by go:embed, so Sub cannot fail.
var Embedded, _ = fs.Sub(embedded, "console")

var errInvalid = errors.New("summary failed validation")

// Server serves summaries from a Store. Loaded summaries are validated once
// and cached for TTL.
type Server struct {
	store   store.Store
	static  fs.FS
	basemap *os.Root // nil: /basemap/ is 404 (see SetBasemapDir)
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]entry

	prom        *url.URL // nil: GET /api/v1/status is 503 (see SetPrometheus)
	statusCache statusCache
}

type entry struct {
	v   any
	err error
	at  time.Time
}

// New returns a Server. static is the console (Embedded, or a directory with
// the same layout for a dev override); ttl is how long a loaded summary is reused.
func New(st store.Store, static fs.FS, ttl time.Duration) *Server {
	return &Server{store: st, static: static, ttl: ttl, now: time.Now, cache: map[string]entry{}}
}

// Handler returns every route wrapped in security headers and metrics.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /api/v1/regions", s.regions)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/regions/{region}/topology", s.summaryHandler("topology", topologyQuery))
	mux.HandleFunc("GET /api/v1/regions/{region}/resilience", s.summaryHandler("resilience", nil))
	mux.HandleFunc("GET /api/v1/regions/{region}/links/residuals", s.summaryHandler("residuals", residualsQuery))
	mux.HandleFunc("GET /api/v1/regions/{region}/links/surprising", s.summaryHandler("surprising", nil))
	mux.HandleFunc("GET /api/v1/regions/{region}/links/{a}/{b}/profile", s.profile)
	mux.HandleFunc("GET /api/v1/regions/{region}/placement", s.summaryHandler("placement", placementQuery))
	mux.HandleFunc("GET /api/v1/regions/{region}/coverage", s.summaryHandler("coverage", nil))
	mux.HandleFunc("GET /api/v1/regions/{region}/terrain.json", s.summaryHandler("terrain", nil))
	// A wildcard must be a whole segment, so "{y}.png" is matched as {file} and parsed in tile.
	mux.HandleFunc("GET /api/v1/regions/{region}/terrain/{z}/{x}/{file}", s.tile)
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /assets/", s.asset)
	mux.HandleFunc("GET /basemap/{file}", s.basemapFile)
	return instrument(mux)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, err := s.store.List(ctx); err != nil {
		slog.Warn("summary store not reachable", "err", err)
		http.Error(w, "summary store not reachable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

func (s *Server) summaryHandler(name string, q query) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		region, ok := regionParam(w, r)
		if !ok {
			return
		}
		var rewrite func([]byte) ([]byte, error)
		if q != nil {
			var err error
			if rewrite, err = q(r.URL.Query()); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), errBadRequest.Error()+": "))
				return
			}
		}
		data, err := s.summary(r.Context(), region, name)
		if err != nil {
			s.writeLoadError(w, r, region, name, err)
			return
		}
		if rewrite != nil {
			if data, err = rewrite(data); err != nil {
				slog.Error("rewrite summary", "region", region, "summary", name, "err", err)
				writeError(w, http.StatusInternalServerError, "internal", "could not process the summary")
				return
			}
		}
		writeJSON(w, r, data)
	}
}

// regionParam returns the request's region, upper-cased, or writes a 400 and
// returns false. The region becomes part of a storage key: a trust boundary.
func regionParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	// ASCII check first: strings.ToUpper maps "ſ" to "S" and the Kelvin sign to "K".
	raw := r.PathValue("region")
	region := strings.ToUpper(raw)
	if strings.ContainsFunc(raw, func(c rune) bool { return c > unicode.MaxASCII }) || !store.ValidRegion(region) {
		writeError(w, http.StatusBadRequest, "bad_request", "region must be 2-8 ASCII letters or digits")
		return "", false
	}
	return region, true
}

// maxTileZoom is the deepest zoom the contract allows.
const maxTileZoom = 14

// tile serves one Terrain-RGB PNG. Tiles are not cached in memory: there can
// be many, each up to 1 MB, so a map would grow without bound, and clients
// already cache them for an hour (max-age=3600, then a 304 by ETag).
// shortcut: every request reads storage; add a small LRU if storage load shows up.
func (s *Server) tile(w http.ResponseWriter, r *http.Request) {
	region, ok := regionParam(w, r)
	if !ok {
		return
	}
	yStr, isPNG := strings.CutSuffix(r.PathValue("file"), ".png")
	z, zOK := tileCoord(r.PathValue("z"), maxTileZoom+1)
	x, xOK := tileCoord(r.PathValue("x"), 1<<z)
	y, yOK := tileCoord(yStr, 1<<z)
	if !isPNG || !zOK || !xOK || !yOK {
		writeError(w, http.StatusBadRequest, "bad_request",
			"tile must be {z}/{x}/{y}.png: decimal integers without sign or leading zeros, 0 <= z <= 14, 0 <= x, y < 2^z")
		return
	}
	b, err := s.store.Tile(r.Context(), region, z, x, y)
	if err == nil {
		err = checkTile(b)
	}
	switch {
	case err == nil:
		writeBody(w, r, b, "image/png", "max-age=3600")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no terrain tile %d/%d/%d for region %s", z, x, y, region))
	case errors.Is(err, store.ErrTooLarge), errors.Is(err, errInvalid):
		validationFailures.WithLabelValues("terrain_tile").Inc()
		slog.Error("terrain tile failed validation; not serving it", "region", region, "z", z, "x", x, "y", y, "err", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "terrain tile is not available")
	default:
		slog.Error("load terrain tile", "region", region, "z", z, "x", x, "y", y, "err", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "summary store unavailable")
	}
}

// tileCoord parses s as a tile coordinate in [0, limit). Only the canonical
// form is accepted (ASCII digits only: no sign, no leading zeros, at most 5
// digits, enough for 2^14), so each tile has exactly one URL and Atoi can
// neither fail nor overflow.
func tileCoord(s string, limit int) (int, bool) {
	if s == "" || len(s) > 5 || len(s) > 1 && s[0] == '0' || strings.ContainsFunc(s, func(c rune) bool { return c < '0' || c > '9' }) {
		return 0, false
	}
	n, _ := strconv.Atoi(s) // cannot fail: 1-5 ASCII digits
	return n, n < limit
}

// checkTile accepts only a PNG of the contract's size. DecodeConfig checks the
// 8-byte signature and reads the IHDR header; the pixels are not decoded.
func checkTile(b []byte) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%w: %w", errInvalid, err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		return fmt.Errorf("%w: tile is %dx%d, want 256x256", errInvalid, cfg.Width, cfg.Height)
	}
	return nil
}

// summary returns a validated summary, re-encoded from what was checked. A
// file that breaks a rule is never served: it is logged, counted, and reported
// to the client as not_ready with a generic message (the reason stays in our logs).
func (s *Server) summary(ctx context.Context, region, name string) ([]byte, error) {
	return cached(s, region+"/"+name, func() ([]byte, error) {
		b, err := s.store.Get(ctx, region, name)
		switch {
		case errors.Is(err, store.ErrTooLarge): // rejected like a file that fails validation
		case err != nil:
			return nil, err
		default:
			b, err = validate(region, name, b)
		}
		if err != nil {
			validationFailures.WithLabelValues(base(name)).Inc()
			slog.Error("summary failed validation; not serving it", "region", region, "summary", name, "err", err)
			return nil, fmt.Errorf("%w: %w", errInvalid, err)
		}
		recordGenerated(region, base(name), b)
		return b, nil
	})
}

func (s *Server) list(ctx context.Context) (map[string][]string, error) {
	return cached(s, "", func() (map[string][]string, error) { return s.store.List(ctx) })
}

// cached memoises fetch under key for s.ttl. Results that will not change by
// retrying now (a value, a missing file, a rejected file) are cached; storage
// errors are not, so an outage clears as soon as storage is back.
// shortcut: concurrent misses on one key each fetch (no singleflight); fine at
// a handful of summaries per region, add x/sync/singleflight if storage load matters.
func cached[T any](s *Server, key string, fetch func() (T, error)) (T, error) {
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && s.now().Sub(e.at) < s.ttl {
		v, _ := e.v.(T)
		return v, e.err
	}
	v, err := fetch()
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, errInvalid) {
		s.mu.Lock()
		s.cache[key] = entry{v: v, err: err, at: s.now()}
		s.mu.Unlock()
	}
	return v, err
}

// writeLoadError maps a load failure to the contract's codes: a region with
// no summaries at all is 404; a region that has some, but not this one (or
// this one was rejected, or storage is down), is 503 not_ready.
func (s *Server) writeLoadError(w http.ResponseWriter, r *http.Request, region, name string, err error) {
	switch {
	case errors.Is(err, errInvalid):
		writeError(w, http.StatusServiceUnavailable, "not_ready", fmt.Sprintf("%s for region %s is not available", name, region))
	case errors.Is(err, store.ErrNotFound):
		list, lerr := s.list(r.Context())
		if lerr == nil && len(list[region]) == 0 {
			writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no summaries for region %s", region))
			return
		}
		writeError(w, http.StatusServiceUnavailable, "not_ready", fmt.Sprintf("no %s for region %s yet", name, region))
	default:
		slog.Error("load summary", "region", region, "summary", name, "err", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "summary store unavailable")
	}
}

type regionInfo struct {
	Region    string     `json:"region"`
	UpdatedAt *time.Time `json:"updated_at"`
	Nodes     *int       `json:"nodes"`
	Links     *int       `json:"links"`
	Summaries []string   `json:"summaries"`
}

// regions lists regions with at least one servable summary. A rejected
// summary is left out of "summaries", so the list only names what the API
// will actually return.
func (s *Server) regions(w http.ResponseWriter, r *http.Request) {
	list, err := s.list(r.Context())
	if err != nil {
		slog.Error("list summaries", "err", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "summary store unavailable")
		return
	}
	out := []regionInfo{}
	for _, region := range slices.Sorted(maps.Keys(list)) {
		info := regionInfo{Region: region, Summaries: []string{}}
		for _, name := range store.Names {
			docs := s.servable(r.Context(), region, name, list[region])
			if docs == nil {
				continue
			}
			info.Summaries = append(info.Summaries, name)
			for _, data := range docs {
				addHead(&info, name, data)
			}
		}
		if len(info.Summaries) > 0 {
			out = append(out, info)
		}
	}
	data, err := json.Marshal(map[string]any{"regions": out})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "encode regions")
		return
	}
	writeJSON(w, r, data)
}

// servable returns the validated documents behind summary name, or nil if
// the API would not serve it. profiles counts only when all 16 shards are
// listed and valid: up to 16 reads per region on a cache miss, cached like any
// summary after that, and no read at all while a shard is missing from the listing.
func (s *Server) servable(ctx context.Context, region, name string, listed []string) [][]byte {
	parts := []string{name}
	if name == "profiles" {
		parts = nil
		for i := range len(store.ProfileShards) {
			parts = append(parts, store.ProfileShard(store.ProfileShards[i]))
		}
	}
	for _, p := range parts {
		if !slices.Contains(listed, p) {
			return nil
		}
	}
	docs := make([][]byte, 0, len(parts))
	for _, p := range parts {
		data, err := s.summary(ctx, region, p)
		if err != nil {
			return nil
		}
		docs = append(docs, data)
	}
	return docs
}

// addHead folds one validated summary's generated_at and topology counts into info.
func addHead(info *regionInfo, name string, data []byte) {
	var head struct {
		Meta struct {
			GeneratedAt time.Time `json:"generated_at"`
		} `json:"meta"`
		Stats *struct {
			Nodes int `json:"nodes"`
			Links int `json:"links"`
		} `json:"stats"`
	}
	if json.Unmarshal(data, &head) != nil {
		return
	}
	// Terrain changes only with the DEM; it says nothing about how fresh the mesh data is.
	if t := head.Meta.GeneratedAt.UTC(); name != "terrain" && (info.UpdatedAt == nil || t.After(*info.UpdatedAt)) {
		info.UpdatedAt = &t
	}
	if name == "topology" && head.Stats != nil {
		info.Nodes, info.Links = &head.Stats.Nodes, &head.Stats.Links
	}
}

// base is a summary's name without its shard ("profiles/0" -> "profiles"),
// for metric labels and validation.
func base(name string) string {
	b, _, _ := strings.Cut(name, "/")
	return b
}

// writeJSON sends data with an ETag of exactly these bytes (the validated,
// re-encoded summary, after any filtering), so a filtered response gets its
// own. A matching If-None-Match gets 304 and no body.
func writeJSON(w http.ResponseWriter, r *http.Request, data []byte) {
	writeBody(w, r, data, "application/json", "max-age=60")
}

// writeBody sends data with an ETag that is a hash of the bytes; a matching
// If-None-Match gets 304 and no body.
func writeBody(w http.ResponseWriter, r *http.Request, data []byte, contentType, cacheControl string) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControl)
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", contentType)
	// data is JSON we re-encoded or a PNG whose header we checked, sent with its own Content-Type, nosniff and a strict CSP (see instrument).
	_, _ = w.Write(data)
}

// etagMatch implements If-None-Match's weak comparison: "*", or any listed
// tag equal to ours with or without a W/ prefix.
func etagMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // a 503 must not stick in caches for a minute
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
