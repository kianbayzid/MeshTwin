// Package meshcore decodes and encodes MeshCore over-the-air packets.
//
// Every layout rule here comes from meshcore-dev/MeshCore at commit 81dbae5
// (src/Packet.h, src/Packet.cpp, src/Mesh.cpp, src/MeshCore.h). See
// docs/protocol.md for the field-by-field reference and source links.
//
// This package reads only the plaintext framing and public fields. It never
// attempts to decrypt payloads.
package meshcore

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Route types: header bits 0-1 (PH_ROUTE_MASK).
const (
	RouteTransportFlood  uint8 = 0x00 // flood + 4 bytes of transport codes
	RouteFlood           uint8 = 0x01 // flood: each repeater APPENDS its hash to path
	RouteDirect          uint8 = 0x02 // direct: path is the hops still to go; each hop REMOVES itself
	RouteTransportDirect uint8 = 0x03 // direct + transport codes
)

// PayloadType is header bits 2-5 (PH_TYPE_MASK after PH_TYPE_SHIFT).
type PayloadType uint8

const (
	PayloadReq       PayloadType = 0x00
	PayloadResponse  PayloadType = 0x01
	PayloadTxtMsg    PayloadType = 0x02
	PayloadAck       PayloadType = 0x03
	PayloadAdvert    PayloadType = 0x04
	PayloadGrpTxt    PayloadType = 0x05
	PayloadGrpData   PayloadType = 0x06
	PayloadAnonReq   PayloadType = 0x07
	PayloadPath      PayloadType = 0x08
	PayloadTrace     PayloadType = 0x09
	PayloadMultipart PayloadType = 0x0A
	PayloadControl   PayloadType = 0x0B
	PayloadRawCustom PayloadType = 0x0F
)

var payloadNames = map[PayloadType]string{
	PayloadReq: "REQ", PayloadResponse: "RESPONSE", PayloadTxtMsg: "TXT_MSG",
	PayloadAck: "ACK", PayloadAdvert: "ADVERT", PayloadGrpTxt: "GRP_TXT",
	PayloadGrpData: "GRP_DATA", PayloadAnonReq: "ANON_REQ", PayloadPath: "PATH",
	PayloadTrace: "TRACE", PayloadMultipart: "MULTIPART", PayloadControl: "CONTROL",
	PayloadRawCustom: "RAW_CUSTOM",
}

func (t PayloadType) String() string {
	if s, ok := payloadNames[t]; ok {
		return s
	}
	return fmt.Sprintf("UNKNOWN_%d", uint8(t))
}

// Limits from src/MeshCore.h.
const (
	MaxPathSize    = 64  // MAX_PATH_SIZE: max path bytes (hash_count * hash_size)
	MaxPayloadSize = 184 // MAX_PACKET_PAYLOAD
	PacketHashSize = 8   // MAX_HASH_SIZE: truncated SHA-256 used as packet identity
	PubKeySize     = 32
	SignatureSize  = 64
)

// Packet is one decoded over-the-air packet.
type Packet struct {
	Route          uint8
	Type           PayloadType
	Version        uint8     // payload version, header bits 6-7; 0 = PAYLOAD_VER_1
	TransportCodes [2]uint16 // zero unless HasTransportCodes()
	PathHashSize   int       // bytes per hop entry: 1, 2 or 3
	Path           []byte    // HopCount()*PathHashSize bytes (see Hops)
	Payload        []byte    // still encrypted for message types; never decrypted here
}

var (
	ErrTooShort         = errors.New("packet too short")
	ErrReservedHashSize = errors.New("reserved path hash size 4")
	ErrPathTooLong      = errors.New("path longer than MAX_PATH_SIZE")
	ErrNoPayload        = errors.New("packet has no payload")
	ErrPayloadTooLong   = errors.New("payload longer than MAX_PACKET_PAYLOAD")
)

// HasTransportCodes mirrors Packet::hasTransportCodes().
func (p Packet) HasTransportCodes() bool {
	return p.Route == RouteTransportFlood || p.Route == RouteTransportDirect
}

// IsFlood mirrors Packet::isRouteFlood().
func (p Packet) IsFlood() bool {
	return p.Route == RouteFlood || p.Route == RouteTransportFlood
}

// HopCount is the number of path entries.
func (p Packet) HopCount() int { return len(p.Path) / p.PathHashSize }

// Hops splits Path into entries of PathHashSize bytes.
//
// What an entry means depends on the route:
//   - flood: hash of each repeater that relayed it, oldest first. The receiver
//     heard the packet from the LAST entry (or from the origin if empty).
//   - direct: hashes of hops still to go; the next hop is the FIRST entry.
//   - direct TRACE: entries are per-hop SNR bytes, not hashes (see TraceSNRs).
//
// A hash is a 1-3 byte public-key prefix, so it can match several nodes.
func (p Packet) Hops() [][]byte {
	hops := make([][]byte, 0, p.HopCount())
	for i := 0; i+p.PathHashSize <= len(p.Path); i += p.PathHashSize {
		hops = append(hops, p.Path[i:i+p.PathHashSize])
	}
	return hops
}

// TraceSNRs returns per-hop SNR in dB for a direct TRACE packet, or nil.
// Mesh.cpp appends (int8)(snr*4) to path for each hop a TRACE crosses.
func (p Packet) TraceSNRs() []float32 {
	if p.Type != PayloadTrace || p.IsFlood() {
		return nil
	}
	snrs := make([]float32, len(p.Path))
	for i, b := range p.Path {
		snrs[i] = float32(int8(b)) / 4 //nolint:gosec // G115: firmware stores SNR*4 as a signed byte
	}
	return snrs
}

// pathLenByte packs hash size and count like Packet::setPathHashSizeAndCount.
func (p Packet) pathLenByte() byte {
	return byte((p.PathHashSize-1)<<6 | p.HopCount()&63) //nolint:gosec // G115: hash size 1-3 (2 bits) and count masked to 6 bits fill exactly one byte
}

// Decode parses wire bytes, following Packet::readFrom:
//
//	header(1) | transport_codes(4, route 0 or 3) | path_len(1) | path | payload
//
// Multi-byte integers are little-endian: the firmware memcpy's them on
// little-endian MCUs (ESP32, nRF52, RP2040, STM32).
// The returned slices are copies, so raw may be reused by the caller.
func Decode(raw []byte) (Packet, error) {
	if len(raw) < 2 {
		return Packet{}, ErrTooShort
	}
	var p Packet
	h := raw[0]
	p.Route = h & 0x03
	p.Type = PayloadType((h >> 2) & 0x0F)
	p.Version = (h >> 6) & 0x03
	i := 1

	if p.HasTransportCodes() {
		if len(raw) < i+4+1 {
			return Packet{}, ErrTooShort
		}
		p.TransportCodes[0] = binary.LittleEndian.Uint16(raw[i:])
		p.TransportCodes[1] = binary.LittleEndian.Uint16(raw[i+2:])
		i += 4
	}

	pathLen := raw[i]
	i++
	p.PathHashSize = int(pathLen>>6) + 1
	count := int(pathLen & 63)
	if p.PathHashSize == 4 {
		return Packet{}, ErrReservedHashSize
	}
	n := count * p.PathHashSize
	if n > MaxPathSize {
		return Packet{}, ErrPathTooLong
	}
	if i+n > len(raw) {
		return Packet{}, ErrTooShort
	}
	p.Path = append([]byte(nil), raw[i:i+n]...)
	i += n

	if i >= len(raw) {
		return Packet{}, ErrNoPayload
	}
	if len(raw)-i > MaxPayloadSize {
		return Packet{}, ErrPayloadTooLong
	}
	p.Payload = append([]byte(nil), raw[i:]...)
	return p, nil
}

// Encode is the inverse of Decode, following Packet::writeTo.
func Encode(p Packet) ([]byte, error) {
	switch {
	case p.PathHashSize < 1 || p.PathHashSize > 3:
		return nil, fmt.Errorf("path hash size %d not in 1..3", p.PathHashSize)
	case len(p.Path)%p.PathHashSize != 0:
		return nil, fmt.Errorf("path length %d not a multiple of hash size %d", len(p.Path), p.PathHashSize)
	case len(p.Path) > MaxPathSize || p.HopCount() > 63:
		return nil, ErrPathTooLong
	case len(p.Payload) == 0:
		return nil, ErrNoPayload
	case len(p.Payload) > MaxPayloadSize:
		return nil, ErrPayloadTooLong
	}
	out := []byte{p.Version<<6 | byte(p.Type&0x0F)<<2 | p.Route&0x03}
	if p.HasTransportCodes() {
		out = binary.LittleEndian.AppendUint16(out, p.TransportCodes[0])
		out = binary.LittleEndian.AppendUint16(out, p.TransportCodes[1])
	}
	out = append(out, p.pathLenByte())
	out = append(out, p.Path...)
	return append(out, p.Payload...), nil
}

// Hash is the packet identity, following Packet::calculatePacketHash:
// SHA-256(payload_type || payload)[:8]. TRACE also mixes in path_len (a
// uint16 in the firmware struct, so 2 little-endian bytes).
//
// The path is excluded, so every copy of one transmission has the same hash
// no matter which repeaters relayed it or which observer heard it.
func Hash(p Packet) [PacketHashSize]byte {
	s := sha256.New()
	s.Write([]byte{byte(p.Type)})
	if p.Type == PayloadTrace {
		s.Write(binary.LittleEndian.AppendUint16(nil, uint16(p.pathLenByte())))
	}
	s.Write(p.Payload)
	var h [PacketHashSize]byte
	copy(h[:], s.Sum(nil))
	return h
}

// Endpoints returns the plaintext source and destination identifiers the
// payload exposes, or nil where it exposes none. Each is either a full 32-byte
// public key or a 1-byte public-key prefix (PATH_HASH_SIZE in V1 payloads).
//
//	ADVERT                     src = pubkey (bytes 0..31)
//	ANON_REQ                   dst = payload[0], src = pubkey (bytes 1..32)
//	REQ/RESPONSE/TXT_MSG/PATH  dst = payload[0], src = payload[1]
//
// Group messages expose only a channel hash, which is not a node identity.
// Layouts are only known for payload version 1, so others return nil.
func (p Packet) Endpoints() (src, dst []byte) {
	if p.Version != 0 {
		return nil, nil
	}
	pl := p.Payload
	switch p.Type {
	case PayloadAdvert:
		if len(pl) >= PubKeySize {
			return pl[:PubKeySize], nil
		}
	case PayloadAnonReq:
		if len(pl) >= 1+PubKeySize {
			return pl[1 : 1+PubKeySize], pl[:1]
		}
	case PayloadReq, PayloadResponse, PayloadTxtMsg, PayloadPath:
		if len(pl) >= 2 {
			return pl[1:2], pl[:1]
		}
	}
	return nil, nil
}
