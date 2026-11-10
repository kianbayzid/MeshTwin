# MeshCore protocol notes

What MeshTwin relies on about MeshCore packets and the observer MQTT feed. Every
claim cites its source. Anything not confirmed in source is marked **UNVERIFIED**,
with where to check it.

**Firmware source:** [`meshcore-dev/MeshCore`](https://github.com/meshcore-dev/MeshCore) at commit
[`81dbae5`](https://github.com/meshcore-dev/MeshCore/tree/81dbae592ffad40b4212bc0b37e82b38da08f4bc) (read 2026-11-04).
Files: `src/Packet.h`, `src/Packet.cpp`, `src/Mesh.cpp`, `src/MeshCore.h`, `src/Identity.h`,
`src/helpers/AdvertDataHelpers.{h,cpp}`.
Our implementation is `ingest/internal/meshcore`, and its tests encode every rule below.

---

## 1. Wire format

From `Packet::writeTo` / `Packet::readFrom` (`Packet.cpp`):

```
+--------+---------------------+----------+----------------+-------------+
| header | transport_codes     | path_len | path           | payload     |
| 1 B    | 4 B, only route 0/3 | 1 B      | count*size B   | the rest    |
+--------+---------------------+----------+----------------+-------------+
```

No length field and no CRC: the radio layer delivers the frame length, and the
payload is "everything after the path". Max frame is `MAX_TRANS_UNIT` = 255 B.

### header (1 byte), `Packet.h`

| bits | field | values |
|---|---|---|
| 0–1 | route type | 0 `TRANSPORT_FLOOD`, 1 `FLOOD`, 2 `DIRECT`, 3 `TRANSPORT_DIRECT` |
| 2–5 | payload type | 0 REQ, 1 RESPONSE, 2 TXT_MSG, 3 ACK, 4 ADVERT, 5 GRP_TXT, 6 GRP_DATA, 7 ANON_REQ, 8 PATH, 9 TRACE, 10 MULTIPART, 11 CONTROL, 15 RAW_CUSTOM |
| 6–7 | payload version | 0 = V1 (1-byte src/dest hashes, 2-byte MAC); 1–3 reserved |

`0xFF` as a header means "do not retransmit". It's used only inside a node and never sent.

### transport codes (4 bytes, route types 0 and 3 only)
Two `uint16`. Their meaning (region/scope filtering) lives in the higher layers. MeshTwin stores
neither code. **UNVERIFIED:** semantics; see `allowPacketForward` overrides in `examples/`.

### path_len (1 byte), `Packet.h` `getPathHashSize/Count`

| bits | meaning |
|---|---|
| 0–5 | hop count (0–63) |
| 6–7 | hash size − 1 → **1, 2 or 3 bytes per hop**; 4 is reserved and rejected (`isValidPathLen`) |

Path bytes = count × size and must be ≤ `MAX_PATH_SIZE` = 64. So a 3-byte path holds at most 21 hops.

### payload
At least 1 byte, at most `MAX_PACKET_PAYLOAD` = 184.

### Byte order
Multi-byte integers are copied with `memcpy` from native structs. Every MeshCore target
(ESP32, nRF52, RP2040, STM32) is little-endian, so the wire is **little-endian**. This is an
inference from the code rather than a stated spec. The decode tests and the real
capture (Phase 2) confirm it.

---

## 2. What the path means (this drives the topology analysis)

A hop entry is **the first 1–3 bytes of the relaying node's public key** (`Identity::copyHashTo`:
"hash is just prefix of pub_key"). It is *not* a cryptographic hash, and it *collides*.

| route | path semantics (source) | what an observer learns |
|---|---|---|
| **flood** | each repeater that forwards **appends** its own hash (`Mesh::routeRecvPacket`) | the actual relay chain, oldest first. The observer heard it **from the last entry** (or directly from the origin if the path is empty). |
| **direct** | path is the hops **still to go**; each hop **removes** itself before forwarding (`Mesh::removeSelfFromPath`) | only the *planned* rest of the route. The node it was heard from is **not** in the path. |
| **direct TRACE** | path bytes are **SNR samples**, `int8(snr×4)`, one per hop crossed (`Mesh::onRecvPacket`, "append SNR (Not hash!)"). The hop hashes sit at the end of the payload. | per-hop SNR, which is useful for the Phase 4 comparison |

Consequences for MeshTwin:
- **Only flood packets give traversed links.** For a flood reception with path `[h1..hk]` heard
  by observer O, the implied links are `h1–h2, …, h(k-1)–hk, hk–O`, plus `origin–h1` when the
  origin is known (ADVERT, ANON_REQ). For direct packets we treat path[0] only as a weak hint
  that some node believed it had a link to path[0].
- **By default only repeaters forward floods** (each example's `allowPacketForward` override, commit `81dbae5`, read 2026-11-08):

  | node type | forwards by default? | source |
  |---|---|---|
  | repeater | **yes**, unless over the hop limit (`flood_max`; adverts `flood_max_advert = 8`), wrong region scope, or loop-detected | `examples/simple_repeater/MyMesh.cpp`, `src/helpers/CommonCLI.h` (`disable_fwd = 0`) |
  | room server | no (`_prefs.disable_fwd = 1`); `set repeat on` enables it | `examples/simple_room_server/MyMesh.cpp`, `CommonCLI.cpp` |
  | sensor | no (`disable_fwd = true`) | `examples/simple_sensor/SensorMesh.cpp` |
  | companion | no (`setRepeatEn(false)`); "client repeat" is only allowed on 433.000 / 869.495 / 918.000 MHz | `examples/companion_radio/MyMesh.cpp` |

  So a hop hash is most likely a repeater, but the others can't be ruled out. Phase 3 hop resolution prefers
  repeater candidates and falls back to other roles at a lower prior.
- **Advert cadence decides how long it takes to learn the network.** Repeaters and room servers send a **flood**
  advert every **47 h** by default (`flood_advert_interval`, settable 3–168 h). Their zero-hop advert (heard one hop
  away only) runs every 2 min on a factory-fresh device and is switched **off** once the device is configured, unless
  set to 60–240 min (`CommonCLI.cpp`, `MIN_LOCAL_ADVERT_INTERVAL 60`). Companions advertise only when a user asks.
  → Node positions and roles need a lookback of **≥ 48 h, and 7 days to be safe**, longer than the link window.
- **Path hash width is chosen by the originator** (`path_hash_mode`, default **0 = 1 byte**, max 2 = 3 bytes), and
  every forwarding repeater appends at the packet's existing width (`Mesh::routeRecvPacket`). Expect mostly 1-byte
  paths, so collisions are the normal case, and mixed widths across packets.
- **Echoes:** an observer that is also a repeater hears its own relayed copy come back from a
  neighbor, so its own hash appears in the path. Real example from our synthetic sample:
  observer SEA-RPT-01 (key `35…`) received an ADVERT with path `[35, 34]`. RPT-01 relayed it,
  then RPT-03 (`34…`) relayed it again, and RPT-01 heard that. Analytics must not
  record that as a self-loop.

### Hash collisions
With 1-byte hashes (the legacy default, and the only option in firmware older than multi-byte paths)
there are only 256 values. By the birthday bound, a region with just 20 repeaters has a ≈52%
chance that two of them share a hash. With 90 nodes in our synthetic Seattle network, 1-byte
paths contained **4,677 ambiguous hop entries in 4,337 receptions** (`go test -v -run Collisions
./internal/synth`). MeshTwin therefore:
1. never maps a hop hash straight to a node, and stores hops as raw hex prefixes;
2. resolves hops in Phase 3 by combining candidate nodes with geographic plausibility, path context,
   and how consistently a node is observed, and reports a confidence per inferred link.

---

## 3. Packet identity (dedup and join key)

`Packet::calculatePacketHash`: `SHA-256(payload_type ‖ payload)` truncated to `MAX_HASH_SIZE` = 8 bytes.
TRACE packets also mix in `path_len` (stored as a `uint16` in the struct, so 2 LE bytes).

**The path is excluded**, so every copy of one transmission, relayed by any repeaters and heard
by any observers, has the same hash. That makes it the natural key to join the N receptions
of one packet.

It is **not** unique per reception: one observer can hear the same packet several times via
different repeaters (our generator does this, and we expect real observers do too). See
`reception_id` in `/schema/schema.md` for the per-row key.

**UNVERIFIED:** that the observer JSON's `hash` field equals this calculation. Verify in Phase 2 by
recomputing it over the recorded capture.

---

## 4. Payload layouts we read (plaintext fields only)

MeshTwin never decrypts anything. For V1 payloads (`Mesh.cpp`), these fields are plaintext:

| type | plaintext prefix | rest |
|---|---|---|
| REQ, RESPONSE, TXT_MSG, PATH | `dest_hash(1) src_hash(1)` | MAC(2) + ciphertext |
| ANON_REQ | `dest_hash(1) sender_pubkey(32)` | MAC(2) + ciphertext |
| GRP_TXT, GRP_DATA | `channel_hash(1)` (not a node) | MAC(2) + ciphertext |
| ACK | `ack_crc(4)` | – |
| TRACE | `tag(4) auth(4) flags(1)` + hop hashes | `flags & 3` = log2(hash size), v1.11+ |
| ADVERT | **entire payload is plaintext** (signed, not encrypted) | see below |

`src_hash` / `dest_hash` are 1-byte public-key prefixes (`PATH_HASH_SIZE` = 1), so they are just
as ambiguous as hop hashes.

### ADVERT (`Mesh::createAdvert`, `AdvertDataHelpers.cpp`)

```
pubkey(32) | timestamp(4, LE, sender clock) | signature(64, Ed25519) | appdata(≤32)
appdata = flags(1) | [lat int32 LE ×1e6][lon int32 LE ×1e6] | [feat1 u16] | [feat2 u16] | [name: rest]
flags   = role (bits 0–3: 1 chat, 2 repeater, 3 room, 4 sensor) | 0x10 latlon | 0x20 feat1 | 0x40 feat2 | 0x80 name
```
The firmware ignores appdata past 32 bytes and truncates names to a valid UTF-8 prefix. Adverts are
the **only** source of node positions, and those positions are often people's homes (see Privacy
in `CLAUDE.md`). We do not verify signatures yet (a shortcut noted in code).

### Worked example (from `testdata/synthetic/sea-seed1-10m.ndjson`)

```
11 02 35 34 0562C16B…22F92061 3981E66A E7F2512B…(64 B) 91 906ED502 B2B4B8F8 5345412D434D502D3032
```
| bytes | decode |
|---|---|
| `11` | `0b00_0100_01`: version 0, type 4 ADVERT, route 1 FLOOD |
| `02` | hash size 1, 2 hops |
| `35 34` | relayed by `35…` then `34…` |
| `0562…2061` | origin public key (32 B) |
| `3981E66A` | timestamp LE = 0x6AE68139 = 1793491257 |
| 64 B | signature |
| `91` | flags: name + latlon, role 1 (chat) |
| `906ED502` | lat LE = 0x02D56E90 = 47541904 → 47.541904 |
| `B2B4B8F8` | lon LE = 0xF8B8B4B2 = −122112846 → −122.112846 |
| `5345…3032` | name "SEA-CMP-02" |

---

## 5. Observer MQTT feed

Sources: [`Cisien/meshcoretomqtt`](https://github.com/Cisien/meshcoretomqtt) README and the
[agessaman MQTT observer firmware docs](https://observer.gessaman.com/docs.html).

**Topics:** `meshcore/{IATA}/{OBSERVER_PUBKEY}/packets`, plus `/status` (and `/debug` in meshcoretomqtt).
IATA is a 3-letter airport code standing in for the region. That gives a natural region filter:
subscribe to `meshcore/SEA/+/packets`.

**Packet message** (one per reception):
```json
{"origin":"ag loft rpt","origin_id":"A1B2…","timestamp":"2025-03-16T00:07:11.191561",
 "type":"PACKET","direction":"rx","time":"00:07:09","date":"16/3/2025","len":"87",
 "packet_type":"5","route":"F","payload_len":"83","raw":"0A1B2C…","SNR":"4","RSSI":"-93",
 "score":"1000","hash":"AC9D2DDDD8395712","path":"C2 -> E2"}
```

Quirks the ingester must handle:
- **All numbers are strings.**
- **`timestamp` format varies:** meshcoretomqtt omits the UTC offset (Python `isoformat()` of
  probably-local time, **UNVERIFIED**); the agessaman firmware sends `+00:00`. Phase 2 policy: use the
  observer timestamp if it has an offset, otherwise fall back to the ingester's receive time.
- **`path` varies:** a `"C2 -> E2"` string (meshcoretomqtt) or a `["c2","e2"]` array (agessaman), and it
  appears only on direct-route packets. → **We ignore it and decode `raw`.**
- `route` letters: `F` = flood **and transport flood**, `D` = direct **and transport direct**; a `D` packet
  also carries `path` ([meshcore-ha MQTT docs](https://meshcore-dev.github.io/meshcore-ha/docs/ha/mqtt/), checked 2026-11-08).
  We still derive the route from `raw`, because the letter loses the transport distinction.
- Some tools add fields (meshcore-ha sends `duration`). Unknown fields are ignored.
- `SNR`/`RSSI`/`score` appear on rx only. SNR is the **last hop only** (the radio measures only
  the final transmission). `score` is a rebroadcast score ×1000. We don't use it.
- `direction: "tx"` messages are the observer's own transmissions. They are dropped at ingest
  because they carry no reception data.
- **Duplicate receptions depend on the observer software.** meshcore-ha documents "duplicates within
  1 second are dropped". **UNVERIFIED** for meshcoretomqtt and the observer firmware: count
  `(hash, observer)` repeats in the capture. Either way, the schema handles both (`reception_id`).

---

## 6. Open items to verify (tracked)

| item | where to check | when |
|---|---|---|
| Observer `hash` == `calculatePacketHash` | `anonymize` prints `hash_mismatches_in_original`; `TestRecordedCaptures` asserts 0 | when a capture exists |
| Observers publish duplicates (meshcore-ha: no, within 1 s) | count `(hash, observer)` repeats in the capture | when a capture exists |
| Timestamp timezone in meshcoretomqtt | its source, `datetime.now()` call (handled either way: no offset → receive time) | when a capture exists |
| ~~Room servers forward floods?~~ **Resolved 2026-11-08:** not by default (see §2) | `examples/simple_room_server` | done |
| ~~US default radio preset~~ **Resolved 2026-11-08:** 910.525 MHz, BW 62.5 kHz, SF7, CR5 (official preset API `api.meshcore.nz/api/v1/config`, `docs/faq.md` §2.3; Puget Mesh uses the same). The **build** default is EU (869.618 MHz, SF8, `platformio.ini`): US radios get the preset from the app/flasher, so never infer frequency from firmware. TX 22 dBm on Heltec V3 / RAK4631 (`variants/*/platformio.ini`), boosted RX gain on. SX1262 demod SNR floor SF7 -7.5 dB ... SF12 -20 dB (datasheet Rev 1.2 Table 6-1); NF about 6-6.5 dB, derived from Table 3-8 sensitivity (not stated). Antenna gain and cable loss are not in firmware: **UNVERIFIED** per site. | preset API, faq.md, SX1262 datasheet | done |
| Transport code semantics | `examples/*` `allowPacketForward` | only if needed |
