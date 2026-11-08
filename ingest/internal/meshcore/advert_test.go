package meshcore

import (
	"errors"
	"strings"
	"testing"
)

func TestAdvertRoundTrip(t *testing.T) {
	in := Advert{
		Timestamp: 1_762_300_000, Role: RoleRepeater,
		HasLocation: true, LatE6: 47_606_200, LonE6: -122_332_100,
		Feat1: 7, Name: "Capitol Hill RPT",
	}
	in.PubKey[0], in.PubKey[31] = 0xAB, 0xCD
	in.Signature[0] = 0x99

	out, err := ParseAdvert(AdvertPayload(in))
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round trip:\n got  %+v\n want %+v", out, in)
	}
	if out.Lat() != 47.6062 || out.Lon() != -122.3321 {
		t.Errorf("lat/lon = %v,%v", out.Lat(), out.Lon())
	}
}

func TestAdvertNoOptionalFields(t *testing.T) {
	out, err := ParseAdvert(AdvertPayload(Advert{Role: RoleChat}))
	if err != nil {
		t.Fatal(err)
	}
	if out.HasLocation || out.Name != "" || out.Role != RoleChat {
		t.Errorf("got %+v", out)
	}
}

func TestAdvertNameTruncatedLikeFirmware(t *testing.T) {
	// flags(1) + latlon(8) leaves 23 bytes; "é" is 2 bytes, so 12 of them (24 bytes)
	// must be cut to 11 (22 bytes) rather than split a character.
	in := Advert{HasLocation: true, Name: strings.Repeat("é", 12)}
	out, err := ParseAdvert(AdvertPayload(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != strings.Repeat("é", 11) {
		t.Errorf("name = %q", out.Name)
	}
}

func TestAdvertMalformed(t *testing.T) {
	full := AdvertPayload(Advert{HasLocation: true, Name: "x"})
	cases := map[string][]byte{
		"empty":          nil,
		"no appdata":     full[:advertFixedSize],
		"latlon cut off": full[:advertFixedSize+5],
	}
	for name, b := range cases {
		if _, err := ParseAdvert(b); !errors.Is(err, ErrBadAdvert) {
			t.Errorf("%s: err = %v, want ErrBadAdvert", name, err)
		}
	}
}

func TestAdvertIgnoresBytesPastMax(t *testing.T) {
	// The firmware clamps appdata to 32 bytes; trailing bytes must not leak into the name.
	b := AdvertPayload(Advert{Name: strings.Repeat("n", 31)})
	b = append(b, "EXTRA"...)
	out, err := ParseAdvert(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != strings.Repeat("n", 31) {
		t.Errorf("name = %q", out.Name)
	}
}
