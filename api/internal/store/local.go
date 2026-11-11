package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// Local reads summaries from a directory (dev, tests, a mounted volume).
type Local struct{ Root string }

func (l Local) Get(_ context.Context, region, name string) ([]byte, error) {
	return l.read(summaryKey(region, name), MaxBytes)
}

func (l Local) Tile(_ context.Context, region string, z, x, y int) ([]byte, error) {
	return l.read(tileKey(region, z, x, y), MaxTileBytes)
}

// read returns the file at key (slash-separated, relative to Root), up to limit bytes.
func (l Local) read(key string, limit int) ([]byte, error) {
	rel := filepath.FromSlash(key)
	// Second line of defence after ValidRegion: never leave Root.
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("path %q escapes the root", rel)
	}
	// OpenInRoot also refuses a symlink that points outside Root.
	f, err := os.OpenInRoot(l.Root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", key, err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error cannot lose data
	b, err := readCapped(f, limit)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return b, nil
}

func (l Local) List(_ context.Context) (map[string][]string, error) {
	// Glob does not report a missing root; a store that isn't there is not ready.
	if _, err := os.Stat(l.Root); err != nil {
		return nil, fmt.Errorf("summary root: %w", err)
	}
	files, err := fs.Glob(os.DirFS(l.Root), "summaries/region=*/*.json")
	if err != nil {
		return nil, fmt.Errorf("list summaries: %w", err)
	}
	m := map[string][]string{}
	for _, f := range files {
		addKey(m, path.Clean(f))
	}
	return m, nil
}
