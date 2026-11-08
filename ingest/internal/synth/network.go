// Package synth generates a synthetic MeshCore network and the observer
// traffic it would produce, plus the ground truth (true nodes and links) so
// the analytics can be scored against a known answer.
//
// The radio model here is deliberately simple (log-distance path loss with
// random shadowing). It only needs to produce plausible topology and SNR
// spreads; the terrain-aware model lives in /analytics (Phase 4).
package synth

import (
	"fmt"
	"math"
	"math/rand/v2"

	"meshtwin/ingest/internal/meshcore"
)

// Radio holds link-budget parameters.
type Radio struct {
	TxPowerDBm     float64 // 22 dBm: SX1262 max, a common repeater setting
	FreqMHz        float64 // 910.525: US MeshCore frequency (UNVERIFIED preset, see docs/protocol.md)
	PathLossExp    float64 // log-distance exponent n; 2 = free space, 3-4 = suburban/obstructed
	ShadowSigmaDB  float64 // std dev of per-link log-normal shadowing
	NoiseFloorDBm  float64 // -174 + 10log10(BW) + NF
	MinSNRDB       float64 // demodulation floor for the spreading factor (SX1262 datasheet)
	RepeaterGainDB float64 // extra gain for repeaters: elevated sites, better antennas
	CompanionLoss  float64 // loss for companions: stub antennas, indoors, body blocking
}

// DefaultRadio is tuned so a Seattle-sized region yields a mostly-connected
// mesh with a handful of single points of failure.
func DefaultRadio() Radio {
	return Radio{
		// PathLossExp is a tuning knob, not a physical constant: 7.5 stands in for
		// the ground clutter and terrain this model ignores. It gives median ranges
		// of ~2.8 km companion-companion, ~4.9 km companion-repeater and ~8.5 km
		// repeater-repeater, and (over seeds 1-3) a connected mesh with 1-7
		// single points of failure. The physical model is Phase 4's job.
		TxPowerDBm: 22, FreqMHz: 910.525, PathLossExp: 7.5, ShadowSigmaDB: 8,
		NoiseFloorDBm:  -174 + 10*math.Log10(62_500) + 6, // BW 62.5 kHz, NF 6 dB => ~-120 dBm
		MinSNRDB:       -7.5,                             // SF7
		RepeaterGainDB: 6,
		CompanionLoss:  12,
	}
}

// Config describes the synthetic network.
type Config struct {
	Seed                           uint64
	Region                         string // IATA code used in MQTT topics
	MinLat, MaxLat, MinLon, MaxLon float64
	Clusters                       int // population centres; companions gather around them
	Repeaters, Companions          int
	Observers                      int // chosen from repeaters first
	PathHashSize                   int // 1 (legacy default, collides), 2 or 3
	Radio                          Radio
}

// DefaultConfig covers greater Seattle.
func DefaultConfig() Config {
	return Config{
		Seed: 1, Region: "SEA",
		MinLat: 47.40, MaxLat: 47.80, MinLon: -122.45, MaxLon: -122.05,
		Clusters: 6, Repeaters: 30, Companions: 60, Observers: 8,
		PathHashSize: 1, Radio: DefaultRadio(),
	}
}

// Node is one simulated radio.
type Node struct {
	PubKey   [meshcore.PubKeySize]byte
	Name     string
	Role     uint8 // meshcore.RoleRepeater or RoleChat
	Lat, Lon float64
	Observer bool
	GainDB   float64
	// ShareLocation is false for companions that do not put lat/lon in adverts.
	ShareLocation bool
}

// Forwards reports whether the node relays flood packets. In MeshCore only
// repeaters do; companions never relay.
func (n Node) Forwards() bool { return n.Role == meshcore.RoleRepeater }

// Link is an undirected radio link, A < B. SNR is the mean; symmetric
// because shadowing is drawn once per pair (real links are often asymmetric).
type Link struct {
	A, B  int
	SNRDB float64
	RSSI  float64
}

type neighbor struct {
	node int
	link int
}

// Network is a set of nodes and the links between them.
type Network struct {
	Nodes []Node
	Links []Link
	adj   [][]neighbor
}

// NewNetwork places nodes and computes links. Same seed => same network.
func NewNetwork(cfg Config, rng *rand.Rand) (*Network, error) {
	if cfg.PathHashSize < 1 || cfg.PathHashSize > 3 {
		return nil, fmt.Errorf("path hash size %d not in 1..3", cfg.PathHashSize)
	}
	if cfg.Observers > cfg.Repeaters+cfg.Companions {
		return nil, fmt.Errorf("more observers (%d) than nodes", cfg.Observers)
	}
	type pt struct{ lat, lon float64 }
	uniform := func() pt {
		return pt{cfg.MinLat + rng.Float64()*(cfg.MaxLat-cfg.MinLat), cfg.MinLon + rng.Float64()*(cfg.MaxLon-cfg.MinLon)}
	}
	centres := make([]pt, max(cfg.Clusters, 1))
	for i := range centres {
		centres[i] = uniform()
	}
	const sigmaKm = 2.0
	nearCentre := func() pt {
		c := centres[rng.IntN(len(centres))]
		return pt{c.lat + rng.NormFloat64()*sigmaKm/111.0, c.lon + rng.NormFloat64()*sigmaKm/(111.0*math.Cos(c.lat*math.Pi/180))}
	}

	n := &Network{}
	add := func(role uint8, name string, p pt, gain float64, share bool) {
		var node Node
		for i := range node.PubKey {
			node.PubKey[i] = byte(rng.Uint32())
		}
		node.Role, node.Name, node.Lat, node.Lon, node.GainDB, node.ShareLocation = role, name, p.lat, p.lon, gain, share
		n.Nodes = append(n.Nodes, node)
	}
	for i := range cfg.Repeaters {
		p := uniform() // half on "hilltops" anywhere, half near towns
		if i%2 == 0 {
			p = nearCentre()
		}
		add(meshcore.RoleRepeater, fmt.Sprintf("%s-RPT-%02d", cfg.Region, i+1), p, cfg.Radio.RepeaterGainDB, true)
	}
	for i := range cfg.Companions {
		add(meshcore.RoleChat, fmt.Sprintf("%s-CMP-%02d", cfg.Region, i+1), nearCentre(), -cfg.Radio.CompanionLoss, rng.Float64() < 0.7)
	}
	for i := range min(cfg.Observers, len(n.Nodes)) {
		n.Nodes[i].Observer = true // repeaters come first, so observers are repeaters when possible
	}

	n.adj = make([][]neighbor, len(n.Nodes))
	r := cfg.Radio
	fspl1km := 20*math.Log10(r.FreqMHz) + 32.44 // free-space loss at 1 km
	for a := range n.Nodes {
		for b := a + 1; b < len(n.Nodes); b++ {
			d := math.Max(haversineKm(n.Nodes[a], n.Nodes[b]), 0.05)
			pl := fspl1km + 10*r.PathLossExp*math.Log10(d) + rng.NormFloat64()*r.ShadowSigmaDB
			rssi := r.TxPowerDBm + n.Nodes[a].GainDB + n.Nodes[b].GainDB - pl
			snr := rssi - r.NoiseFloorDBm
			if snr < r.MinSNRDB {
				continue
			}
			n.Links = append(n.Links, Link{A: a, B: b, SNRDB: snr, RSSI: rssi})
			id := len(n.Links) - 1
			n.adj[a] = append(n.adj[a], neighbor{b, id})
			n.adj[b] = append(n.adj[b], neighbor{a, id})
		}
	}
	return n, nil
}

func haversineKm(a, b Node) float64 {
	const earthKm = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (b.Lat-a.Lat)*rad, (b.Lon-a.Lon)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthKm * math.Asin(math.Sqrt(h))
}

// shortestRelayPath returns the nodes from src to dst (inclusive) by fewest
// hops, relaying only through repeaters. nil if unreachable.
func (n *Network) shortestRelayPath(src, dst int) []int {
	prev := make([]int, len(n.Nodes))
	for i := range prev {
		prev[i] = -1
	}
	prev[src] = src
	queue := []int{src}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		if u == dst {
			break
		}
		if u != src && !n.Nodes[u].Forwards() {
			continue // companions can be endpoints but never relays
		}
		for _, nb := range n.adj[u] {
			if prev[nb.node] == -1 {
				prev[nb.node] = u
				queue = append(queue, nb.node)
			}
		}
	}
	if prev[dst] == -1 {
		return nil
	}
	var path []int
	for v := dst; v != src; v = prev[v] {
		path = append(path, v)
	}
	path = append(path, src)
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}
