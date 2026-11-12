package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"regexp"
)

// assetTypes pins the types the console needs. Go's built-in table has no
// fonts, distroless has no /etc/mime.types, and on Windows the registry can
// map .js to text/plain, which nosniff then refuses to run.
var assetTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".woff":  "font/woff",
}

// index serves the console's entry page. no-cache: it names the current
// content-hashed assets, so a browser must revalidate it on every load.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.static, "index.html")
}

// asset serves one file under assets/. Vite puts a content hash in every
// name, so a URL never changes meaning and can be cached for a year.
// Directories are 404 (no listings).
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[1:] // "assets/..."; the mux has already cleaned the path
	fi, err := fs.Stat(s.static, name)
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	if ct, ok := assetTypes[path.Ext(name)]; ok {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFileFS(w, r, s.static, name) //nolint:gosec // G703: an fs.FS path, fs.ValidPath rejects ".." and absolute paths; no OS path is built
}

// basemapName is the contract's {name}.pmtiles. It never contains a slash or
// a dot segment, so it can only name a file directly inside the basemap dir.
var basemapName = regexp.MustCompile(`^[a-z0-9-]{1,32}\.pmtiles$`)

// SetBasemapDir enables GET /basemap/{name}.pmtiles from dir. Empty leaves it
// off (404). The directory is opened once as an os.Root, so no request can
// reach outside it, even through a symlink.
func (s *Server) SetBasemapDir(dir string) error {
	if dir == "" {
		s.basemap = nil
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("basemap dir: %w", err)
	}
	s.basemap = root
	return nil
}

// basemapFile serves a PMTiles archive. http.ServeContent handles Range (206,
// Accept-Ranges: bytes), which is how the map client reads it. A name outside
// the contract's pattern is 404, like any other file that does not exist.
func (s *Server) basemapFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if s.basemap == nil || !basemapName.MatchString(name) {
		writeError(w, http.StatusNotFound, "not_found", "no such basemap")
		return
	}
	f, err := s.basemap.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Error("open basemap", "name", name, "err", err)
		}
		writeError(w, http.StatusNotFound, "not_found", "no such basemap")
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not_found", "no such basemap")
		return
	}
	// octet-stream: application/vnd.pmtiles is not an IANA-registered type.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
