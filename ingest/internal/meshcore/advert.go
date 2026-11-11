package meshcore

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// Advert roles: low 4 bits of the appdata flags (ADV_TYPE_*).
const (
	RoleNone     uint8 = 0
	RoleChat     uint8 = 1
	RoleRepeater uint8 = 2
	RoleRoom     uint8 = 3
	RoleSensor   uint8 = 4
)

// Appdata flag bits (src/helpers/AdvertDataHelpers.h).
const (
	advLatLon uint8 = 0x10
	advFeat1  uint8 = 0x20
	advFeat2  uint8 = 0x40
	advName   uint8 = 0x80
)

// MaxAdvertData is MAX_ADVERT_DATA_SIZE; the firmware ignores appdata past it.
const MaxAdvertData = 32

const advertFixedSize = PubKeySize + 4 + SignatureSize // pubkey | timestamp | signature

// Advert is the public content of an ADVERT payload (Mesh::createAdvert):
//
//	pubkey(32) | timestamp(4, LE) | signature(64) | appdata(<=32)
//	appdata = flags(1) | [lat(4) lon(4)] | [feat1(2)] | [feat2(2)] | [name]
//
// Adverts are signed, not encrypted: every node in range can read them.
// The signature is carried through but not verified (shortcut: no ed25519
// check, add one if forged adverts ever show up in real data).
type Advert struct {
	PubKey       [PubKeySize]byte
	Timestamp    uint32 // sender's clock, UNIX seconds; often wrong on nodes without GPS
	Signature    [SignatureSize]byte
	Role         uint8
	HasLocation  bool
	LatE6, LonE6 int32 // degrees * 1e6, as on the wire
	Feat1, Feat2 uint16
	Name         string
}

var ErrBadAdvert = errors.New("malformed advert payload")

// Lat and Lon in degrees. These are often a person's home: never publish
// them unsnapped (see CLAUDE.md, Privacy).
func (a Advert) Lat() float64 { return float64(a.LatE6) / 1e6 }
func (a Advert) Lon() float64 { return float64(a.LonE6) / 1e6 }

// ParseAdvert decodes an ADVERT payload, following Mesh::onRecvPacket and
// AdvertDataParser.
func ParseAdvert(payload []byte) (Advert, error) {
	var a Advert
	if len(payload) < advertFixedSize+1 { // +1: appdata needs at least the flags byte
		return a, ErrBadAdvert
	}
	copy(a.PubKey[:], payload)
	a.Timestamp = binary.LittleEndian.Uint32(payload[PubKeySize:])
	copy(a.Signature[:], payload[PubKeySize+4:])

	app := payload[advertFixedSize:]
	if len(app) > MaxAdvertData {
		app = app[:MaxAdvertData]
	}
	flags := app[0]
	a.Role = flags & 0x0F
	i := 1
	need := func(n int) bool { return i+n <= len(app) }
	if flags&advLatLon != 0 {
		if !need(8) {
			return a, ErrBadAdvert
		}
		a.HasLocation = true
		a.LatE6 = int32(binary.LittleEndian.Uint32(app[i:]))   //nolint:gosec // G115: wire format is two's-complement int32; reinterpreting the bits is the decode
		a.LonE6 = int32(binary.LittleEndian.Uint32(app[i+4:])) //nolint:gosec // G115: as above
		i += 8
	}
	if flags&advFeat1 != 0 {
		if !need(2) {
			return a, ErrBadAdvert
		}
		a.Feat1 = binary.LittleEndian.Uint16(app[i:])
		i += 2
	}
	if flags&advFeat2 != 0 {
		if !need(2) {
			return a, ErrBadAdvert
		}
		a.Feat2 = binary.LittleEndian.Uint16(app[i:])
		i += 2
	}
	if flags&advName != 0 {
		a.Name = string(app[i:])
	}
	return a, nil
}

// AdvertPayload encodes a as an ADVERT payload, following
// AdvertDataBuilder::encodeTo. Like the firmware, the name is cut to a valid
// UTF-8 prefix that fits in MaxAdvertData. Used by the synthetic generator.
func AdvertPayload(a Advert) []byte {
	app := []byte{a.Role & 0x0F}
	if a.HasLocation {
		app[0] |= advLatLon
		app = binary.LittleEndian.AppendUint32(app, uint32(a.LatE6)) //nolint:gosec // G115: two's-complement int32 on the wire, the inverse of ParseAdvert
		app = binary.LittleEndian.AppendUint32(app, uint32(a.LonE6)) //nolint:gosec // G115: as above
	}
	if a.Feat1 != 0 {
		app[0] |= advFeat1
		app = binary.LittleEndian.AppendUint16(app, a.Feat1)
	}
	if a.Feat2 != 0 {
		app[0] |= advFeat2
		app = binary.LittleEndian.AppendUint16(app, a.Feat2)
	}
	if name := utf8Prefix(a.Name, MaxAdvertData-len(app)); name != "" {
		app[0] |= advName
		app = append(app, name...)
	}
	out := make([]byte, 0, advertFixedSize+len(app))
	out = append(out, a.PubKey[:]...)
	out = binary.LittleEndian.AppendUint32(out, a.Timestamp)
	out = append(out, a.Signature[:]...)
	return append(out, app...)
}

// utf8Prefix returns the longest prefix of s that is at most n bytes and
// does not split a UTF-8 sequence.
func utf8Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
