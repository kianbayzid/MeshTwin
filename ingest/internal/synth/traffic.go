package synth

import (
	"container/heap"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"meshtwin/ingest/internal/meshcore"
	"meshtwin/ingest/internal/observer"
	"meshtwin/ingest/internal/replay"
)

// Traffic describes how much traffic to simulate.
type Traffic struct {
	Start         time.Time
	Duration      time.Duration
	PacketsPerMin float64 // originated packets per minute, network-wide (Poisson)
}

// Stats summarises a generation run.
type Stats struct {
	Packets    int // originated transmissions
	Receptions int // observer records emitted
}

// Generate simulates traffic on n and calls emit once per observer reception,
// in RecvTime order. It mirrors the forwarding rules in Mesh.cpp:
//   - flood: every repeater that hears a packet for the first time appends
//     its hash to the path and rebroadcasts; companions never relay.
//   - direct: the path lists the hops still to go; each hop removes itself.
//
// Observers log EVERY reception, including duplicates of a packet they
// already heard via another repeater (expected firmware behaviour, to be
// confirmed against the real capture).
func (n *Network) Generate(cfg Config, tr Traffic, rng *rand.Rand, emit func(replay.Record) error) (Stats, error) {
	if tr.PacketsPerMin <= 0 {
		return Stats{}, fmt.Errorf("packets per minute must be > 0")
	}
	g := &generator{net: n, cfg: cfg, rng: rng}
	for range 3 {
		g.channels = append(g.channels, byte(rng.Uint32()))
	}
	var companions []int
	for i, nd := range n.Nodes {
		if nd.Role == meshcore.RoleChat {
			companions = append(companions, i)
		}
	}

	flush := func(until time.Time) error {
		for g.pending.Len() > 0 && !g.pending[0].t.After(until) {
			r := heap.Pop(&g.pending).(reception)
			if err := emit(r.rec); err != nil {
				return err
			}
			g.stats.Receptions++
		}
		return nil
	}

	end := tr.Start.Add(tr.Duration)
	meanGap := time.Minute.Seconds() / tr.PacketsPerMin
	for t := tr.Start; ; {
		t = t.Add(time.Duration(rng.ExpFloat64() * meanGap * float64(time.Second)))
		if t.After(end) {
			break
		}
		// Every reception of a packet starting at t happens at or after t, so
		// everything earlier is final. Keeps memory bounded on long runs.
		if err := flush(t); err != nil {
			return g.stats, err
		}
		g.originate(t, companions)
		if g.err != nil {
			return g.stats, g.err
		}
	}
	return g.stats, flush(end.Add(24 * time.Hour))
}

type generator struct {
	net      *Network
	cfg      Config
	rng      *rand.Rand
	channels []byte
	pending  receptionHeap
	seq      int
	stats    Stats
	err      error
}

// originate picks a packet kind and sends it.
func (g *generator) originate(t time.Time, companions []int) {
	nodes := g.net.Nodes
	if len(companions) < 2 {
		g.advert(g.rng.IntN(len(nodes)), t)
		return
	}
	src := companions[g.rng.IntN(len(companions))]
	switch k := g.rng.Float64(); {
	case k < 0.15:
		g.advert(g.rng.IntN(len(nodes)), t)
	case k < 0.60:
		payload := append([]byte{g.channels[g.rng.IntN(len(g.channels))]}, g.cipher()...)
		g.flood(src, meshcore.PayloadGrpTxt, payload, t)
	default:
		dst := companions[g.rng.IntN(len(companions))]
		if dst == src {
			return
		}
		payload := append([]byte{nodes[dst].PubKey[0], nodes[src].PubKey[0]}, g.cipher()...)
		route := g.net.shortestRelayPath(src, dst)
		if route == nil || k < 0.70 {
			g.flood(src, meshcore.PayloadTxtMsg, payload, t) // no known route: MeshCore floods
			return
		}
		if g.direct(route, meshcore.PayloadTxtMsg, payload, t) {
			back := make([]int, len(route))
			for i, v := range route {
				back[len(route)-1-i] = v
			}
			g.direct(back, meshcore.PayloadAck, g.randBytes(4), t.Add(2*time.Second))
		}
	}
}

func (g *generator) advert(origin int, t time.Time) {
	nd := g.net.Nodes[origin]
	a := meshcore.Advert{
		PubKey: nd.PubKey, Timestamp: uint32(t.Unix()), Role: nd.Role, Name: nd.Name,
		HasLocation: nd.ShareLocation,
		LatE6:       int32(math.Round(nd.Lat * 1e6)), LonE6: int32(math.Round(nd.Lon * 1e6)),
	}
	copy(a.Signature[:], g.randBytes(meshcore.SignatureSize)) // shortcut: not a real ed25519 signature
	g.flood(origin, meshcore.PayloadAdvert, meshcore.AdvertPayload(a), t)
}

// cipher returns a plausible MAC(2) + ciphertext(16*k). Random bytes: no real messages exist.
func (g *generator) cipher() []byte { return g.randBytes(2 + 16*(1+g.rng.IntN(4))) }

func (g *generator) randBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.rng.Uint32())
	}
	return b
}

func (g *generator) hash(node int) []byte { return g.net.Nodes[node].PubKey[:g.cfg.PathHashSize] }

// delivered decides whether one transmission over a link gets through:
// ~50% at 2 dB above the demod floor, ~98% at 8 dB above.
func (g *generator) delivered(l Link) bool {
	margin := l.SNRDB - g.cfg.Radio.MinSNRDB
	return g.rng.Float64() < 1/(1+math.Exp(-(margin-2)/1.5))
}

func (g *generator) hopDelay(maxMs int) time.Duration {
	return time.Duration(100+g.rng.IntN(maxMs)) * time.Millisecond
}

func (g *generator) flood(origin int, typ meshcore.PayloadType, payload []byte, t time.Time) {
	g.stats.Packets++
	base := meshcore.Packet{Route: meshcore.RouteFlood, Type: typ, PathHashSize: g.cfg.PathHashSize, Payload: payload}
	maxHops := min(63, meshcore.MaxPathSize/g.cfg.PathHashSize)

	type tx struct {
		node int
		path []byte
		t    time.Time
	}
	seen := map[int]bool{origin: true}
	queue := []tx{{origin, nil, t}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range g.net.adj[cur.node] {
			link := g.net.Links[nb.link]
			if !g.delivered(link) {
				continue
			}
			rt := cur.t.Add(g.hopDelay(200))
			p := base
			p.Path = cur.path
			g.receive(nb.node, p, link, rt)
			if g.net.Nodes[nb.node].Forwards() && !seen[nb.node] && len(cur.path)/g.cfg.PathHashSize < maxHops {
				seen[nb.node] = true
				next := append(append([]byte(nil), cur.path...), g.hash(nb.node)...)
				queue = append(queue, tx{nb.node, next, rt.Add(g.hopDelay(2000))})
			}
		}
	}
}

// direct sends along route (src, relays..., dst). Returns false if a hop
// failed; there are no retries (shortcut: real nodes retry, which only adds
// duplicate receptions).
func (g *generator) direct(route []int, typ meshcore.PayloadType, payload []byte, t time.Time) bool {
	g.stats.Packets++
	base := meshcore.Packet{Route: meshcore.RouteDirect, Type: typ, PathHashSize: g.cfg.PathHashSize, Payload: payload}
	for i := 0; i < len(route)-1; i++ {
		var path []byte
		for _, relay := range route[i+1 : len(route)-1] {
			path = append(path, g.hash(relay)...)
		}
		p := base
		p.Path = path
		nextOK := false
		for _, nb := range g.net.adj[route[i]] {
			link := g.net.Links[nb.link]
			if !g.delivered(link) {
				continue
			}
			g.receive(nb.node, p, link, t.Add(g.hopDelay(200)))
			if nb.node == route[i+1] {
				nextOK = true
			}
		}
		if !nextOK {
			return false
		}
		t = t.Add(g.hopDelay(300))
	}
	return true
}

// receive queues an observer record if node is an observer.
func (g *generator) receive(node int, p meshcore.Packet, l Link, t time.Time) {
	nd := g.net.Nodes[node]
	if !nd.Observer || g.err != nil {
		return
	}
	raw, err := meshcore.Encode(p)
	if err != nil {
		g.err = fmt.Errorf("encode: %w", err)
		return
	}
	// SX126x SNR estimates saturate in the low teens; quantised to 0.25 dB like the radio.
	snr := math.Round(math.Min(l.SNRDB+g.rng.NormFloat64()*1.5, 14)*4) / 4
	rssi := math.Round(l.RSSI + g.rng.NormFloat64()*1.5)
	h := meshcore.Hash(p)
	t = t.UTC()
	msg := observer.Message{
		Origin: nd.Name, OriginID: upperHex(nd.PubKey[:]),
		Timestamp: t.Format("2006-01-02T15:04:05.000000+00:00"),
		Type:      "PACKET", Direction: "rx",
		Time: t.Format("15:04:05"), Date: fmt.Sprintf("%d/%d/%d", t.Day(), int(t.Month()), t.Year()),
		Len: strconv.Itoa(len(raw)), PacketType: strconv.Itoa(int(p.Type)),
		Route: "F", PayloadLen: strconv.Itoa(len(p.Payload)), Raw: upperHex(raw),
		SNR: strconv.FormatFloat(snr, 'f', -1, 64), RSSI: strconv.Itoa(int(rssi)),
		Hash: upperHex(h[:]),
	}
	if p.Route == meshcore.RouteDirect {
		msg.Route = "D"
		if p.HopCount() > 0 { // meshcoretomqtt style: "C2 -> E2"
			var hops []string
			for _, hop := range p.Hops() {
				hops = append(hops, upperHex(hop))
			}
			msg.Path, _ = json.Marshal(strings.Join(hops, " -> "))
		}
	}
	body, err := json.Marshal(msg)
	if err != nil {
		g.err = err
		return
	}
	g.seq++
	heap.Push(&g.pending, reception{t: t, seq: g.seq, rec: replay.Record{
		RecvTime: t, Topic: observer.PacketsTopic(g.cfg.Region, msg.OriginID), Payload: body,
	}})
}

func upperHex(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

// reception heap: min by time, then by creation order so output is deterministic.
type reception struct {
	t   time.Time
	seq int
	rec replay.Record
}

type receptionHeap []reception

func (h receptionHeap) Len() int { return len(h) }
func (h receptionHeap) Less(i, j int) bool {
	if h[i].t.Equal(h[j].t) {
		return h[i].seq < h[j].seq
	}
	return h[i].t.Before(h[j].t)
}
func (h receptionHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *receptionHeap) Push(x any)   { *h = append(*h, x.(reception)) }
func (h *receptionHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
