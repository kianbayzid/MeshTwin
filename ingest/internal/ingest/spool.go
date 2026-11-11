package ingest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Spool is the DDIL buffer: a Store that sits in front of object storage.
// Put tries storage first; if storage is down it writes the same key and bytes
// to a local directory and returns nil, so the Writer frees the rows from
// memory. Drain uploads spooled files newest first once storage answers again.
//
// Keys are content-addressed (see encode), so uploading a file twice (after a
// crash between upload and delete) writes the same object: harmless.
//
// The directory is flat: each file is named url.QueryEscape(key), so there is
// one directory to fsync and one ReadDir to rebuild the index on restart.
type Spool struct {
	upstream  Store
	dir       string
	maxBytes  int64
	rateBytes int64         // drain limit in bytes/s for limited links; 0 = unlimited
	timeout   time.Duration // per upstream call, so a black-holed endpoint cannot stall the Writer
	sleep     func(context.Context, time.Duration) error

	up   atomic.Bool   // last upstream call succeeded
	wake chan struct{} // nudges an idle drainer after a spool write

	mu                         sync.Mutex
	files                      map[string]spoolFile // by object key
	bytes                      int64
	seq                        uint64 // write order; newest = highest
	writes, uploads            int64
	evictedFiles, evictedBytes int64
}

type spoolFile struct {
	size int64
	at   time.Time
	seq  uint64
}

// SpoolStats is a snapshot for metrics.
type SpoolStats struct {
	Files                                       int
	Bytes, MaxBytes                             int64
	OldestAge                                   time.Duration // 0 when empty
	Writes, Uploads, EvictedFiles, EvictedBytes int64
}

const (
	drainMinBackoff = time.Second
	drainMaxBackoff = 30 * time.Second // short cap: recovery is noticed within 30 s
)

// OpenSpool opens (or creates) dir, deletes temp files left by a crash
// mid-write (never uploaded: they may be partial), indexes the finished files
// a previous run left behind, and checks the directory is writable.
func OpenSpool(upstream Store, dir string, maxBytes, rateBytes int64) (*Spool, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("spool: max bytes must be > 0, got %d", maxBytes)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	type found struct {
		key string
		f   spoolFile
	}
	var old []found
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return nil, fmt.Errorf("spool: remove partial file: %w", err)
			}
			slog.Warn("spool: removed partial file from an interrupted write", "file", name)
			continue
		}
		key, err := url.QueryUnescape(name)
		// Only names we would have written: "a+b" and "a%20b" both decode to
		// "a b", which would double-count bytes under one key.
		if e.IsDir() || err != nil || url.QueryEscape(key) != name {
			slog.Warn("spool: ignoring unexpected entry", "name", name)
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("spool: %w", err)
		}
		old = append(old, found{key, spoolFile{size: info.Size(), at: info.ModTime()}})
	}
	// Restore write order from modification times (name breaks ties).
	slices.SortFunc(old, func(a, b found) int {
		return cmp.Or(a.f.at.Compare(b.f.at), strings.Compare(a.key, b.key))
	})
	s := &Spool{
		upstream: upstream, dir: dir, maxBytes: maxBytes, rateBytes: rateBytes,
		timeout: 30 * time.Second, sleep: sleepCtx,
		wake: make(chan struct{}, 1), files: map[string]spoolFile{},
	}
	for _, o := range old {
		s.seq++
		o.f.seq = s.seq
		s.files[o.key] = o.f
		s.bytes += o.f.size
	}
	s.up.Store(true) // optimistic until a call fails: the first Put tries storage
	probe, err := os.CreateTemp(dir, tmpPrefix+"probe-*")
	if err != nil {
		return nil, fmt.Errorf("spool: directory not writable: %w", err)
	}
	probe.Close()
	os.Remove(probe.Name())
	if len(old) > 0 {
		slog.Info("spool: resuming files from a previous run", "files", len(old), "bytes", s.bytes)
	}
	return s, nil
}

// Put uploads directly while storage is up. Once a call has failed, Puts go
// straight to disk and only the drainer probes storage (with backoff), so an
// outage costs one timeout, not one per flush.
//
// If the spool cannot take the file either (directory gone, disk full, file
// larger than the cap), storage is tried anyway: the drainer only probes
// storage when the spool has files, so without this a broken spool with an
// empty index would never notice storage coming back.
func (s *Spool) Put(ctx context.Context, key string, data []byte) error {
	tried := s.up.Load()
	if tried {
		err := s.upload(ctx, key, data)
		if err == nil {
			return nil
		}
		slog.Warn("object storage put failed; spooling to disk", "key", key, "err", err)
	}
	werr := s.write(key, data)
	if werr == nil || tried {
		return werr
	}
	if err := s.upload(ctx, key, data); err != nil {
		return errors.Join(werr, err)
	}
	return nil
}

// StorageUp reports whether the last call to object storage succeeded. It
// only changes on a storage call, so on a quiet link (no flush, empty spool)
// it can lag reality by up to one flush age.
func (s *Spool) StorageUp() bool { return s.up.Load() }

func (s *Spool) upload(ctx context.Context, key string, data []byte) error {
	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	err := s.upstream.Put(cctx, key, data)
	if ctx.Err() == nil { // our own shutdown says nothing about storage health
		s.up.Store(err == nil)
	}
	return err
}

func (s *Spool) path(key string) string { return filepath.Join(s.dir, url.QueryEscape(key)) }

// write stores data under key, evicting the oldest files first if it would
// push the spool over maxBytes: for a live twin the newest data is worth most
// (the hourly topology job reads the recent window), and the oldest data is
// the most likely to be superseded. It never blocks the Writer and never
// grows past maxBytes.
func (s *Spool) write(key string, data []byte) error {
	name := url.QueryEscape(key)
	if strings.HasPrefix(name, ".") || !filepath.IsLocal(name) {
		return fmt.Errorf("spool: unsafe key %q", key)
	}
	n := int64(len(data))
	if n > s.maxBytes {
		return fmt.Errorf("spool: %s is %d bytes, more than the whole spool (%d)", key, n, s.maxBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[key]; ok {
		return nil // same key = same content, already spooled
	}
	for s.bytes+n > s.maxBytes {
		k, f, ok := s.pick(false)
		if !ok {
			break // index empty; cannot happen while bytes is accurate and n <= maxBytes
		}
		if err := os.Remove(s.path(k)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("spool: evict %s: %w", k, err)
		}
		s.forget(k)
		// If the drainer is uploading this very file, it may still arrive, so
		// evicted_* can over-count real loss in that rare race.
		s.evictedFiles++
		s.evictedBytes += f.size
		slog.Warn("spool full: evicted oldest file (data lost)", "key", k, "bytes", f.size, "age", time.Since(f.at).Round(time.Second))
	}
	if err := writeFileAtomic(s.dir, name, data); err != nil {
		return fmt.Errorf("spool: %s: %w", key, err)
	}
	s.seq++
	s.files[key] = spoolFile{size: n, at: time.Now(), seq: s.seq}
	s.bytes += n
	s.writes++
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// pick returns the newest (or oldest) spooled file. Caller holds mu.
// shortcut: linear scan, fine for thousands of files; use a heap if spools hold ~100k.
func (s *Spool) pick(newest bool) (string, spoolFile, bool) {
	var bestKey string
	var best spoolFile
	found := false
	for k, f := range s.files {
		if !found || (newest && f.seq > best.seq) || (!newest && f.seq < best.seq) {
			bestKey, best, found = k, f, true
		}
	}
	return bestKey, best, found
}

// forget drops key from the index. Caller holds mu.
func (s *Spool) forget(key string) {
	if f, ok := s.files[key]; ok {
		s.bytes -= f.size
		delete(s.files, key)
	}
}

// Drain uploads spooled files until ctx is done: newest first, deleting each
// after a successful upload, sleeping len/rate after each file when a rate
// limit is set, and backing off 1 s doubling to 30 s while storage is down.
// It picks up files left by a previous run because OpenSpool indexed them.
func (s *Spool) Drain(ctx context.Context) {
	backoff := drainMinBackoff
	for ctx.Err() == nil {
		s.mu.Lock()
		key, _, ok := s.pick(true)
		s.mu.Unlock()
		if !ok {
			select {
			case <-s.wake:
			case <-ctx.Done():
			}
			continue
		}
		data, err := os.ReadFile(s.path(key))
		if err != nil {
			// Evicted meanwhile (then it is already out of the index) or
			// unreadable; drop it from the index so we never spin on it.
			s.mu.Lock()
			if _, still := s.files[key]; still {
				slog.Error("spool: unreadable file dropped", "key", key, "err", err)
				s.forget(key)
			}
			s.mu.Unlock()
			continue
		}
		if err := s.upload(ctx, key, data); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("spool drain: object storage still down", "retry_in", backoff, "err", err)
			if s.sleep(ctx, backoff) != nil {
				return
			}
			backoff = min(backoff*2, drainMaxBackoff)
			continue
		}
		backoff = drainMinBackoff
		s.mu.Lock()
		if _, still := s.files[key]; still {
			// A failed remove only means a harmless idempotent re-upload after restart.
			if err := os.Remove(s.path(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("spool: remove uploaded file", "key", key, "err", err)
			}
			s.forget(key)
		}
		s.uploads++
		s.mu.Unlock()
		if s.rateBytes > 0 {
			// Upload time is not subtracted, so the real rate stays a little under the limit.
			if s.sleep(ctx, time.Duration(float64(len(data))/float64(s.rateBytes)*float64(time.Second))) != nil {
				return
			}
		}
	}
}

// Stats is a snapshot for the metrics endpoint.
func (s *Spool) Stats() SpoolStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := SpoolStats{
		Files: len(s.files), Bytes: s.bytes, MaxBytes: s.maxBytes,
		Writes: s.writes, Uploads: s.uploads, EvictedFiles: s.evictedFiles, EvictedBytes: s.evictedBytes,
	}
	if _, f, ok := s.pick(false); ok {
		st.OldestAge = time.Since(f.at)
	}
	return st
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
