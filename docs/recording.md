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
- **A radio on USB, Windows PC** (the recommended route; `docs/protocol.md` §5): stock **companion USB** firmware on the
  radio and [`meshcore-packet-capture`](https://github.com/agessaman/meshcore-packet-capture) on the PC. Steps below.
- meshcoretomqtt (Linux/macOS/Pi only, and only with a repeater built with `-D MESH_PACKET_LOGGING=1`): add a broker
  with `server = <this machine's LAN IP>`, `port = 1883`, `transport = tcp`, `[broker.auth] method = "password"` and
  the username/password from `.env`. Keep the topic template `meshcore/{IATA}/{PUBLIC_KEY}/packets`. It publishes at
  QoS 0 whatever you set; the bundled broker queues QoS 0 for the ingester too (`queue_qos0_messages`).
- The MQTT observer firmware or meshcore-ha take the same four values in their broker settings.

**USB radio on Windows 11, step by step** (sources checked 2026-11-12; the first real run still has to confirm them):
1. Flash **stock companion USB** firmware for your board with the MeshCore web flasher, and set the US preset
   (910.525 MHz, 62.5 kHz, SF7, CR5) in the MeshCore app. Then close the app and the flasher: they hold the COM port.
2. Install Python 3.12 (`winget install Python.Python.3.12`), then in PowerShell:
   ```powershell
   git clone https://github.com/agessaman/meshcore-packet-capture; cd meshcore-packet-capture
   py -3.12 -m venv .venv; .venv\Scripts\pip install -e .
   ```
3. Find the radio's COM port: Device Manager → Ports, or
   `Get-CimInstance Win32_PnPEntity | ? Name -match 'COM\d+' | select Name`.
4. Create `.env.local` in that folder:
   ```
   PACKETCAPTURE_CONNECTION_TYPE=serial
   PACKETCAPTURE_SERIAL_PORTS=COM5
   PACKETCAPTURE_IATA=SEA
   PACKETCAPTURE_ADVERT_INTERVAL_HOURS=0
   PACKETCAPTURE_DRAIN_MESSAGES=false
   PACKETCAPTURE_MQTT1_ENABLED=true
   PACKETCAPTURE_MQTT1_SERVER=127.0.0.1
   PACKETCAPTURE_MQTT1_PORT=1883
   PACKETCAPTURE_MQTT1_TRANSPORT=tcp
   PACKETCAPTURE_MQTT1_USE_TLS=false
   PACKETCAPTURE_MQTT1_USERNAME=observer
   PACKETCAPTURE_MQTT1_PASSWORD=<broker password>
   PACKETCAPTURE_MQTT1_QOS=1
   PACKETCAPTURE_TOPIC_PACKETS=meshcore/{IATA}/{PUBLIC_KEY}/packets
   PACKETCAPTURE_TOPIC_STATUS=meshcore/{IATA}/{PUBLIC_KEY}/status
   ```
   Why these settings:
   - `ADVERT_INTERVAL_HOURS=0`: otherwise the bridge makes your radio **transmit** a flood advert with its name (and
     any location set on it) every 47 h (11 h if you used `install.ps1`).
   - `DRAIN_MESSAGES=false`: RF capture only; don't pull the companion's stored messages.
   - `QOS=1`: the broker keeps messages for the ingester while it restarts.
   - Leave payload decoding off (the default), and decline `install.ps1`'s LetsMesh, owner-key and email prompts.
5. Run it: `.venv\Scripts\python -m meshcore_packet_capture --verbose`. It stops when the PC sleeps or reboots
   (no auto-start on Windows).
   The broker password on the k3d cluster (release `meshtwin`), from Git Bash:
   `kubectl --context k3d-meshtwin -n meshtwin get secret meshtwin-mqtt -o jsonpath='{.data.password}' | base64 -d`
6. Check the feed: `meshtwin_ingest_messages_total{result="ok"}` climbs (Grafana, or the console's status strip), or
   `mosquitto_sub -h 127.0.0.1 -u observer -P <password> -t 'meshcore/SEA/+/packets' -v`.

What to expect on a fresh cluster: until the ingester writes its first Parquet file (up to 2 min after the first
packet, `flushAge`), topology, propagation and placement fail with `NoReceptionsError: no receptions for region …`
and retry on their next run; the terrain download and terrain-tiles steps skip ("no located nodes … yet", exit 0). Topology runs every 5
min, so the map shows nodes 2-8 min after they're first heard. A node appears on the map only once its advert (with a
location) has been heard; repeaters flood-advert about every 47 h, so the first hours show a partial network. The 3D
terrain arrives with the daily terrain job (or run it by hand: `kubectl --context k3d-meshtwin -n meshtwin create job --from=cronjob/meshtwin-terrain-tiles terrain-now`).

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
