package synth

import "meshtwin/ingest/internal/meshcore"

// Truth is the generator's answer key: what the analytics should recover
// from the observer traffic alone.
type Truth struct {
	Region       string      `json:"region"`
	Seed         uint64      `json:"seed"`
	PathHashSize int         `json:"path_hash_size"`
	Nodes        []TruthNode `json:"nodes"`
	Links        []TruthLink `json:"links"`
}

type TruthNode struct {
	PubKey   string  `json:"pubkey"`
	Name     string  `json:"name"`
	Role     string  `json:"role"` // "repeater" or "companion"
	Lat      float64 `json:"lat"`  // synthetic, so publishing exact values is fine
	Lon      float64 `json:"lon"`
	Observer bool    `json:"observer"`
}

// TruthLink is undirected; A and B are public keys (hex).
type TruthLink struct {
	A     string  `json:"a"`
	B     string  `json:"b"`
	SNRDB float64 `json:"snr_db"` // mean, before the observer's ~14 dB clamp
}

// Truth exports the network as an answer key.
func (n *Network) Truth(cfg Config) Truth {
	t := Truth{Region: cfg.Region, Seed: cfg.Seed, PathHashSize: cfg.PathHashSize}
	for _, nd := range n.Nodes {
		role := "companion"
		if nd.Role == meshcore.RoleRepeater {
			role = "repeater"
		}
		t.Nodes = append(t.Nodes, TruthNode{upperHex(nd.PubKey[:]), nd.Name, role, nd.Lat, nd.Lon, nd.Observer})
	}
	for _, l := range n.Links {
		t.Links = append(t.Links, TruthLink{upperHex(n.Nodes[l.A].PubKey[:]), upperHex(n.Nodes[l.B].PubKey[:]), l.SNRDB})
	}
	return t
}
