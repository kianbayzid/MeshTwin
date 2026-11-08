# Data sources

Every external source MeshTwin reads, its terms, and how we behave toward it.
**Rule:** don't connect to a source until its row here says the terms are checked.

## Packet feeds (MQTT)

| source | access | terms | status |
|---|---|---|---|
| **Your own observer** (meshcoretomqtt or the agessaman observer firmware → your own broker) | full | yours | ✅ preferred for live mode |
| **Synthetic** (`ingest/cmd/meshgen`) | local | n/a: fake data | ✅ default for tests and CI |
| **Recorded capture** (`testdata/`) | local | anonymized before commit (Phase 2) | ⏳ Phase 2 |
| LetsMesh community broker (`letsmesh.net`) | **UNVERIFIED:** observers authenticate to publish; read access for third parties unclear | **not yet checked** | ❌ do not connect |
| MeshMapper broker ([wiki](https://wiki.meshmapper.net/mqtt-main/)) | **UNVERIFIED** | **not yet checked** | ❌ do not connect |

Community context: packet analyzers aren't universally welcome. For example, an Arizona MeshCore
community [banned an MQTT analyzer](https://www.reddit.com/r/meshcore/comments/1pu19ad/arizona_meshcore_bans_mqtt_analyzer/).
Before reading a community's feed, check that the broker's terms allow third-party reading
**and** that the local community is OK with it.

### Politeness policy (enforced in the ingester, Phase 2)
- **Subscribe only.** Never publish to a broker we don't own.
- One connection per broker, subscribed to a region-filtered topic (`meshcore/SEA/+/packets`), never `#`.
- QoS 0 (no broker-side queueing on our behalf).
- Reconnect with exponential backoff and jitter: 5 s → 5 min cap.
- Client id `meshtwin-<random>`, so operators can identify us. Credentials come from a Kubernetes Secret.
- Optional per-source message-rate cap. Messages over the cap are dropped and counted in a metric.

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
