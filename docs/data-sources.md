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
| Copernicus DEM GLO-30 | Licence COP-DEM-GLO-30-F ([PDF](https://docs.sentinel-hub.com/api/latest/static/files/data/dem/resources/license/License-COPDEM-30.pdf)): free; reproduction, distribution and adaptation allowed (Art. 4-5). Attribution required (below). No implied endorsement (Art. 6d); obligations pass to later users (Art. 6e). | ✅ primary |
| USGS 3DEP 1/3″ (`prd-tnm.s3.amazonaws.com/StagedProducts/Elevation/13/TIFF/current/n48w123/USGS_13_n48w123.tif`) | public domain (US Government work). Bare earth (DTM). Vertical datum NAVD88 **UNVERIFIED** (check product metadata) | fallback, not wired up |

- **Access:** AWS Open Data, no account: `https://copernicus-dem-30m.s3.amazonaws.com/Copernicus_DSM_COG_10_N47_00_W123_00_DEM/Copernicus_DSM_COG_10_N47_00_W123_00_DEM.tif`
  (tile named by its SW corner; COG, DEFLATE + floating-point predictor, float32, 3600×3600, EGM2008 heights; no nodata tag, ocean has no tile → 0 m).
  `meshtwin-analytics fetch-dem` downloads into a local cache. **Tiles are never committed.**
- **It is a surface model (DSM):** tree and building tops are included, so in forested Seattle the knife-edge model over-predicts blockage.
- **Required attribution** (Art. 6b, adapted data, plus the 6c disclaimer), shown in the README and on the map:
  > produced using Copernicus WorldDEM-30 © DLR e.V. 2010-2014 and © Airbus Defence and Space GmbH 2014-2018 provided under COPERNICUS by the European Union and ESA; all rights reserved.
  > The organisations in charge of the Copernicus programme by law or by delegation do not incur any liability for any use of the Copernicus WorldDEM-30.

## Map page (Phase 5)

| source | terms | how we comply |
|---|---|---|
| OpenStreetMap standard tiles (`tile.openstreetmap.org`) | [Tile Usage Policy](https://operations.osmfoundation.org/policies/tiles/): visible "© OpenStreetMap contributors" attribution, a valid Referer/User-Agent (browsers send these), no bulk download or prefetching; heavy use is not allowed and access can be withdrawn | attribution in the map corner; tiles load only as the viewer pans; fine for a demo. **For a public deployment, switch to a commercial tile provider or self-hosted tiles** (one URL in `app.js`). |
| Leaflet 1.9.4 (BSD-2-Clause) via unpkg | free CDN, no terms beyond the licence | pinned version with SRI hashes; the CSP allows only that host |

The map also shows the Copernicus attribution (Terrain section), because residuals and placement are derived from the DEM.

## Privacy rules that apply to all sources
- Metadata only. Payloads are not decrypted and not stored.
- Advert locations are stored exactly in private storage only. Any output is snapped to ~1 km.
- Recorded captures are anonymized (keys remapped, names replaced, coordinates shifted and snapped)
  before they enter the repo.

## Software supply chain

| component | source | note |
|---|---|---|
| S3 object store | `chrislusf/seaweedfs:4.52@sha256:57877e32bb90073b37dc109b438ba66e84fd5ed9ced5f5c04f56444ef243ce59` (from the maintainer, Chris Lu) | Chosen by the user in Phase 6, replacing a community MinIO rebuild (MinIO stopped publishing images in 2025). Runs `weed mini` (single node, S3 on 8333) as non-root with a read-only root fs; telemetry to seaweedfs.com turned off (`-master.telemetry=false`). With no credentials it would allow all access, so compose refuses to start without `S3_SECRET_KEY` and the chart always generates one. The code only speaks plain S3 (path-style), so the store stays swappable. |
| Mosquitto | `eclipse-mosquitto:2.0.22@sha256:81a6193ae693942a3231cd3ac52875b02b7e3a19da671b2fb4b55cc874c5b279` (official) | Setting 1 only |
| Go build | `golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0` (official) | build stage only |
| Go runtime | `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` (official) | ingester, API: no shell, non-root |
| Python runtime | `python:3.12.16-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df` (official) | analytics CronJobs; DuckDB `httpfs` baked in at build time so jobs never download extensions |
| uv | `ghcr.io/astral-sh/uv:0.13.1@sha256:4a1f3666816dafadd20ceb71eac9a7d56d7ef30279f7d64048519ab901ada336` (official, Astral) | build stage only |
| Prometheus | `prom/prometheus:v3.16.0-distroless@sha256:e8b8fb0e336fe0affce6f9ea347b11d24975465fd3ba8119420186679c3efd5f` (official) | |
| Grafana | `grafana/grafana:13.2.5@sha256:9f7aa1e1e3246aade5792d35a738049f7f32f89cd51d1956cf426a4e86268a84` (official) | |
| k3s (k3d node) | `rancher/k3s:v1.35.5-k3s1@sha256:2074403abe1bded11ef3dde09d457e13be8e0b64c218b1c4f8269b4565cfbc65` (official) | local cluster only |

Our own images (`meshtwin/*:0.6.0`) are imported straight into k3d; there is no registry yet, so they have tags, not digests.
