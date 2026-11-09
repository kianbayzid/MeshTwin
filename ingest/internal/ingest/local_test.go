package ingest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalStorePut(t *testing.T) {
	root := t.TempDir()
	s := LocalStore{Root: root}
	ctx := context.Background()
	key := "receptions/region=SEA/date=2026-11-01/hour=00/part-ab.parquet"

	for _, data := range []string{"first version", "v2"} { // second put overwrites, shorter
		if err := s.Put(ctx, key, []byte(data)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
		if err != nil || string(got) != data {
			t.Fatalf("read back = %q, %v; want %q", got, err, data)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "receptions/region=SEA/date=2026-11-01/hour=00"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir has %d entries (%v), want 1 (no temp files left)", len(entries), err)
	}
}

func TestLocalStoreRejectsUnsafeKeys(t *testing.T) {
	root := t.TempDir()
	s := LocalStore{Root: filepath.Join(root, "store")}
	keys := []string{
		"",
		"../escape.parquet",
		"receptions/../../escape.parquet",
		"/abs/x.parquet",
		filepath.Join(root, "abs.parquet"),
	}
	if runtime.GOOS == "windows" { // ordinary file names on Unix
		keys = append(keys, `\abs\x.parquet`, "C:foo", "NUL", "receptions/NUL")
	}
	for _, key := range keys {
		if err := s.Put(context.Background(), key, []byte("x")); err == nil {
			t.Errorf("Put(%q) succeeded, want error", key)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape.parquet")); err == nil {
		t.Error("file written outside root")
	}
}
