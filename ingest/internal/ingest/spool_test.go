package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// switchStore is object storage that a test can take down and bring back.
type switchStore struct {
	mu       sync.Mutex
	down     bool
	files    map[string][]byte
	order    []string       // successful puts, in order
	puts     map[string]int // successful puts per key
	attempts int
}

func (m *switchStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	if m.down {
		return errors.New("storage unreachable")
	}
	if m.files == nil {
		m.files, m.puts = map[string][]byte{}, map[string]int{}
	}
	m.files[key] = append([]byte(nil), data...)
	m.order = append(m.order, key)
	m.puts[key]++
	return nil
}

func (m *switchStore) setDown(d bool) { m.mu.Lock(); m.down = d; m.mu.Unlock() }

// noSleep records requested sleeps without waiting.
type noSleep struct {
	mu    sync.Mutex
	slept []time.Duration
}

func (n *noSleep) sleep(ctx context.Context, d time.Duration) error {
	n.mu.Lock()
	n.slept = append(n.slept, d)
	n.mu.Unlock()
	return ctx.Err()
}

func spoolKey(i int) string {
	return fmt.Sprintf("receptions/region=SEA/date=2026-11-01/hour=%02d/part-%04d.parquet", i%24, i)
}

func openTestSpool(t *testing.T, up Store, dir string, maxBytes, rate int64) (*Spool, *noSleep) {
	t.Helper()
	s, err := OpenSpool(up, dir, maxBytes, rate)
	if err != nil {
		t.Fatal(err)
	}
	ns := &noSleep{}
	s.sleep = ns.sleep
	return s, ns
}

// drainUntilEmpty runs Drain until the spool is empty, then stops it.
func drainUntilEmpty(t *testing.T, s *Spool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Drain(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.Stats().Files > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("spool not drained: %+v", s.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

// spoolDirNames lists what is on disk in the spool directory.
func spoolDirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// Outage: the Writer's flush succeeds, its memory is freed, the files are on
// disk. Recovery: drained newest first, each key stored exactly once.
func TestSpoolOutageThenRecovery(t *testing.T) {
	store := &switchStore{down: true}
	dir := t.TempDir()
	sp, _ := openTestSpool(t, store, dir, 1<<30, 0)

	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	w := newWriter(sp, &clock)
	for _, ts := range []string{"2026-11-01T09:00:00+00:00", "2026-11-01T10:00:00+00:00", "2026-11-01T11:00:00+00:00"} {
		w.Add(rowAt(t, ts))
	}
	st, err := w.Flush(context.Background(), true)
	if err != nil || st.Files != 3 {
		t.Fatalf("flush during outage = %+v, %v; want 3 files, nil", st, err)
	}
	if w.Buffered() != 0 {
		t.Fatalf("Buffered = %d, want 0 (rows freed once spooled)", w.Buffered())
	}
	if sp.StorageUp() {
		t.Error("StorageUp = true during outage")
	}
	if got := sp.Stats(); got.Files != 3 || got.Writes != 3 || got.OldestAge <= 0 {
		t.Fatalf("stats = %+v, want 3 files, 3 writes, age > 0", got)
	}
	if len(store.files) != 0 {
		t.Fatalf("store has %d files during outage", len(store.files))
	}

	// Spool more files in a known order, then recover.
	for i := range 3 {
		if err := sp.Put(context.Background(), spoolKey(i), []byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	store.setDown(false)
	drainUntilEmpty(t, sp)

	want := []string{spoolKey(2), spoolKey(1), spoolKey(0)} // newest first
	if !slices.Equal(store.order[:3], want) {
		t.Errorf("drain order = %v, want newest first %v", store.order[:3], want)
	}
	if len(store.puts) != 6 {
		t.Errorf("store has %d keys, want 6", len(store.puts))
	}
	for k, n := range store.puts {
		if n != 1 {
			t.Errorf("%s stored %d times, want 1", k, n)
		}
	}
	if names := spoolDirNames(t, dir); len(names) != 0 {
		t.Errorf("spool dir not empty after drain: %v", names)
	}
	if got := sp.Stats(); got.Files != 0 || got.Bytes != 0 || got.Uploads != 6 || got.OldestAge != 0 || !sp.StorageUp() {
		t.Errorf("stats after drain = %+v up=%v", got, sp.StorageUp())
	}
}

// A restart mid-outage loses nothing that reached the spool; a temp file from
// a crash mid-write is deleted and never uploaded.
func TestSpoolRestartResumesAndDropsPartialFiles(t *testing.T) {
	store := &switchStore{down: true}
	dir := t.TempDir()
	first, _ := openTestSpool(t, store, dir, 1<<30, 0)
	for i := range 2 {
		if err := first.Put(context.Background(), spoolKey(i), []byte("rows")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, tmpPrefix+"123456"), []byte("half a parq"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, _ := openTestSpool(t, store, dir, 1<<30, 0) // the "restarted" process
	if got := second.Stats(); got.Files != 2 || got.Bytes != 8 {
		t.Fatalf("after restart stats = %+v, want 2 files, 8 bytes", got)
	}
	for _, n := range spoolDirNames(t, dir) {
		if strings.HasPrefix(n, ".") {
			t.Errorf("temp file %s survived restart", n)
		}
	}
	store.setDown(false)
	drainUntilEmpty(t, second)
	if len(store.files) != 2 || store.files[spoolKey(0)] == nil || store.files[spoolKey(1)] == nil {
		t.Errorf("store after resume = %v keys, want exactly the 2 spooled", len(store.files))
	}
}

func TestSpoolEvictsOldestAtCap(t *testing.T) {
	tests := []struct {
		name             string
		max              int64
		sizes            []int // one Put per entry, keys spoolKey(0..)
		wantKept         []int
		wantEvicted      int64
		wantEvictedBytes int64
		wantErrAt        int // index of the Put that must fail, -1 none
	}{
		{"fits", 12, []int{4, 4, 4}, []int{0, 1, 2}, 0, 0, -1},
		{"one over", 10, []int{4, 4, 4}, []int{1, 2}, 1, 4, -1},
		{"big file evicts two", 10, []int{3, 3, 3, 8}, []int{3}, 3, 9, -1},
		{"larger than the cap", 10, []int{4, 11}, []int{0}, 0, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sp, _ := openTestSpool(t, &switchStore{down: true}, dir, tc.max, 0)
			for i, n := range tc.sizes {
				err := sp.Put(context.Background(), spoolKey(i), make([]byte, n))
				if (err != nil) != (i == tc.wantErrAt) {
					t.Fatalf("Put %d err = %v, want error: %v", i, err, i == tc.wantErrAt)
				}
			}
			st := sp.Stats()
			if st.EvictedFiles != tc.wantEvicted || st.EvictedBytes != tc.wantEvictedBytes {
				t.Errorf("evicted %d files / %d bytes, want %d / %d", st.EvictedFiles, st.EvictedBytes, tc.wantEvicted, tc.wantEvictedBytes)
			}
			if st.Bytes > tc.max {
				t.Errorf("spool holds %d bytes, cap %d", st.Bytes, tc.max)
			}
			kept := spoolDirNames(t, dir)
			var want []string
			for _, i := range tc.wantKept {
				want = append(want, filepath.Base(sp.path(spoolKey(i))))
			}
			slices.Sort(kept)
			slices.Sort(want)
			if !slices.Equal(kept, want) || st.Files != len(want) {
				t.Errorf("kept %v (index %d), want %v", kept, st.Files, want)
			}
		})
	}
}

// Drain time is bytes/rate: 3 files x 500 B at 1000 B/s = 1.5 s of throttle.
func TestSpoolDrainRateLimit(t *testing.T) {
	store := &switchStore{down: true}
	sp, ns := openTestSpool(t, store, t.TempDir(), 1<<30, 1000)
	for i := range 3 {
		if err := sp.Put(context.Background(), spoolKey(i), make([]byte, 500)); err != nil {
			t.Fatal(err)
		}
	}
	store.setDown(false)
	drainUntilEmpty(t, sp)
	ns.mu.Lock()
	defer ns.mu.Unlock()
	var total time.Duration
	for _, d := range ns.slept {
		total += d
	}
	if total != 1500*time.Millisecond {
		t.Errorf("throttle sleeps = %v (total %v), want 1.5s", ns.slept, total)
	}
}

// While storage is down the drainer waits 1 s doubling to 30 s between
// attempts: one upload attempt per sleep, never a hot loop.
func TestSpoolBackoffWhileDown(t *testing.T) {
	store := &switchStore{down: true}
	sp, _ := openTestSpool(t, store, t.TempDir(), 1<<30, 0)
	if err := sp.Put(context.Background(), spoolKey(0), []byte("x")); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.attempts = 0
	store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var slept []time.Duration
	sp.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		if len(slept) == 8 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	sp.Drain(ctx) // returns once the sleep cancels ctx

	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(slept, want) {
		t.Errorf("backoff = %v, want %v", slept, want)
	}
	if store.attempts != 8 {
		t.Errorf("upload attempts = %d, want 8 (one per backoff)", store.attempts)
	}
}

// After the first failure, Puts go straight to disk without calling storage.
func TestSpoolSkipsStorageOnceDown(t *testing.T) {
	store := &switchStore{down: true}
	sp, _ := openTestSpool(t, store, t.TempDir(), 1<<30, 0)
	for i := range 5 {
		if err := sp.Put(context.Background(), spoolKey(i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if store.attempts != 1 {
		t.Errorf("storage attempts = %d, want 1", store.attempts)
	}
}

func TestSpoolRejectsUnsafeKeys(t *testing.T) {
	sp, _ := openTestSpool(t, &switchStore{down: true}, t.TempDir(), 1<<30, 0)
	sp.up.Store(false)
	for _, key := range []string{"", ".hidden", ".tmp-1"} {
		if err := sp.Put(context.Background(), key, []byte("x")); err == nil {
			t.Errorf("Put(%q) succeeded, want error", key)
		}
	}
}

// BenchmarkSpoolWrite / BenchmarkSpoolDrain measure fsync'd spool writes and
// the drain into an in-memory store, 1 MiB files (a typical flushed partition).
//
//	go test -run=^$ -bench=Spool -benchtime=64x ./internal/ingest
func BenchmarkSpoolWrite(b *testing.B) {
	sp, err := OpenSpool(&switchStore{down: true}, b.TempDir(), 1<<40, 0)
	if err != nil {
		b.Fatal(err)
	}
	sp.up.Store(false)
	data := make([]byte, 1<<20)
	b.SetBytes(int64(len(data)))
	for i := 0; b.Loop(); i++ {
		if err := sp.Put(context.Background(), spoolKey(i), data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpoolDrain(b *testing.B) {
	store := &switchStore{down: true}
	sp, err := OpenSpool(store, b.TempDir(), 1<<40, 0)
	if err != nil {
		b.Fatal(err)
	}
	sp.up.Store(false)
	data := make([]byte, 1<<20)
	for i := range b.N {
		if err := sp.Put(context.Background(), spoolKey(i), data); err != nil {
			b.Fatal(err)
		}
	}
	store.setDown(false)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sp.Drain(ctx); close(done) }()
	for sp.Stats().Files > 0 {
		time.Sleep(100 * time.Microsecond)
	}
	b.StopTimer()
	cancel()
	<-done
}

// If the spool cannot take a file while storage is down (directory gone, file
// larger than the cap) and the index is empty, the drainer has nothing to
// probe with; the next Put must still try storage, so the rows get out once
// it recovers instead of piling up in memory.
func TestSpoolRecoversWhenSpoolWriteFails(t *testing.T) {
	tests := []struct {
		name     string
		maxBytes int64
		size     int
		breakDir bool
	}{
		{"spool directory removed", 1 << 30, 1, true},
		{"file larger than the cap", 10, 11, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &switchStore{down: true}
			dir := t.TempDir()
			sp, _ := openTestSpool(t, store, dir, tc.maxBytes, 0)
			if tc.breakDir {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			data := make([]byte, tc.size)
			if err := sp.Put(context.Background(), spoolKey(0), data); err == nil {
				t.Fatal("Put with storage down and spool unusable succeeded, want error (rows stay in the Writer)")
			}
			if err := sp.Put(context.Background(), spoolKey(0), data); err == nil {
				t.Fatal("retry while still down succeeded, want error")
			}
			store.setDown(false)
			if err := sp.Put(context.Background(), spoolKey(0), data); err != nil { // the Writer's next retry
				t.Fatalf("Put after recovery = %v, want nil", err)
			}
			if store.puts[spoolKey(0)] != 1 || !sp.StorageUp() {
				t.Errorf("after recovery: stored %d times, up=%v; want 1, true", store.puts[spoolKey(0)], sp.StorageUp())
			}
		})
	}
}

// Writers, the drainer and Stats at once, with eviction and flapping storage:
// never over the cap, nothing is both missing from storage and not evicted.
func TestSpoolConcurrentFlapping(t *testing.T) {
	store := &switchStore{down: true}
	sp, _ := openTestSpool(t, store, t.TempDir(), 40, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sp.Drain(ctx); close(done) }()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				if err := sp.Put(context.Background(), spoolKey(g*1000+i), make([]byte, 7)); err != nil {
					t.Error(err)
				}
				if st := sp.Stats(); st.Bytes > 40 {
					t.Errorf("spool holds %d bytes, cap 40", st.Bytes)
				}
				if i%50 == 0 {
					store.setDown(i%100 == 0)
				}
			}
		}()
	}
	wg.Wait()
	store.setDown(false)
	deadline := time.Now().Add(5 * time.Second)
	for sp.Stats().Files > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	st := sp.Stats()
	store.mu.Lock()
	defer store.mu.Unlock()
	if st.Files != 0 || st.Bytes != 0 {
		t.Errorf("spool not drained: %+v", st)
	}
	// Evicted files may also have been uploaded (in-flight race), so >= not ==.
	if got := int64(len(store.files)) + st.EvictedFiles; got < 800 {
		t.Errorf("stored %d + evicted %d = %d, want >= 800 (a file vanished)", len(store.files), st.EvictedFiles, got)
	}
}

// Names the spool would not have written are ignored at startup: "a+b"
// (canonical for key "a b") and "a%20b" both decode to "a b" and would
// double-count one key; "%zz" does not decode at all.
func TestSpoolIgnoresForeignNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a+b", "a%20b", "%zz"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sp, _ := openTestSpool(t, &switchStore{down: true}, dir, 1<<30, 0)
	if st := sp.Stats(); st.Files != 1 || st.Bytes != 5 || st.MaxBytes != 1<<30 {
		t.Errorf("stats = %+v, want only a+b (1 file, 5 bytes) and MaxBytes 1 GiB", st)
	}
}
