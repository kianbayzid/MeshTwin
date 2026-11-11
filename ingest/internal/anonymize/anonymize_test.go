package anonymize

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"meshtwin/ingest/internal/ingest"
	"meshtwin/ingest/internal/meshcore"
	"meshtwin/ingest/internal/replay"
)

const sample = "../../../testdata/synthetic/sea-seed1-10m.ndjson"

func load(t *testing.T) []replay.Record {
	t.Helper()
	f, err := os.Open(sample)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var recs []replay.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r replay.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

func run(t *testing.T, key byte, recs []replay.Record) ([]replay.Record, Stats) {
	t.Helper()
	a := New(bytes.Repeat([]byte{key}, 32))
	var st Stats
	var out []replay.Record
	for _, r := range recs {
		o, err := a.Record(r, &st)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, o)
	}
	return out, st
}

func TestAnonymizePreservesStructure(t *testing.T) {
	in := load(t)
	out, st := run(t, 1, in)
	if st.Kept != len(in) || st.HashMismatchesInOriginal != 0 {
		t.Fatalf("stats = %+v", st)
	}

	oldToNew := map[[8]byte][8]byte{}
	newSeen := map[[8]byte][8]byte{}
	oldHops, newHops := map[string]bool{}, map[string]bool{}
	for i := range in {
		before, err1 := ingest.Convert(in[i])
		after, err2 := ingest.Convert(out[i])
		if err1 != nil || err2 != nil {
			t.Fatalf("convert: %v / %v", err1, err2)
		}
		b, a := before.Reception, after.Reception
		if after.HashMismatch {
			t.Fatal("anonymized hash field disagrees with packet")
		}
		// Everything the analytics use is unchanged. Advert lengths may change on
		// purpose: names become fixed-format "node-XXXX" so name length can't fingerprint.
		lenChanged := a.PayloadLen != b.PayloadLen && b.PayloadType != 4
		if !a.RxTime.Equal(b.RxTime) || *a.SNRDB != *b.SNRDB || *a.RSSIDBm != *b.RSSIDBm ||
			a.HopCount != b.HopCount || a.PayloadType != b.PayloadType || lenChanged {
			t.Fatalf("record %d: analytic fields changed:\n%+v\n%+v", i, b, a)
		}
		// ...and packet identity maps one-to-one, so receptions still group by packet.
		if prev, ok := oldToNew[b.PacketHash]; ok && prev != a.PacketHash {
			t.Fatal("one packet mapped to two hashes")
		}
		if prev, ok := newSeen[a.PacketHash]; ok && prev != b.PacketHash {
			t.Fatal("two packets merged into one hash")
		}
		oldToNew[b.PacketHash], newSeen[a.PacketHash] = a.PacketHash, b.PacketHash
		for _, h := range b.PathHops {
			oldHops[h] = true
		}
		for _, h := range a.PathHops {
			newHops[h] = true
		}
		if after.Advert != nil && after.Advert.Lat != nil {
			for _, v := range []float64{*after.Advert.Lat, *after.Advert.Lon} {
				if r := math.Abs(v/GridDeg - math.Round(v/GridDeg)); r > 1e-6 {
					t.Errorf("coordinate %v not on the %v° grid", v, GridDeg)
				}
			}
		}
	}
	// Byte permutation keeps the number of distinct hop hashes (= collision structure).
	if len(oldHops) != len(newHops) {
		t.Errorf("distinct hops %d -> %d; collision structure changed", len(oldHops), len(newHops))
	}
}

func TestAnonymizeRemovesIdentifiers(t *testing.T) {
	in := load(t)
	out, _ := run(t, 1, in)
	var blob strings.Builder
	for _, r := range out {
		blob.WriteString(r.Topic)
		blob.Write(r.Payload)
	}
	text := strings.ToUpper(blob.String())

	var truth struct {
		Nodes []struct{ PubKey, Name string } `json:"nodes"`
	}
	b, _ := os.ReadFile("../../../testdata/synthetic/sea-seed1-truth.json")
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	for _, n := range truth.Nodes {
		if strings.Contains(text, n.PubKey[8:40]) { // any long run of the real key
			t.Errorf("public key of %s leaked", n.Name)
		}
		if strings.Contains(text, strings.ToUpper(n.Name)) {
			t.Errorf("name %s leaked", n.Name)
		}
		// The key also appears hex-encoded inside raw; check that form too.
		raw, _ := hex.DecodeString(n.PubKey)
		if strings.Contains(text, strings.ToUpper(hex.EncodeToString(raw[4:20]))) {
			t.Errorf("public key bytes of %s leaked in raw", n.Name)
		}
	}
}

func TestAnonymizeKeyDependence(t *testing.T) {
	in := load(t)[:50]
	a1, _ := run(t, 1, in)
	a2, _ := run(t, 1, in)
	b, _ := run(t, 2, in)
	if string(a1[0].Payload) != string(a2[0].Payload) {
		t.Error("same key gave different output")
	}
	if string(a1[0].Payload) == string(b[0].Payload) {
		t.Error("different keys gave the same output")
	}
}

func TestAnonymizeDropsNonPackets(t *testing.T) {
	a := New(make([]byte, 32))
	var st Stats
	status := replay.Record{Topic: "meshcore/SEA/" + strings.Repeat("AB", 32) + "/status", Payload: []byte(`{"status":"online","model":"x"}`)}
	if _, err := a.Record(status, &st); !errors.Is(err, ErrDrop) || st.Dropped != 1 {
		t.Errorf("status message not dropped: %v", err)
	}
}

func TestPrefixIsConsistentWithPubKey(t *testing.T) {
	a := New(make([]byte, 32))
	key := bytes.Repeat([]byte{0x35, 0x1F, 0xC1, 0x77}, 8)
	full := a.PubKey(key)
	for n := 1; n <= 3; n++ {
		if !bytes.Equal(a.prefix(key[:n]), full[:n]) {
			t.Errorf("%d-byte hop hash does not match the mapped key's prefix", n)
		}
	}
	if len(full) != meshcore.PubKeySize {
		t.Errorf("mapped key length %d", len(full))
	}
}
