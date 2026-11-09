// Package anonymize rewrites a real observer capture so it can be published
// as test data. It keeps everything the analytics care about (timing, SNR,
// RSSI, path structure, hash collisions, which receptions belong to the same
// packet) and removes what identifies people (public keys, names, exact
// locations, ciphertext).
//
// The secret key is random per run and never stored, so the mapping cannot be
// reversed from the output.
package anonymize

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"meshtwin/ingest/internal/meshcore"
	"meshtwin/ingest/internal/observer"
	"meshtwin/ingest/internal/replay"
)

// ErrDrop marks messages that are not published at all (status messages
// carry device model, firmware and battery data; tx messages carry no
// reception data).
var ErrDrop = errors.New("message dropped")

// GridDeg is the coordinate grid: 0.01° ≈ 1.1 km north-south, ≈ 0.75 km
// east-west at Seattle's latitude.
const GridDeg = 0.01

// Anonymizer holds the per-run secret mapping.
type Anonymizer struct {
	key            []byte
	perm           [3][256]byte // keyed byte permutation per prefix position
	latOff, lonOff float64      // secret shift applied before snapping
}

// New derives all mappings from key (use 32 random bytes).
func New(key []byte) *Anonymizer {
	a := &Anonymizer{key: key}
	seed := a.prf("perm", nil, 16)
	rng := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(seed), binary.LittleEndian.Uint64(seed[8:])))
	for i := range a.perm {
		for j := range a.perm[i] {
			a.perm[i][j] = byte(j)
		}
		rng.Shuffle(256, func(x, y int) { a.perm[i][x], a.perm[i][y] = a.perm[i][y], a.perm[i][x] })
	}
	a.latOff = (rng.Float64()*2 - 1) * 0.02 // up to ~2 km
	a.lonOff = (rng.Float64()*2 - 1) * 0.02
	return a
}

// prf returns n deterministic pseudorandom bytes for (label, data): the same
// input always maps to the same output, so every copy of one packet maps the same way.
func (a *Anonymizer) prf(label string, data []byte, n int) []byte {
	var out []byte
	for ctr := byte(0); len(out) < n; ctr++ {
		m := hmac.New(sha256.New, a.key)
		m.Write([]byte(label))
		m.Write([]byte{0, ctr})
		m.Write(data)
		out = m.Sum(out)
	}
	return out[:n]
}

// prefix maps a 1-3 byte key prefix (hop hash, src/dst hash). Byte i goes
// through permutation i, so a prefix of a key maps to the prefix of the
// mapped key, and two nodes collide after mapping exactly when they did before.
func (a *Anonymizer) prefix(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = a.perm[min(i, 2)][v]
	}
	return out
}

// PubKey maps a full 32-byte key: permuted prefix + keyed hash for the rest.
func (a *Anonymizer) PubKey(k []byte) []byte {
	return append(a.prefix(k[:3]), a.prf("pubkey", k, len(k)-3)...)
}

func (a *Anonymizer) snap(v, off float64) float64 {
	return math.Round((v+off)/GridDeg) * GridDeg
}

// Stats counts what Record saw.
type Stats struct {
	Kept, Dropped, HashMismatchesInOriginal int
}

// Record anonymizes one message, or returns ErrDrop.
func (a *Anonymizer) Record(r replay.Record, st *Stats) (replay.Record, error) {
	parts := strings.Split(r.Topic, "/")
	if len(parts) != 4 || parts[0] != "meshcore" || parts[3] != "packets" {
		st.Dropped++
		return replay.Record{}, ErrDrop
	}
	obsKey, err := hex.DecodeString(parts[2])
	if err != nil || len(obsKey) != meshcore.PubKeySize {
		st.Dropped++
		return replay.Record{}, ErrDrop
	}
	var in observer.Message
	if err := json.Unmarshal(r.Payload, &in); err != nil || in.Type != "PACKET" || in.Direction != "rx" {
		st.Dropped++
		return replay.Record{}, ErrDrop
	}
	raw, err := hex.DecodeString(in.Raw)
	if err != nil {
		st.Dropped++
		return replay.Record{}, ErrDrop
	}
	p, err := meshcore.Decode(raw)
	if err != nil {
		st.Dropped++ // undecodable packets can't be anonymized safely; count them
		return replay.Record{}, ErrDrop
	}
	origHash := meshcore.Hash(p)
	matched := strings.EqualFold(in.Hash, hex.EncodeToString(origHash[:]))
	if !matched {
		st.HashMismatchesInOriginal++
	}

	if p.TraceSNRs() == nil { // TRACE paths hold SNRs, not hashes: leave them
		p.Path = a.prefixHops(p.Path, p.PathHashSize)
	}
	if p.Payload, err = a.payload(p); err != nil {
		return replay.Record{}, err
	}
	newRaw, err := meshcore.Encode(p)
	if err != nil {
		return replay.Record{}, fmt.Errorf("re-encode: %w", err)
	}
	h := meshcore.Hash(p)
	hashField := upper(h[:])
	if !matched { // keep the original's (dis)agreement so the hash check stays meaningful
		hashField = upper(a.prf("hash", []byte(in.Hash), 8))
	}

	newObs := a.PubKey(obsKey)
	out := observer.Message{
		Origin: "obs-" + upper(newObs[:2]), OriginID: upper(newObs),
		Timestamp: in.Timestamp, Type: in.Type, Direction: in.Direction, Time: in.Time, Date: in.Date,
		Len: fmt.Sprint(len(newRaw)), PacketType: in.PacketType, Route: in.Route,
		PayloadLen: fmt.Sprint(len(p.Payload)), Raw: upper(newRaw),
		SNR: in.SNR, RSSI: in.RSSI, Score: in.Score, Hash: hashField,
		// path dropped: redundant with raw
	}
	body, err := json.Marshal(out)
	if err != nil {
		return replay.Record{}, err
	}
	st.Kept++
	return replay.Record{RecvTime: r.RecvTime, Topic: observer.PacketsTopic(parts[1], out.OriginID), Payload: body}, nil
}

func (a *Anonymizer) prefixHops(path []byte, size int) []byte {
	out := make([]byte, 0, len(path))
	for i := 0; i+size <= len(path); i += size {
		out = append(out, a.prefix(path[i:i+size])...)
	}
	return append(out, a.prefix(path[len(out):])...) // a truncated last entry keeps its length
}

// payload maps the plaintext identifiers (see docs/protocol.md section 4) and
// replaces everything else with keyed pseudorandom bytes of the same length.
func (a *Anonymizer) payload(p meshcore.Packet) ([]byte, error) {
	pl := p.Payload
	opaque := func(b []byte) []byte { return a.prf("payload", b, len(b)) }
	if p.Version != 0 {
		return opaque(pl), nil
	}
	switch p.Type {
	case meshcore.PayloadAdvert:
		adv, err := meshcore.ParseAdvert(pl)
		if err != nil {
			return opaque(pl), nil
		}
		copy(adv.PubKey[:], a.PubKey(adv.PubKey[:]))
		copy(adv.Signature[:], a.prf("sig", adv.Signature[:], meshcore.SignatureSize))
		if adv.Name != "" {
			adv.Name = "node-" + upper(adv.PubKey[:2])
		}
		if adv.HasLocation {
			adv.LatE6 = int32(math.Round(a.snap(adv.Lat(), a.latOff) * 1e6))
			adv.LonE6 = int32(math.Round(a.snap(adv.Lon(), a.lonOff) * 1e6))
		}
		return meshcore.AdvertPayload(adv), nil
	case meshcore.PayloadReq, meshcore.PayloadResponse, meshcore.PayloadTxtMsg, meshcore.PayloadPath:
		if len(pl) >= 2 {
			return append(a.prefix(pl[:1]), append(a.prefix(pl[1:2]), opaque(pl[2:])...)...), nil
		}
	case meshcore.PayloadAnonReq:
		if len(pl) >= 1+meshcore.PubKeySize {
			out := append(a.prefix(pl[:1]), a.PubKey(pl[1:1+meshcore.PubKeySize])...)
			return append(out, opaque(pl[1+meshcore.PubKeySize:])...), nil
		}
	case meshcore.PayloadGrpTxt, meshcore.PayloadGrpData:
		if len(pl) >= 1 {
			return append(a.prefix(pl[:1]), opaque(pl[1:])...), nil
		}
	case meshcore.PayloadTrace:
		// tag(4) auth(4) flags(1) then hop hashes of 1<<(flags&3) bytes.
		if len(pl) >= 9 {
			size := 1 << (pl[8] & 3)
			out := append(opaque(pl[:8]), pl[8])
			return append(out, a.prefixHops(pl[9:], size)...), nil
		}
	}
	return opaque(pl), nil
}

func upper(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }
