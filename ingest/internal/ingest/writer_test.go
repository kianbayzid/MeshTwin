package ingest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

type memStore struct {
	files map[string][]byte
	fail  bool
}

func (m *memStore) Put(_ context.Context, key string, data []byte) error {
	if m.fail {
		return errors.New("storage unreachable")
	}
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[key] = append([]byte(nil), data...)
	return nil
}

func rowAt(t *testing.T, ts string) Result {
	t.Helper()
	res, err := Convert(msgRecord(t, topic, map[string]any{"timestamp": ts}))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func newWriter(store Store, clock *time.Time) *Writer {
	return &Writer{Store: store, FlushRows: 3, FlushAge: time.Minute, Now: func() time.Time { return *clock }}
}

func TestWriterPartitionsAndRoundTrips(t *testing.T) {
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := &memStore{}
	w := newWriter(store, &clock)
	w.Add(rowAt(t, "2026-11-01T10:59:59+00:00"))
	w.Add(rowAt(t, "2026-11-01T10:00:01+00:00"))
	w.Add(rowAt(t, "2026-11-01T11:00:00+00:00"))

	st, err := w.Flush(context.Background(), true)
	if err != nil || st.Files != 2 || st.Rows != 3 {
		t.Fatalf("flush = %+v, %v; want 2 files, 3 rows", st, err)
	}
	var hour10 []byte
	for k, v := range store.files {
		if !strings.HasPrefix(k, "receptions/region=SEA/date=2026-11-01/hour=") || !strings.HasSuffix(k, ".parquet") {
			t.Errorf("unexpected key %s", k)
		}
		if strings.Contains(k, "hour=10/") {
			hour10 = v
		}
	}
	rows, err := parquet.Read[Reception](bytes.NewReader(hour10), int64(len(hour10)))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].RxTime.Before(rows[1].RxTime) {
		t.Fatalf("hour 10 rows not sorted by time: %v", rows)
	}
	if rows[0].PathHops[1] != "B2" || *rows[0].SNRDB != 4.25 || rows[0].SrcID != nil {
		t.Errorf("round trip lost data: %+v", rows[0])
	}
	if w.Buffered() != 0 {
		t.Errorf("buffered = %d after flush", w.Buffered())
	}
}

func TestWriterFlushTriggers(t *testing.T) {
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := &memStore{}
	w := newWriter(store, &clock)
	w.Add(rowAt(t, "2026-11-01T10:00:01+00:00"))
	if st, _ := w.Flush(context.Background(), false); st.Files != 0 {
		t.Fatal("flushed a small, young partition")
	}
	clock = clock.Add(2 * time.Minute) // older than FlushAge
	if st, _ := w.Flush(context.Background(), false); st.Files != 1 {
		t.Fatal("did not flush an aged partition")
	}
	for i := range 3 { // FlushRows reached
		w.Add(rowAt(t, "2026-11-01T10:00:0"+string(rune('2'+i))+"+00:00"))
	}
	if st, _ := w.Flush(context.Background(), false); st.Files != 1 {
		t.Fatal("did not flush a full partition")
	}
}

func TestWriterDedupAndRetry(t *testing.T) {
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := &memStore{fail: true}
	w := newWriter(store, &clock)
	if !w.Add(rowAt(t, "2026-11-01T10:00:01+00:00")) || w.Add(rowAt(t, "2026-11-01T10:00:01+00:00")) {
		t.Fatal("MQTT redelivery was not deduplicated")
	}
	if _, err := w.Flush(context.Background(), true); err == nil || w.Buffered() != 1 {
		t.Fatalf("failed upload must keep rows: err=%v buffered=%d", err, w.Buffered())
	}
	store.fail = false
	if st, err := w.Flush(context.Background(), true); err != nil || st.Rows != 1 {
		t.Fatalf("retry = %+v, %v", st, err)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	// Same rows => same object key, so a retried upload overwrites instead of duplicating.
	rows := []Reception{rowAt(t, "2026-11-01T10:00:01+00:00").Reception}
	k1, d1, _ := encode("receptions", partKey{"SEA", "2026-11-01", "10"}, rows)
	k2, d2, _ := encode("receptions", partKey{"SEA", "2026-11-01", "10"}, rows)
	if k1 != k2 || !bytes.Equal(d1, d2) {
		t.Error("encoding the same rows twice differs")
	}
}

func TestWriterAdvertTable(t *testing.T) {
	clock := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := &memStore{}
	w := newWriter(store, &clock)
	res, err := Convert(msgRecord(t, topic, map[string]any{"raw": advertHex}))
	if err != nil || res.Advert == nil {
		t.Fatalf("advert not parsed: %v", err)
	}
	if res.Advert.Name == nil || *res.Advert.Name != "SEA-CMP-02" || res.Advert.Lat == nil || *res.Advert.Lat != 47.541904 {
		t.Errorf("advert row = %+v", res.Advert)
	}
	w.Add(res)
	if _, err := w.Flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	adverts := 0
	for k := range store.files {
		if strings.HasPrefix(k, "adverts/") {
			adverts++
		}
	}
	if adverts != 1 {
		t.Errorf("advert files = %d, want 1", adverts)
	}
}

// From docs/protocol.md, worked example.
const advertHex = "110235340562C16BA4EFC9875477FAAEE55CD36645214BAFD526A0C77BED905022F920613981E66AE7F2512B04DCAC4A7BF718F07A579E1A417F6610C716D7911CF20FA579E6E9E570A35ED2C14F8828CDD452D78215870C31679681EADFE518DD057A25154723ED91906ED502B2B4B8F85345412D434D502D3032"
