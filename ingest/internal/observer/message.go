// Package observer holds the JSON message format that MeshCore observer nodes
// publish to MQTT. Field names follow Cisien/meshcoretomqtt and the agessaman
// MQTT observer firmware docs (see docs/protocol.md).
package observer

import (
	"encoding/json"
	"fmt"
)

// Message is one "packets" topic message. Numeric fields are JSON strings on
// the wire, so they are kept as strings here; the ingester parses them.
//
// The decoder treats Raw as the source of truth. Route, PacketType, Hash and
// Path duplicate what Raw already contains, and Path in particular differs
// between tools ("C2 -> E2" string vs ["c2","e2"] array) and is missing on
// flood packets, hence json.RawMessage.
type Message struct {
	Origin     string          `json:"origin"`    // observer's name
	OriginID   string          `json:"origin_id"` // observer's public key, hex
	Timestamp  string          `json:"timestamp"` // ISO 8601; some tools omit the UTC offset
	Type       string          `json:"type"`      // "PACKET"
	Direction  string          `json:"direction"` // "rx" or "tx"
	Time       string          `json:"time"`      // HH:MM:SS
	Date       string          `json:"date"`      // D/M/YYYY
	Len        string          `json:"len"`
	PacketType string          `json:"packet_type"`
	Route      string          `json:"route"`
	PayloadLen string          `json:"payload_len"`
	Raw        string          `json:"raw"` // full packet, hex
	SNR        string          `json:"SNR,omitempty"`
	RSSI       string          `json:"RSSI,omitempty"`
	Score      string          `json:"score,omitempty"`
	Hash       string          `json:"hash"`
	Path       json.RawMessage `json:"path,omitempty"`
}

// PacketsTopic is the topic an observer publishes packets on:
// meshcore/{IATA}/{OBSERVER_PUBKEY}/packets.
func PacketsTopic(iata, observerPubKeyHex string) string {
	return fmt.Sprintf("meshcore/%s/%s/packets", iata, observerPubKeyHex)
}
