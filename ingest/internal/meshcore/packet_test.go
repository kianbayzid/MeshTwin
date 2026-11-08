package meshcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Golden packets built by hand from the layout in Packet.cpp; spaces separate fields.
var goldens = []struct {
	name     string
	hex      string
	route    uint8
	typ      PayloadType
	codes    [2]uint16
	hashSize int
	hops     []string
	payload  string
}{
	{
		name:  "flood GRP_TXT, 1-byte hashes, 2 hops",
		hex:   "15 02 A1B2 11 2233" + strings.Repeat("44", 16),
		route: RouteFlood, typ: PayloadGrpTxt, hashSize: 1,
		hops:    []string{"a1", "b2"},
		payload: "112233" + strings.Repeat("44", 16),
	},
	{
		name:  "transport direct ACK, 2-byte hashes, little-endian codes",
		hex:   "0F 3412 CDAB 41 0102 DEADBEEF",
		route: RouteTransportDirect, typ: PayloadAck, codes: [2]uint16{0x1234, 0xABCD}, hashSize: 2,
		hops:    []string{"0102"},
		payload: "deadbeef",
	},
	{
		name:  "flood TXT_MSG, 3-byte hashes",
		hex:   "09 82 AABBCC DDEEFF 10 20 3031" + strings.Repeat("55", 16),
		route: RouteFlood, typ: PayloadTxtMsg, hashSize: 3,
		hops:    []string{"aabbcc", "ddeeff"},
		payload: "10203031" + strings.Repeat("55", 16),
	},
	{
		name:  "zero-hop direct ADVERT-sized payload",
		hex:   "12 00 " + strings.Repeat("77", 101),
		route: RouteDirect, typ: PayloadAdvert, hashSize: 1,
		hops:    []string{},
		payload: strings.Repeat("77", 101),
	},
}

func TestDecodeGoldens(t *testing.T) {
	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			raw := mustHex(t, g.hex)
			p, err := Decode(raw)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if p.Route != g.route || p.Type != g.typ || p.Version != 0 {
				t.Errorf("route/type/ver = %d/%v/%d, want %d/%v/0", p.Route, p.Type, p.Version, g.route, g.typ)
			}
			if p.TransportCodes != g.codes {
				t.Errorf("transport codes = %#x, want %#x", p.TransportCodes, g.codes)
			}
			if p.PathHashSize != g.hashSize {
				t.Errorf("hash size = %d, want %d", p.PathHashSize, g.hashSize)
			}
			var hops []string
			for _, h := range p.Hops() {
				hops = append(hops, hex.EncodeToString(h))
			}
			if strings.Join(hops, ",") != strings.Join(g.hops, ",") {
				t.Errorf("hops = %v, want %v", hops, g.hops)
			}
			if got := hex.EncodeToString(p.Payload); got != g.payload {
				t.Errorf("payload = %s, want %s", got, g.payload)
			}
			back, err := Encode(p)
			if err != nil || !bytes.Equal(back, raw) {
				t.Errorf("Encode round trip = %x, %v; want %x", back, err, raw)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want error
	}{
		{"empty", "", ErrTooShort},
		{"header only", "15", ErrTooShort},
		{"transport codes cut off", "14 3412", ErrTooShort},
		{"reserved hash size 4", "15 C1 AABBCCDD 00", ErrReservedHashSize},
		{"path over 64 bytes (2B x 33)", "15 61" + strings.Repeat("00", 70), ErrPathTooLong},
		{"path cut off", "15 03 AABB", ErrTooShort},
		{"no payload after path", "15 02 AABB", ErrNoPayload},
		{"payload over 184 bytes", "15 00" + strings.Repeat("00", 185), ErrPayloadTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode(mustHex(t, c.hex)); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestTraceSNRs(t *testing.T) {
	// Direct TRACE: path holds int8(snr*4) per hop: 0x28 = +10 dB, 0xF6 = -2.5 dB.
	p, err := Decode(mustHex(t, "26 02 28F6 01000000 02000000 00 AABB"))
	if err != nil {
		t.Fatal(err)
	}
	got := p.TraceSNRs()
	if len(got) != 2 || got[0] != 10 || got[1] != -2.5 {
		t.Errorf("TraceSNRs = %v, want [10 -2.5]", got)
	}
	flood, _ := Decode(mustHex(t, goldens[0].hex))
	if flood.TraceSNRs() != nil {
		t.Error("non-TRACE packet returned SNRs")
	}
}

func TestHash(t *testing.T) {
	p, _ := Decode(mustHex(t, goldens[0].hex))
	want := sha256.Sum256(append([]byte{byte(PayloadGrpTxt)}, p.Payload...))
	if h := Hash(p); !bytes.Equal(h[:], want[:8]) {
		t.Errorf("Hash = %x, want %x", h, want[:8])
	}

	// Same transmission relayed along a different path: same identity.
	relayed := p
	relayed.Path = []byte{0x01, 0x02, 0x03}
	if Hash(relayed) != Hash(p) {
		t.Error("hash changed with path; it must not for non-TRACE packets")
	}

	// TRACE includes path_len, so each hop of a trace is a distinct packet.
	tr, _ := Decode(mustHex(t, "26 02 28F6 01000000 02000000 00 AABB"))
	longer := tr
	longer.Path = append(append([]byte(nil), tr.Path...), 0x10)
	if Hash(longer) == Hash(tr) {
		t.Error("TRACE hash ignored path_len")
	}
}

func TestEndpoints(t *testing.T) {
	txt, _ := Decode(mustHex(t, goldens[2].hex))
	src, dst := txt.Endpoints()
	if !bytes.Equal(src, []byte{0x20}) || !bytes.Equal(dst, []byte{0x10}) {
		t.Errorf("TXT_MSG endpoints = %x,%x; want 20,10", src, dst)
	}
	grp, _ := Decode(mustHex(t, goldens[0].hex))
	if s, d := grp.Endpoints(); s != nil || d != nil {
		t.Error("group message must not expose endpoints")
	}
	future := txt
	future.Version = 1
	if s, _ := future.Endpoints(); s != nil {
		t.Error("unknown payload version must not be parsed")
	}
}

func TestEncodeRejectsInvalid(t *testing.T) {
	ok := Packet{Route: RouteFlood, Type: PayloadAck, PathHashSize: 1, Payload: []byte{1}}
	bad := map[string]Packet{
		"hash size 0":      {Route: RouteFlood, PathHashSize: 0, Payload: []byte{1}},
		"ragged path":      {Route: RouteFlood, PathHashSize: 2, Path: []byte{1, 2, 3}, Payload: []byte{1}},
		"empty payload":    {Route: RouteFlood, PathHashSize: 1},
		"path over 64":     {Route: RouteFlood, PathHashSize: 1, Path: make([]byte, 65), Payload: []byte{1}},
		"payload over 184": {Route: RouteFlood, PathHashSize: 1, Payload: make([]byte, 185)},
	}
	if _, err := Encode(ok); err != nil {
		t.Fatalf("valid packet rejected: %v", err)
	}
	for name, p := range bad {
		if _, err := Encode(p); err == nil {
			t.Errorf("%s: Encode accepted invalid packet", name)
		}
	}
}

// FuzzDecode: arbitrary bytes never panic, and anything that decodes
// re-encodes to exactly the same bytes.
func FuzzDecode(f *testing.F) {
	for _, g := range goldens {
		f.Add(mustHex(f, g.hex))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := Decode(raw)
		if err != nil {
			return
		}
		back, err := Encode(p)
		if err != nil {
			t.Fatalf("decoded packet failed to encode: %v", err)
		}
		if !bytes.Equal(back, raw) {
			t.Fatalf("round trip mismatch:\n in  %x\n out %x", raw, back)
		}
	})
}
