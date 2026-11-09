package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// Store is where finished Parquet files go (MinIO in production, a map in tests).
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
}

// row is what the writer needs from both tables.
type row interface {
	id() [16]byte
	time() time.Time
}

func (r Reception) id() [16]byte    { return r.ReceptionID }
func (r Reception) time() time.Time { return r.RxTime }
func (a Advert) id() [16]byte       { return a.ReceptionID }
func (a Advert) time() time.Time    { return a.RxTime }

type partKey struct{ region, date, hour string }

type buffer[T row] struct {
	rows    []T
	seen    map[[16]byte]bool // dedup MQTT redeliveries within this buffer
	firstAt time.Time         // wall-clock time the first row arrived
}

// Writer buffers rows per partition and writes one Parquet file per partition
// when it reaches FlushRows or has been open for FlushAge. Not safe for
// concurrent use: the ingester calls it from a single goroutine.
type Writer struct {
	Store     Store
	FlushRows int           // e.g. 100k rows: big enough for good compression, small enough for memory
	FlushAge  time.Duration // e.g. 5 min: bounds how stale the data in storage can be
	Now       func() time.Time

	receptions map[partKey]*buffer[Reception]
	adverts    map[partKey]*buffer[Advert]
}

// Add buffers the rows from one converted message. Returns false if the
// reception was a duplicate already buffered.
func (w *Writer) Add(res Result) bool {
	if w.receptions == nil {
		w.receptions, w.adverts = map[partKey]*buffer[Reception]{}, map[partKey]*buffer[Advert]{}
	}
	if !add(w.receptions, res.Reception, w.Now()) {
		return false
	}
	if res.Advert != nil {
		add(w.adverts, *res.Advert, w.Now())
	}
	return true
}

func add[T row](m map[partKey]*buffer[T], r T, now time.Time) bool {
	t := r.time().UTC()
	k := partKey{regionOf(r), t.Format("2006-01-02"), fmt.Sprintf("%02d", t.Hour())}
	b := m[k]
	if b == nil {
		b = &buffer[T]{seen: map[[16]byte]bool{}, firstAt: now}
		m[k] = b
	}
	if b.seen[r.id()] {
		return false
	}
	b.seen[r.id()] = true
	b.rows = append(b.rows, r)
	return true
}

func regionOf(r row) string {
	switch v := r.(type) {
	case Reception:
		return v.Region
	case Advert:
		return v.Region
	}
	return "UNKNOWN"
}

// Buffered is the number of rows not yet written (both tables).
func (w *Writer) Buffered() int {
	n := 0
	for _, b := range w.receptions {
		n += len(b.rows)
	}
	for _, b := range w.adverts {
		n += len(b.rows)
	}
	return n
}

// FlushStats reports what one Flush wrote.
type FlushStats struct {
	Files, Rows, Bytes int
}

// Flush writes partitions that are full or old enough; force writes all
// (used on shutdown). A partition whose upload fails stays buffered and is
// retried next time, so a storage outage delays data instead of losing it.
func (w *Writer) Flush(ctx context.Context, force bool) (FlushStats, error) {
	var st FlushStats
	err := errors.Join(
		flushTable(ctx, w, "receptions", w.receptions, force, &st),
		flushTable(ctx, w, "adverts", w.adverts, force, &st),
	)
	return st, err
}

func flushTable[T row](ctx context.Context, w *Writer, table string, m map[partKey]*buffer[T], force bool, st *FlushStats) error {
	var errs []error
	for k, b := range m {
		if !force && len(b.rows) < w.FlushRows && w.Now().Sub(b.firstAt) < w.FlushAge {
			continue
		}
		key, data, err := encode(table, k, b.rows)
		if err != nil {
			errs = append(errs, fmt.Errorf("encode %s: %w", table, err))
			continue
		}
		if err := w.Store.Put(ctx, key, data); err != nil {
			errs = append(errs, fmt.Errorf("put %s: %w", key, err))
			continue
		}
		st.Files++
		st.Rows += len(b.rows)
		st.Bytes += len(data)
		delete(m, k)
	}
	return errors.Join(errs...)
}

// encode sorts rows by time (tight min/max statistics, so time-range queries
// skip row groups) and names the file after its content: re-uploading the
// same rows writes the same key, which makes retries idempotent.
func encode[T row](table string, k partKey, rows []T) (string, []byte, error) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].time().Before(rows[j].time()) })
	h := sha256.New()
	for _, r := range rows {
		id := r.id()
		h.Write(id[:])
	}
	var buf bytes.Buffer
	err := parquet.Write(&buf, rows,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.KeyValueMetadata("meshtwin.schema_version", strconv.Itoa(SchemaVersion)),
	)
	if err != nil {
		return "", nil, err
	}
	key := fmt.Sprintf("%s/region=%s/date=%s/hour=%s/part-%x.parquet", table, k.region, k.date, k.hour, h.Sum(nil)[:12])
	return key, buf.Bytes(), nil
}

// rowGroupRows caps a row group at ~64k rows (~3-5 MB compressed here): small
// enough that readers can skip by time, large enough for efficient scans.
const rowGroupRows = 64 * 1024
