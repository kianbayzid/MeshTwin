# Data sources

Every external source MeshTwin reads, its terms, and how we behave toward it.
**Rule:** don't connect to a source until its row here says the terms are checked.

## Packet feeds (MQTT)

Two live settings, both in `deploy/compose/` (how-to: `docs/recording.md`):

| source | setting | access | terms | status |
|---|---|---|---|---|
| **Your own observer** (USB radio on stock companion firmware + meshcore-packet-capture on Windows; meshcoretomqtt on Linux with a packet-logging repeater build; or MQTT observer firmware / meshcore-ha → your broker) | 1 `own-observer` | full | yours. packet-capture: adverts off (`ADVERT_INTERVAL_HOURS=0`), message drain off, decoding off, LetsMesh preset declined (`docs/recording.md`) | ✅ ready |
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
- QoS 0 on public brokers (no broker-side queueing on our behalf); QoS 1 only on your own broker (see below).
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

## Map page (Phase 5, replaced)

The Leaflet page (Leaflet 1.9.4 from unpkg, OpenStreetMap standard tiles from `tile.openstreetmap.org`) was replaced
by the console in Console Phase 6 (2026-11-11). The console loads nothing from other origins; the CSP
(`docs/api-contract.md`) no longer allows unpkg or the OSM tile servers. Basemap terms: see "Console basemap" below.

The map also shows the Copernicus attribution (Terrain section), because residuals and placement are derived from the DEM.

## Console basemap (frontend Phase 2)

| source | terms | how we comply |
|---|---|---|
| Protomaps daily planet build (`build.protomaps.com/YYYYMMDD.pmtiles`), vector tiles from OpenStreetMap | Data: [ODbL](https://opendatacommons.org/licenses/odbl/) via OSM, attribution "© OpenStreetMap contributors" required. Protomaps asks for credit; build files are public downloads (HTTP Range) | `web/scripts/fetch-basemap.sh` reads the build list from `build-metadata.protomaps.dev/builds.json` (newest daily build), then extracts **once** with `pmtiles extract` (go-pmtiles v1.31.2, pinned) for the region box only, max zoom 14. The file is served by us, never hotlinked, not committed (`.gitignore`). Attribution shown in the map's attribution control. |
| `@protomaps/basemaps` 5.7.2 (BSD-3-Clause), `pmtiles` 4.5.0 (BSD-3-Clause), `maplibre-gl` 5 (BSD-3-Clause) | npm packages, bundled into `web/dist` | versions pinned in `web/package-lock.json` |

- Build and CI only (never at runtime), pinned like every other image/action here:
  - `node:22.23.5-bookworm-slim@sha256:e9f565d32859f0cbfb52125eafe8be39390f24dac302dc3cdf566e8ba3ab21ee` (official Docker
    image): the API image's console build stage. `npm ci --ignore-scripts` from the lockfile; nothing from it reaches
    the final distroless image. The API image is built with `--build-context web=web`.
  - `mcr.microsoft.com/playwright:v1.65.0-noble@sha256:9beb546099e11f52d62e505537914870e01a67e0e6b00ae41da4775324e73dbe`
    (official Microsoft image): the CI `e2e` job's container, with browsers matching `@playwright/test` 1.65.0.
  - `actions/setup-node` v7.1.0 pinned to `949feb2413d6458794dcd2491c4babbbce0c15c1`.
- The console makes **no third-party requests** at runtime: basemap, terrain tiles, fonts and icons are all same-origin.
  No external terrain fallback exists; without terrain tiles the map is flat.
- **US overview basemap** (added 2026-11-13): the same Protomaps build, extracted once as `us.pmtiles` for the
  contiguous US, Alaska and Hawaii at max zoom 9 (182 MB; z10 would be ~385 MB, z11 ~835 MB for the contiguous US
  alone). Region archives (z14) draw on top of it. Same licence and attribution.

## Console city list (checked 2026-11-13)

The console's "jump to a city" list: camera targets only, no nodes. Built once, by hand, and committed as a static file;
the console never fetches either source.

| source | used for | terms | status |
|---|---|---|---|
| LetsMesh analyzer region list (`analyzer.letsmesh.net`, the region picker: IATA code, city, count badge), snapshot 2026-11-13 | which US cities have MeshCore activity: 100 US regions with a badge of 4 or more (of 429 US regions) | No written terms (see LetsMesh above). Only city names and codes are kept, plus the count shown next to each; no packets, nodes or observer identities. The badge's meaning (observers, per its `status/observers` page) is **UNVERIFIED** | ✅ one-off snapshot, re-run by hand |
| [OurAirports](https://ourairports.com/data/) `airports.csv` | each code's coordinates (the airport, a few km to ~40 km from downtown; the camera shows a metro area). `NYC` is a city code with no airport, so it uses Manhattan | "All data is released to the Public Domain, and comes with no guarantee of accuracy or fitness for use" | ✅ |

## Satellite telemetry (plan step 4, checked 2026-11-12)

| source | setting | access | terms | status |
|---|---|---|---|---|
| **TinyGS network data** (`tinygs.com`, `api.tinygs.com`) | none | No documented public API, external MQTT feed or bulk export ([README](https://github.com/tinygs/tinyGS): "we are developing an API to access data"). An undocumented `api.tinygs.com/v2/packets?satellite=…` exists; the maintainer [said](https://github.com/tinygs/tinyGS/issues/259) it "can be used" but usage is "quite restrictive to avoid impacting the network". Checked 2026-11-12 | [Terms](https://tinygs.com/terms), [Privacy](https://tinygs.com/privacy) (2025-01-06): station owners licence their data **to TinyGS**; no reuse licence for third parties. Station locations public at 3 decimals (~110 m). Ask terms@tinygs.com in writing first | ❌ no access without written permission; do not poll or scrape |
| **Our own TinyGS station** (Heltec WiFi LoRa 32 V3, **433 MHz variant**) | own station | Uploads to `mqtt.tinygs.com:8883` (credentials from `@tinygs_personal_bot`). Stock firmware shows only the last packet locally. Full history needs the GPL fork [`sundruid/tinyGS` aux-mqtt-v0.1.0](https://github.com/sundruid/tinyGS/releases/tag/aux-mqtt-v0.1.0) publishing to our Mosquitto (code not yet reviewed) | Firmware GPL-3.0. Joining is by invite/Telegram. Uploads must be real-time and unmodified (Privacy Policy). Our location published at ~110 m; MeshTwin outputs still snap to ~1 km | ⏳ buy the 433 MHz board; review the fork; decide OTA auto-update and auto-tune |
| **CelesTrak GP data** (`https://celestrak.org/NORAD/elements/gp.php?CATNR=<n>&FORMAT=CSV`) | analytics fetch | Public, no account. Checked 2026-11-12 | [Usage policy](https://celestrak.org/usage-policy.php): one download per 2-hour update; directory queries at most hourly; stop on any non-200 and alert a human (>50 errors in 2 h → firewall); >100 MB/day risks a block. Catalog numbers ≥ 100000 have **no TLE format**: use CSV/JSON (OMM). Redistribution licence **UNVERIFIED** | ✅ fetch under the rules below; ⏳ before committing or serving raw elements |

CelesTrak politeness rules:
- Fetch only the satellites the phase uses, by `CATNR`; never `GROUP=active`.
- Cache to disk; re-fetch only when the cache is more than 2 h old, and only when a job runs.
- Any non-200 stops the fetch with no retry and logs/alerts. `https://celestrak.org` only, never `.com`.
- Raw element sets are not committed or served by the API; tests use synthetic elements.

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
| Go build | `golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea` (official) | build stage only |
| Go runtime | `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` (official) | ingester, API: no shell, non-root |
| Python runtime | `python:3.12.16-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df` (official) | analytics CronJobs; DuckDB `httpfs` baked in at build time so jobs never download extensions |
| uv | `ghcr.io/astral-sh/uv:0.13.7@sha256:96f40d3904aad8d0bd010539001d36c8e2188b981e1334471a90c4a1d93bb490` (official, Astral) | build stage only |
| Prometheus | `prom/prometheus:v3.16.0-distroless@sha256:e8b8fb0e336fe0affce6f9ea347b11d24975465fd3ba8119420186679c3efd5f` (official) | |
| Grafana | `grafana/grafana:13.2.5@sha256:9f7aa1e1e3246aade5792d35a738049f7f32f89cd51d1956cf426a4e86268a84` (official) | |
| k3s (k3d node) | `rancher/k3s:v1.35.5-k3s1@sha256:2074403abe1bded11ef3dde09d457e13be8e0b64c218b1c4f8269b4565cfbc65` (official) | local cluster only |
| Chaos test image | `meshtwin/chaos:test`: built from the pinned `golang` base + our analytics image; nothing new pulled | **Test only** (`make chaos`): bundles `mqttreplay` and the synthetic `sea-seed1-10m.ndjson`. Never in default values. |

Our own images (`meshtwin/*:0.7.0`) are imported straight into k3d, and CI builds them fresh each run; there is no registry, so they have tags, not digests.

### CI (Phase 8, `.github/workflows/ci.yml`)

Actions are pinned by commit SHA (a tag can be moved; a SHA can't). Tool images by digest; downloaded binaries checked against a SHA-256 written in the workflow.

| component | version | pin |
|---|---|---|
| actions/checkout | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| actions/setup-go | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| astral-sh/setup-uv | v10.3.3 | `68559e99cc807c1eb7e9238fd43a15bba460eec2` |
| actions/upload-artifact | v7.0.2 | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` |
| golangci/golangci-lint | v2.15.0 | `sha256:fbfcfd12a6f8d7f1b97b3362ca80fc2a244d46863148c73cb88e7793af96c481` |
| aquasec/trivy | 0.76.2 | `sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb` (vulnerability DB from `mirror.gcr.io`) |
| anchore/syft | v1.57.1 | `sha256:03215d0e98b661d0ecbc8575dbc14d65be1743b5de5df6410aecb7e80f95b060` |
| rhysd/actionlint (local check) | 1.7.12 | `sha256:b1934ee5f1c509618f2508e6eb47ee0d3520686341fec936f3b79331f9315667` |
| helm linux-amd64 | v4.3.1 | sha256 `0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b` |
| k3d-linux-amd64 | v5.9.0 | sha256 `06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0` |
| kubectl linux/amd64 | v1.35.5 (matches k3s) | sha256 `90f75ea6ecc9ea5633262e1c0b83a40560003b30fc94a04cb099404fcef0c224` |
| govulncheck | v1.9.0 | `go run …@v1.9.0`, verified by the Go checksum database |
| uv / Python | 0.13.7 / 3.12.16 | via setup-uv |

## Broker politeness under QoS 1 (Phase 7)

- **own-observer** (your broker): QoS 1 + persistent session, so the broker queues messages while the ingester restarts. `MQTT_MAX_PER_SEC` is 50 there; under QoS 1 the cap throttles (no ack until processed) instead of dropping, so a backlog after a restart is delayed, not lost.
- **public-broker**: stays QoS 0 with a clean session. A persistent session would make a community broker hold a queue for us, which isn't ours to ask for.
