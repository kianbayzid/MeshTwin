package ingest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"meshtwin/ingest/internal/replay"
)

const obsKey = "351FC1F9E2D52044496CA2CD51999F0CC41561F41DB70B3D561838B4E4260FED"

func msgRecord(t *testing.T, topic string, fields map[string]any) replay.Record {
	t.Helper()
	base := map[string]any{"type": "PACKET", "direction": "rx", "origin_id": obsKey,
		"timestamp": "2026-11-01T10:00:00.123456+00:00", "SNR": "4.25", "RSSI": "-93",
		"raw": "1502A1B2112233" + strings.Repeat("44", 16)}
	maps.Copy(base, fields)
	b, _ := json.Marshal(base)
	return replay.Record{RecvTime: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), Topic: topic, Payload: b}
}

var topic = "meshcore/sea/" + strings.ToLower(obsKey) + "/packets"

func TestConvertBasic(t *testing.T) {
	res, err := Convert(msgRecord(t, topic, nil))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Reception
	if r.Region != "SEA" || r.ObserverID != obsKey {
		t.Errorf("region/observer = %s/%s, want upper-cased SEA/%s", r.Region, r.ObserverID, obsKey)
	}
	if want := time.Date(2026, 11, 1, 10, 0, 0, 123456000, time.UTC); !r.RxTime.Equal(want) {
		t.Errorf("rx_time = %v, want observer timestamp %v", r.RxTime, want)
	}
	if strings.Join(r.PathHops, ",") != "A1,B2" || r.HopCount != 2 || r.PayloadType != 5 {
		t.Errorf("path/type = %v/%d/%d", r.PathHops, r.HopCount, r.PayloadType)
	}
	if r.SNRDB == nil || *r.SNRDB != 4.25 || r.RSSIDBm == nil || *r.RSSIDBm != -93 {
		t.Errorf("snr/rssi = %v/%v", r.SNRDB, r.RSSIDBm)
	}
	if r.SrcID != nil || res.Advert != nil {
		t.Error("group message must have no src_id and no advert row")
	}
}

func TestConvertQuirks(t *testing.T) {
	// meshcoretomqtt omits the UTC offset: fall back to receive time rather than guess a timezone.
	res, err := Convert(msgRecord(t, topic, map[string]any{"timestamp": "2026-11-01T10:00:00.123456"}))
	if err != nil || !res.Reception.RxTime.Equal(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("offset-less timestamp: rx_time = %v, err %v; want receive time", res.Reception.RxTime, err)
	}
	// Missing / junk SNR and RSSI become nulls, not zeros; float RSSI is tolerated.
	res, _ = Convert(msgRecord(t, topic, map[string]any{"SNR": "", "RSSI": " -93.0 "}))
	if res.Reception.SNRDB != nil || res.Reception.RSSIDBm == nil || *res.Reception.RSSIDBm != -93 {
		t.Errorf("snr/rssi = %v/%v", res.Reception.SNRDB, res.Reception.RSSIDBm)
	}
	// Both "path" shapes parse (and are ignored).
	for _, p := range []any{"C2 -> E2", []string{"c2", "e2"}} {
		if _, err := Convert(msgRecord(t, topic, map[string]any{"path": p})); err != nil {
			t.Errorf("path %v: %v", p, err)
		}
	}
	// The observer's hash field is checked against ours.
	res, _ = Convert(msgRecord(t, topic, map[string]any{"hash": "0000000000000000"}))
	if !res.HashMismatch {
		t.Error("wrong hash field not flagged")
	}
}

func TestConvertRejects(t *testing.T) {
	cases := []struct {
		name  string
		topic string
		f     map[string]any
		want  error
	}{
		{"status topic", "meshcore/SEA/" + obsKey + "/status", nil, ErrBadTopic},
		{"short key", "meshcore/SEA/ABCD/packets", nil, ErrBadTopic},
		{"region with colon", "meshcore/X:Y/" + obsKey + "/packets", nil, ErrBadTopic},
		{"region dot-dot", "meshcore/../" + obsKey + "/packets", nil, ErrBadTopic},
		{"region punctuation", "meshcore/sea!/" + obsKey + "/packets", nil, ErrBadTopic},
		{"region empty", "meshcore//" + obsKey + "/packets", nil, ErrBadTopic},
		{"region one char", "meshcore/S/" + obsKey + "/packets", nil, ErrBadTopic},
		{"region non-ASCII", "meshcore/SÉA/" + obsKey + "/packets", nil, ErrBadTopic},
		{"region long s", "meshcore/ſea/" + obsKey + "/packets", nil, ErrBadTopic}, // U+017F upper-cases to ASCII S
		{"region too long", "meshcore/ABCDEFGHI/" + obsKey + "/packets", nil, ErrBadTopic},
		{"tx direction", topic, map[string]any{"direction": "tx"}, ErrNotPacket},
		{"status type", topic, map[string]any{"type": "STATUS"}, ErrNotPacket},
		{"raw not hex", topic, map[string]any{"raw": "XYZ"}, ErrDecode},
		{"raw invalid", topic, map[string]any{"raw": "15C1AABBCCDD00"}, ErrDecode},
	}
	for _, c := range cases {
		if _, err := Convert(msgRecord(t, c.topic, c.f)); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := Convert(replay.Record{Topic: topic, Payload: []byte("{")}); !errors.Is(err, ErrBadJSON) {
		t.Errorf("bad JSON: err = %v", err)
	}
}

func TestConvertRegion(t *testing.T) {
	// NUL is accepted on purpose: it only ever appears as "region=NUL", which is
	// not a Windows device name; LocalStore's filepath.IsLocal is the backstop.
	for in, want := range map[string]string{"test": "TEST", "sea": "SEA", "NUL": "NUL"} {
		res, err := Convert(msgRecord(t, "meshcore/"+in+"/"+obsKey+"/packets", nil))
		if err != nil || res.Reception.Region != want {
			t.Errorf("region %q: got %q, %v; want %q", in, res.Reception.Region, err, want)
		}
	}
}

func TestReceptionIDStableAcrossRedelivery(t *testing.T) {
	a, _ := Convert(msgRecord(t, topic, nil))
	b, _ := Convert(msgRecord(t, topic, nil))
	c, _ := Convert(msgRecord(t, topic, map[string]any{"timestamp": "2026-11-01T10:00:01+00:00"}))
	if a.Reception.ReceptionID != b.Reception.ReceptionID {
		t.Error("same message produced different reception ids")
	}
	if a.Reception.ReceptionID == c.Reception.ReceptionID {
		t.Error("separate receptions of the same bytes collided")
	}
	if a.Reception.PacketHash != c.Reception.PacketHash {
		t.Error("packet hash must not depend on reception")
	}
}

// convertFile converts every line of an NDJSON capture and fails on any
// unexpected error. Shared by the synthetic and recorded-capture tests.
func convertFile(t *testing.T, path string) (rows, adverts, mismatches, skipped int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var rec replay.Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("%s: bad line: %v", path, err)
		}
		res, err := Convert(rec)
		switch {
		case errors.Is(err, ErrNotPacket):
			skipped++
			continue
		case err != nil:
			t.Fatalf("%s: %v (topic %s)", path, err, rec.Topic)
		}
		rows++
		if res.Advert != nil {
			adverts++
		}
		if res.HashMismatch {
			mismatches++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return
}

func TestConvertSyntheticSample(t *testing.T) {
	rows, adverts, mismatches, _ := convertFile(t, "../../../testdata/synthetic/sea-seed1-10m.ndjson")
	if rows != 4337 || adverts == 0 || mismatches != 0 {
		t.Errorf("rows=%d adverts=%d mismatches=%d; want 4337, >0, 0", rows, adverts, mismatches)
	}
}

// TestRecordedCaptures runs on real captures in testdata/recorded/. It is the
// check for the UNVERIFIED items in docs/protocol.md section 6: every real
// packet must decode, and the observer "hash" field must equal our hash.
func TestRecordedCaptures(t *testing.T) {
	files, _ := filepath.Glob("../../../testdata/recorded/*.ndjson")
	if len(files) == 0 {
		t.Skip("no recorded capture yet: see docs/recording.md")
	}
	for _, f := range files {
		rows, adverts, mismatches, skipped := convertFile(t, f)
		t.Logf("%s: %d rows, %d adverts, %d non-rx skipped, %d hash mismatches", filepath.Base(f), rows, adverts, skipped, mismatches)
		if rows == 0 {
			t.Errorf("%s: no packets", f)
		}
		if mismatches != 0 {
			t.Errorf("%s: %d observer hash fields disagree with calculatePacketHash", f, mismatches)
		}
	}
}

// TestSchemaMatchesJSON keeps the Go structs and /schema/schema.json in lockstep.
func TestSchemaMatchesJSON(t *testing.T) {
	b, err := os.ReadFile("../../../schema/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		SchemaVersion int `json:"schema_version"`
		Tables        map[string][]struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Nullable bool   `json:"nullable"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.SchemaVersion != SchemaVersion {
		t.Errorf("schema.json version %d != SchemaVersion %d", spec.SchemaVersion, SchemaVersion)
	}
	for table, s := range map[string]*parquet.Schema{"receptions": parquet.SchemaOf(Reception{}), "adverts": parquet.SchemaOf(Advert{})} {
		var got, want []string
		for _, f := range s.Fields() {
			got = append(got, fmt.Sprintf("%s %s nullable=%v", f.Name(), typeName(f), f.Optional()))
		}
		for _, c := range spec.Tables[table] {
			want = append(want, fmt.Sprintf("%s %s nullable=%v", c.Name, c.Type, c.Nullable))
		}
		if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
			t.Errorf("%s schema drift:\n--- Go ---\n%s\n--- schema.json ---\n%s", table, g, w)
		}
	}
}

// typeName renders a Parquet column in schema.json's vocabulary.
func typeName(n parquet.Node) string {
	if lt := n.Type().LogicalType(); lt != nil {
		s := lt.String() // e.g. STRING, INT(16,true), TIMESTAMP(isAdjustedToUTC=true,unit=MICROS), LIST
		var bits int
		switch {
		case s == "STRING":
			return "string"
		case strings.HasPrefix(s, "INT("):
			if _, err := fmt.Sscanf(s, "INT(%d", &bits); err != nil {
				return "unparsed " + s
			}
			return fmt.Sprintf("int%d", bits)
		case strings.HasPrefix(s, "TIMESTAMP(isAdjustedToUTC=true"):
			return map[bool]string{true: "timestamp[us, UTC]", false: "timestamp[ms, UTC]"}[strings.Contains(s, "MICROS")]
		case s == "LIST":
			return "list<" + typeName(n.Fields()[0].Fields()[0]) + ">"
		}
	}
	switch n.Type().Kind() {
	case parquet.Float:
		return "float32"
	case parquet.Double:
		return "float64"
	case parquet.FixedLenByteArray:
		return fmt.Sprintf("fixed_len_binary[%d]", n.Type().Length())
	}
	return n.Type().String()
}
