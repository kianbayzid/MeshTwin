package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// LocalStore writes objects as files under Root, for development and test
// fixtures when no object storage is available.
type LocalStore struct {
	Root string
}

// Put writes data to Root/key atomically: a temp file in the same directory is
// renamed into place, so readers never see a half-written Parquet file.
func (s LocalStore) Put(_ context.Context, key string, data []byte) error {
	// Keys are built from row data (region), so refuse anything that could escape Root.
	// IsLocal rejects absolute paths, "..", drive-relative "C:foo" and Windows device names like NUL.
	if !filepath.IsLocal(filepath.FromSlash(key)) {
		return fmt.Errorf("local store: unsafe key %q", key)
	}
	path := filepath.Join(s.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("local store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("local store: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	// shortcut: no tmp.Sync() before rename, so a power cut can lose the latest file; fine for dev and fixtures, add it if LocalStore becomes the DDIL buffer (Phase 7).
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("local store: write %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("local store: close %s: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("local store: rename %s: %w", key, err)
	}
	return nil
}
