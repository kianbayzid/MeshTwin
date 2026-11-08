package synth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"meshtwin/ingest/internal/meshcore"
	"meshtwin/ingest/internal/observer"
	"meshtwin/ingest/internal/replay"
)

func generate(t *testing.T, cfg Config, minutes int) (*Network, []replay.Record) {
	t.Helper()
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed))
	n, err := NewNetwork(cfg, rng)
	if err != nil {
		t.Fatal(err)
	}
	var recs []replay.Record
	tr := Traffic{Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Duration: time.Duration(minutes) * time.Minute, PacketsPerMin: 20}
	if _, err := n.Generate(cfg, tr, rng, func(r replay.Record) error { recs = append(recs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("no receptions generated")
	}
	return n, recs
}

func TestDeterministic(t *testing.T) {
	encode := func() []byte {
		_, recs := generate(t, DefaultConfig(), 5)
		var buf bytes.Buffer
		for _, r := range recs {
			_ = json.NewEncoder(&buf).Encode(r)
		}
		return buf.Bytes()
	}
	if !bytes.Equal(encode(), encode()) {
		t.Error("same seed produced different output")
	}
}

// TestRoundTrip decodes every record the way the ingester will and checks
// that the topology it implies matches the ground truth. 3-byte hashes make
// every hop unambiguous here; TestHashCollisions covers the ambiguous case.
func TestRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PathHashSize = 3
	n, recs := generate(t, cfg, 10)

	byHash := map[string]int{}
	for i, nd := range n.Nodes {
		k := hex.EncodeToString(nd.PubKey[:3])
		if _, dup := byHash[k]; dup {
			t.Fatalf("3-byte prefix collision for %s; pick another seed", k)
		}
		byHash[k] = i
	}
	isLink := map[[2]int]bool{}
	for _, l := range n.Links {
		isLink[[2]int{l.A, l.B}], isLink[[2]int{l.B, l.A}] = true, true
	}
	nodeOf := func(hop []byte) int {
		i, ok := byHash[hex.EncodeToString(hop)]
		if !ok {
			t.Fatalf("hop %x matches no node", hop)
		}
		return i
	}

	var prev time.Time
	seen := map[string]int{}
	for _, rec := range recs {
		if rec.RecvTime.Before(prev) {
			t.Fatalf("records out of order: %v after %v", rec.RecvTime, prev)
		}
		prev = rec.RecvTime

		var msg observer.Message
		if err := json.Unmarshal(rec.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		if rec.Topic != observer.PacketsTopic("SEA", msg.OriginID) {
			t.Errorf("topic %q does not match observer %s", rec.Topic, msg.OriginID)
		}
		raw, _ := hex.DecodeString(msg.Raw)
		p, err := meshcore.Decode(raw)
		if err != nil {
			t.Fatalf("generated packet does not decode: %v (%s)", err, msg.Raw)
		}
		if h := meshcore.Hash(p); !strings.EqualFold(hex.EncodeToString(h[:]), msg.Hash) {
			t.Errorf("hash field %s != computed %x", msg.Hash, h)
		}
		obsKey, _ := hex.DecodeString(msg.OriginID)
		obs := nodeOf(obsKey[:3])
		seen[msg.Hash+msg.OriginID]++

		if !p.IsFlood() {
			for _, hop := range p.Hops() {
				if !n.Nodes[nodeOf(hop)].Forwards() {
					t.Errorf("direct path contains non-repeater %x", hop)
				}
			}
			continue
		}
		// Flood: origin -> hops... -> observer must all be true links.
		chain := []int{}
		if src, _ := p.Endpoints(); p.Type == meshcore.PayloadAdvert {
			chain = append(chain, nodeOf(src[:3]))
		}
		for _, hop := range p.Hops() {
			chain = append(chain, nodeOf(hop))
		}
		chain = append(chain, obs)
		for i := 0; i+1 < len(chain); i++ {
			if !isLink[[2]int{chain[i], chain[i+1]}] {
				t.Errorf("path implies link %s-%s that does not exist", n.Nodes[chain[i]].Name, n.Nodes[chain[i+1]].Name)
			}
		}
	}

	dups := 0
	for _, c := range seen {
		if c > 1 {
			dups++
		}
	}
	if dups == 0 {
		t.Error("expected some packets heard more than once by the same observer via different relays")
	}
}

// TestHashCollisions shows why hop hashes cannot be treated as identities:
// with 1-byte hashes, real paths contain hops matching several nodes.
func TestHashCollisions(t *testing.T) {
	cfg := DefaultConfig() // PathHashSize 1
	n, recs := generate(t, cfg, 10)
	matches := map[byte]int{}
	for _, nd := range n.Nodes {
		matches[nd.PubKey[0]]++
	}
	ambiguous := 0
	for _, rec := range recs {
		var msg observer.Message
		_ = json.Unmarshal(rec.Payload, &msg)
		raw, _ := hex.DecodeString(msg.Raw)
		p, _ := meshcore.Decode(raw)
		for _, hop := range p.Hops() {
			if matches[hop[0]] > 1 {
				ambiguous++
			}
		}
	}
	if ambiguous == 0 {
		t.Errorf("expected ambiguous 1-byte hops among %d nodes", len(n.Nodes))
	}
	t.Logf("%d ambiguous hop occurrences in %d receptions", ambiguous, len(recs))
}

func TestConfigValidation(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	bad := DefaultConfig()
	bad.PathHashSize = 4
	if _, err := NewNetwork(bad, rng); err == nil {
		t.Error("accepted hash size 4")
	}
	bad = DefaultConfig()
	bad.Observers = 1000
	if _, err := NewNetwork(bad, rng); err == nil {
		t.Error("accepted more observers than nodes")
	}
}
