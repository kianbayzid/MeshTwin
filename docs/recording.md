# Recording a real capture

Real traffic is the only way to settle the **UNVERIFIED** items in `protocol.md` §6.
This page describes the flow from radio to committed test file.

```
observer ──MQTT──▶ ingester (RECORD_FILE) ──▶ testdata/recorded/raw/*.ndjson   (private, git-ignored)
                                                   │
                                          anonymize │
                                                   ▼
                                      testdata/recorded/*.ndjson   (safe to commit)
                                                   │
                                   go test ./...  ─┘  (TestRecordedCaptures runs on these)
```

## 1. Choose a setting

Both settings live in `deploy/compose/`. Copy `.env.example` to `.env` first.

| | Setting 1: own observer | Setting 2: public broker |
|---|---|---|
| profile | `own-observer` | `public-broker` |
| settings file | `own-observer.env` | `public-broker.env` |
| credentials (`.env`) | `OBSERVER_MQTT_USERNAME/PASSWORD`: you choose them | `PUBLIC_MQTT_USERNAME/PASSWORD`: issued by the broker operator |
| broker | bundled mosquitto on port 1883, or your own (set `MQTT_URL`) | LetsMesh `wss://mqtt-us-v1.letsmesh.net:443/` (or MeshMapper) |
| what you need first | a MeshCore node running observer software | a **subscribe-only, full-access (role 2)** account; role 3 strips SNR/RSSI |

**Setting 1:** point your observer at this machine.
- meshcoretomqtt: add a broker with `server = <this machine's LAN IP>`, `port = 1883`, `transport = tcp`, and the username/password from `.env`. Keep the topic template `meshcore/{IATA}/{PUBLIC_KEY}/packets` with IATA `SEA`.
- The MQTT observer firmware or meshcore-ha take the same four values in their broker settings.

**Setting 2:** ask the operator. For LetsMesh, the forum
(`forum.letsmesh.net`, packet-analyzer tag) or the MeshCore Discord thread linked on
`analyzer.letsmesh.net/about`. Say what you'll do with the data: research, metadata only,
coordinates snapped to 1 km, nothing republished. Also check that the local community is OK
with it (see `data-sources.md`).

## 2. Record

```sh
cd deploy/compose
RECORD_FILE=/records/sea-$(date +%F).ndjson docker compose --profile own-observer up --build
# or:  ... --profile public-broker up --build
```
The ingester writes Parquet as usual and also appends every raw message to
`testdata/recorded/raw/` (file mode 0600). 30–60 minutes is plenty.
Watch `http://localhost:8080/metrics`: `meshtwin_ingest_messages_total{result="ok"}` should climb.
Stop with Ctrl-C. Buffered rows are flushed before exit.

> Linux hosts: the container runs as uid 65532, so first run `chmod 0777 testdata/recorded/raw`
> (or `chown 65532`). Docker Desktop doesn't need this.

## 3. Anonymize and check

```sh
cd ingest
go run ./cmd/anonymize -in ../testdata/recorded/raw/sea-2026-11-08.ndjson -out ../testdata/recorded/sea-anon.ndjson
# prints kept=… dropped=… hash_mismatches_in_original=…
go test -v -run RecordedCaptures ./internal/ingest
```

`hash_mismatches_in_original=0` confirms that the observer `hash` field is `calculatePacketHash`
(protocol.md §6). The anonymizer preserves mismatches, so the test still catches them on the
anonymized file.

What the anonymizer changes (`internal/anonymize`), with a fresh random key each run that is never stored:

| field | treatment | why |
|---|---|---|
| public keys (topic, adverts, ANON_REQ) | first 3 bytes through a secret byte permutation, rest keyed-hashed | hop hashes stay consistent with keys, and **collisions are preserved exactly** |
| hop hashes, src/dst hashes | same permutation | same |
| names | `obs-XXXX`, `node-XXXX` | fixed format, so name length can't fingerprint |
| coordinates | secret shift (≤ ~2 km), then snapped to a 0.01° grid | ≈ 1 km privacy standard |
| ciphertext, signatures, ACK codes | keyed pseudorandom bytes, same length | content gone; identical packets still map identically, so grouping receptions by packet survives |
| status / tx messages | dropped | carry device model, firmware, battery |
| time, SNR, RSSI, path length, payload type | **unchanged** | that is the analytic signal |

Before committing, skim the output: `head -c 600 testdata/recorded/sea-anon.ndjson`.
Only the anonymized file is committed. `raw/` is git-ignored.
