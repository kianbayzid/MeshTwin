// Package ingest turns observer MQTT messages into Parquet rows matching
// /schema/schema.json, buffers them per partition and writes files to object
// storage.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"meshtwin/ingest/internal/meshcore"
	"meshtwin/ingest/internal/observer"
	"meshtwin/ingest/internal/replay"
)

// SchemaVersion is written into every row. Bump it with schema/schema.json.
const SchemaVersion = 1

// Reception is one row of the receptions table. Field order and tags must
// match schema/schema.json; TestSchemaMatchesJSON enforces it.
type Reception struct {
	SchemaVersion  int16     `parquet:"schema_version"`
	ReceptionID    [16]byte  `parquet:"reception_id"`
	PacketHash     [8]byte   `parquet:"packet_hash"`
	ObserverID     string    `parquet:"observer_id,dict"`
	Region         string    `parquet:"region,dict"`
	RxTime         time.Time `parquet:"rx_time,timestamp(microsecond:utc)"`
	RouteType      int8      `parquet:"route_type"`
	PayloadType    int8      `parquet:"payload_type"`
	PayloadVersion int8      `parquet:"payload_version"`
	PathHashSize   int8      `parquet:"path_hash_size"`
	HopCount       int8      `parquet:"hop_count"`
	PathHops       []string  `parquet:"path_hops,list" parquet-element:",dict"`
	TraceSNRDB     []float32 `parquet:"trace_snr_db,optional,list"`
	SNRDB          *float32  `parquet:"snr_db,optional"`
	RSSIDBm        *int16    `parquet:"rssi_dbm,optional"`
	PayloadLen     int16     `parquet:"payload_len"`
	SrcID          *string   `parquet:"src_id,optional"`
	DstID          *string   `parquet:"dst_id,optional"`
}

// Advert is one row of the adverts table.
type Advert struct {
	SchemaVersion int16     `parquet:"schema_version"`
	ReceptionID   [16]byte  `parquet:"reception_id"`
	PacketHash    [8]byte   `parquet:"packet_hash"`
	ObserverID    string    `parquet:"observer_id,dict"`
	Region        string    `parquet:"region,dict"`
	RxTime        time.Time `parquet:"rx_time,timestamp(microsecond:utc)"`
	PubKey        string    `parquet:"pubkey,dict"`
	Name          *string   `parquet:"name,optional"`
	Role          int8      `parquet:"role"`
	AdvertTime    time.Time `parquet:"advert_time,timestamp(millisecond:utc)"`
	Lat           *float64  `parquet:"lat,optional"` // exact: private bucket only (see CLAUDE.md, Privacy)
	Lon           *float64  `parquet:"lon,optional"`
}

// Outcome classifies a message that produced no rows.
var (
	ErrNotPacket = errors.New("not an rx packet message") // status, tx, other topics: expected, not an error
	ErrBadTopic  = errors.New("topic is not meshcore/{IATA}/{KEY}/packets")
	ErrBadJSON   = errors.New("malformed message JSON")
	ErrDecode    = errors.New("packet does not decode")
)

// Result is what one message turns into.
type Result struct {
	Reception    Reception
	Advert       *Advert // non-nil for ADVERT packets with a parseable payload
	HashMismatch bool    // observer's "hash" field disagrees with ours (tracks an UNVERIFIED claim)
}

// Convert turns one MQTT message into rows. The packet bytes in "raw" are
// the source of truth; the JSON's redundant fields are ignored.
func Convert(rec replay.Record) (Result, error) {
	region, observerID, ok := parseTopic(rec.Topic)
	if !ok {
		return Result{}, ErrBadTopic
	}
	var msg observer.Message
	if err := json.Unmarshal(rec.Payload, &msg); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrBadJSON, err)
	}
	if msg.Type != "PACKET" || msg.Direction != "rx" {
		return Result{}, ErrNotPacket
	}
	raw, err := hex.DecodeString(msg.Raw)
	if err != nil {
		return Result{}, fmt.Errorf("%w: raw is not hex", ErrDecode)
	}
	p, err := meshcore.Decode(raw)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrDecode, err)
	}

	//nolint:gosec // G115: the decoder bounds every narrowed field (route 2 bits, type 4, version 2, hash size 1-3, hops 0-63, payload <= 184 bytes).
	r := Reception{
		SchemaVersion:  SchemaVersion,
		ReceptionID:    receptionID(observerID, msg.Timestamp, raw),
		PacketHash:     meshcore.Hash(p),
		ObserverID:     observerID,
		Region:         region,
		RxTime:         rxTime(msg.Timestamp, rec.RecvTime),
		RouteType:      int8(p.Route),
		PayloadType:    int8(p.Type),
		PayloadVersion: int8(p.Version),
		PathHashSize:   int8(p.PathHashSize),
		HopCount:       int8(p.HopCount()),
		PathHops:       []string{},
		TraceSNRDB:     p.TraceSNRs(),
		SNRDB:          parseFloat32(msg.SNR),
		RSSIDBm:        parseInt16(msg.RSSI),
		PayloadLen:     int16(len(p.Payload)),
	}
	if r.TraceSNRDB == nil {
		for _, h := range p.Hops() {
			r.PathHops = append(r.PathHops, strings.ToUpper(hex.EncodeToString(h)))
		}
	}
	src, dst := p.Endpoints()
	r.SrcID, r.DstID = hexOrNil(src), hexOrNil(dst)

	res := Result{
		Reception:    r,
		HashMismatch: msg.Hash != "" && !strings.EqualFold(msg.Hash, hex.EncodeToString(r.PacketHash[:])),
	}
	if p.Type == meshcore.PayloadAdvert {
		if a, err := meshcore.ParseAdvert(p.Payload); err == nil {
			res.Advert = advertRow(r, a)
		}
	}
	return res, nil
}

func advertRow(r Reception, a meshcore.Advert) *Advert {
	//nolint:gosec // G115: Role is the low 4 bits of the advert flags byte.
	row := &Advert{
		SchemaVersion: SchemaVersion, ReceptionID: r.ReceptionID, PacketHash: r.PacketHash,
		ObserverID: r.ObserverID, Region: r.Region, RxTime: r.RxTime,
		PubKey: strings.ToUpper(hex.EncodeToString(a.PubKey[:])), Role: int8(a.Role),
		AdvertTime: time.Unix(int64(a.Timestamp), 0).UTC(),
	}
	if a.Name != "" {
		row.Name = &a.Name
	}
	if a.HasLocation {
		lat, lon := a.Lat(), a.Lon()
		row.Lat, row.Lon = &lat, &lon
	}
	return row
}

// parseTopic splits meshcore/{IATA}/{KEY}/packets. The topic, not the JSON
// origin_id, identifies the observer: brokers like LetsMesh enforce that the
// topic key matches the authenticated publisher.
func parseTopic(topic string) (region, observerID string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "meshcore" || parts[3] != "packets" {
		return "", "", false
	}
	key := strings.ToUpper(parts[2])
	if !validRegion(parts[1]) || len(key) != 2*meshcore.PubKeySize {
		return "", "", false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", "", false
	}
	return strings.ToUpper(parts[1]), key, true
}

// validRegion accepts 2-8 ASCII letters or digits (IATA codes, or LetsMesh's
// "test"). The region becomes part of object keys, so this is a trust boundary:
// it blocks separators, "..", and ':' (a Windows alternate data stream).
func validRegion(r string) bool {
	if len(r) < 2 || len(r) > 8 {
		return false
	}
	for _, c := range r {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// receptionID identifies one reception event: an MQTT redelivery or a buffer
// resync of the same message yields the same id; two receptions of the same
// bytes differ by observer timestamp. NUL separators keep fields unambiguous.
func receptionID(observerID, timestamp string, raw []byte) [16]byte {
	h := sha256.New()
	h.Write([]byte(observerID))
	h.Write([]byte{0})
	h.Write([]byte(timestamp))
	h.Write([]byte{0})
	h.Write(raw)
	var id [16]byte
	copy(id[:], h.Sum(nil))
	return id
}

// rxTime uses the observer's timestamp when it carries a UTC offset, and the
// receive time otherwise: current meshcoretomqtt sends one, older versions did
// not, and guessing the observer's timezone would silently shift data by hours.
func rxTime(ts string, recv time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.UTC()
	}
	return recv.UTC()
}

// parseFinite parses a broker-supplied number; junk and the "NaN"/"Inf"
// spellings ParseFloat accepts become missing, never a stored non-finite value.
func parseFinite(s string, bitSize int) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), bitSize)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func parseFloat32(s string) *float32 {
	f, ok := parseFinite(s, 32)
	if !ok {
		return nil
	}
	v := float32(f)
	return &v
}

func parseInt16(s string) *int16 {
	f, ok := parseFinite(s, 64) // tolerate "-93.0"
	if !ok || f < -32768 || f > 32767 {
		return nil
	}
	v := int16(f)
	return &v
}

func hexOrNil(b []byte) *string {
	if b == nil {
		return nil
	}
	s := strings.ToUpper(hex.EncodeToString(b))
	return &s
}
