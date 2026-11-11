package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// LocalStore writes objects as files under Root, for development and test
// fixtures when no object storage is available.
type LocalStore struct {
	Root string
}

// Put writes data to Root/key atomically (see writeFileAtomic), so readers
// never see a half-written Parquet file.
func (s LocalStore) Put(_ context.Context, key string, data []byte) error {
	// Keys are built from row data (region), so refuse anything that could escape Root.
	// IsLocal rejects absolute paths, "..", drive-relative "C:foo" and Windows device names like NUL.
	if !filepath.IsLocal(filepath.FromSlash(key)) {
		return fmt.Errorf("local store: unsafe key %q", key)
	}
	path := filepath.Join(s.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G703: key passed filepath.IsLocal just above
		return fmt.Errorf("local store: %w", err)
	}
	// shortcut: directories created by MkdirAll are not fsynced, so a power cut can lose a brand-new partition directory; the spool (the DDIL buffer) is flat and does not have this gap.
	if err := writeFileAtomic(filepath.Dir(path), filepath.Base(path), data); err != nil {
		return fmt.Errorf("local store: %s: %w", key, err)
	}
	return nil
}

// tmpPrefix marks files still being written. They are hidden (leading dot)
// and never treated as finished data; the spool deletes leftovers at startup.
const tmpPrefix = ".tmp-"

// writeFileAtomic writes dir/name so that after a crash or power cut the file
// either holds all of data or does not exist: write a temp file, fsync it,
// rename it into place, then fsync the directory (the rename itself is only
// durable once the directory entry is on disk).
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:gosec // G703: CreateTemp chose this name inside dir; cleanup only, ENOENT after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // already failing; the write error is the one to report
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close() // already failing; the fsync error is the one to report
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil { //nolint:gosec // G703: both callers (LocalStore.Put, Spool.write) reject keys that fail filepath.IsLocal
		return fmt.Errorf("rename: %w", err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	// Windows cannot fsync a directory handle (and NTFS journals renames);
	// production runs on Linux, where this is what makes the rename durable.
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir) //nolint:gosec // G304: dir is our own output directory, opened only to fsync it
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer func() { _ = d.Close() }() // the Sync result below is what matters
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
