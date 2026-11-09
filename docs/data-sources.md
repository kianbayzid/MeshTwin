# Data sources

Every external source MeshTwin reads, its terms, and how we behave toward it.
**Rule:** don't connect to a source until its row here says the terms are checked.

## Packet feeds (MQTT)

Two live settings, both in `deploy/compose/` (how-to: `docs/recording.md`):

| source | setting | access | terms | status |
|---|---|---|---|---|
| **Your own observer** (meshcoretomqtt / MQTT observer firmware / meshcore-ha → your broker) | 1 `own-observer` | full | yours | ✅ ready |
| **LetsMesh** `wss://mqtt-us-v1.letsmesh.net:443/` | 2 `public-broker` | observers **publish** with a node-key-signed JWT (username `v1_<PUBKEY>`); **reading needs a subscribe-only account created by the operator**. Roles: 1 admin, 2 full access, 3 limited (**SNR/RSSI/score stripped**). We need role 2. Source: [meshcore-mqtt-broker README](https://github.com/michaelhart/meshcore-mqtt-broker) (MIT), checked 2026-11-08 | The software is MIT. The data is collected from volunteer observers, and the site says it is "free, ad-free, and commercial data exploitation free". No written data-use terms were found, so **ask the operator** and describe the use | ⏳ needs an account from the operator |
| **MeshMapper** `wss://mqtt.meshmapper.net:443/` | 2 `public-broker` (change `MQTT_URL`) | observers publish with device signing. No third-party read access documented; region admins configure brokers. Source: [MeshMapper wiki](https://wiki.meshmapper.net/mqtt-main/), checked 2026-11-08 | none published | ⏳ ask region admins |
| **Recorded capture** (`testdata/recorded/`) | replay | local | anonymized before commit | ⏳ waiting for a recording |
| **Synthetic** (`ingest/cmd/meshgen`) | unit tests and CI only | local | fake data | ✅ |

No public broker has open read access. Connecting needs credentials an operator chose to give,
which is itself the consent step.

Community context: packet analyzers aren't universally welcome. For example, an Arizona MeshCore
community [banned an MQTT analyzer](https://www.reddit.com/r/meshcore/comments/1pu19ad/arizona_meshcore_bans_mqtt_analyzer/).
Before reading a community's feed, check that the broker's terms allow third-party reading
**and** that the local community is OK with it.

### Politeness policy (enforced in `ingest/internal/source/mqtt.go`)
- **Subscribe only.** Never publish to a broker we don't own.
- One connection per broker, subscribed to a region-filtered topic (`meshcore/SEA/+/packets`), never `#`.
- QoS 0 (no broker-side queueing on our behalf).
- Reconnect with exponential backoff (paho: 1 s doubling to a 5 min cap; retry every 5 s on first connect).
- Client id `meshtwin-<random>`, so operators can identify us. Credentials come from a Kubernetes Secret.
- Per-source message-rate cap (`MQTT_MAX_PER_SEC`) and a 16 KB message-size cap. Dropped messages are counted in `meshtwin_ingest_messages_total{result="rate_limited"|"oversize"}`.

## Terrain (Phase 4)

| source | terms | status |
|---|---|---|
| Copernicus DEM GLO-30 | free incl. commercial use, attribution required. **UNVERIFIED:** exact attribution text; read the licence PDF on the Copernicus DEM page before redistributing tiles or derived products | ⏳ Phase 4 |
| SRTM 1 arc-second (NASA/USGS) | public domain (US government work) | ⏳ Phase 4 (fallback) |

## Privacy rules that apply to all sources
- Metadata only. Payloads are not decrypted and not stored.
- Advert locations are stored exactly in private storage only. Any output is snapped to ~1 km.
- Recorded captures are anonymized (keys remapped, names replaced, coordinates shifted and snapped)
  before they enter the repo.

## Software supply chain

| component | source | note |
|---|---|---|
| MinIO server | `pgsty/minio:RELEASE.2026-08-04T00-00-00Z` (community rebuild, 3.7M pulls) | MinIO stopped publishing community images in 2025 (`minio/minio` is gone from Docker Hub, and Quay now requires login). That makes this a **third-party image of our storage layer**. Mitigations: pinned release (digest in Phase 6), Trivy scan in CI (Phase 8), and the code only speaks plain S3, so we can swap in SeaweedFS, Garage, or an image built from source. |
| Mosquitto | `eclipse-mosquitto:2.0.22` (official) | Setting 1 only |
| Go base images | `golang:1.27.3-bookworm`, `gcr.io/distroless/static-debian12:nonroot` (official) | |
