# MeshTwin plan

## Phases (stop after each)
1. Protocol research → `docs/protocol.md`, Parquet schema, synthetic + replay generators
2. Go ingester (decode → Parquet → S3) in docker-compose, tests on recorded packets
3. Python topology graph + resilience analysis
4. Terrain propagation model + predicted-vs-observed report
5. Placement recommender + Go API + map page
6. Containers, k3d, Helm, CronJobs, observability
7. DDIL buffering + chaos demo
8. CI, security scanning, README

## Decisions (2026-11-04)
- Region: **Seattle (SEA)**. Synthetic network and Phase 4 DEM tile cover the Seattle area.
- Coordinates: **stored raw** in private S3 storage and **snapped to ~1 km at every output** (API, JSON, map). A test enforces it.
- Test capture: **record from a public source, then anonymize** (remap pubkeys, shift coordinates consistently) before it enters the repo.
- Generator + replay: **Go**, in the ingest module. They double as the ingester's synthetic and replay modes.

## Phase 1 detail

### What the sources already confirm (cited in protocol.md)
- Wire format (`src/Packet.cpp` `readFrom`):
  `header(1) | transport_codes(4, only if route ∈ {0,3}) | path_len(1) | path(count×size) | payload(rest)`
- `header`: bits 0–1 route type, bits 2–5 payload type (REQ, RESPONSE, TXT_MSG, ACK, ADVERT, GRP_TXT, … TRACE, CONTROL, RAW_CUSTOM), bits 6–7 payload version.
- `path_len`: low 6 bits = hop count, top 2 bits = (hash size − 1). So hop hashes are **1, 2 or 3 bytes**; 4 is reserved. Older firmware is always 1 byte, which means only 256 values, so collisions are guaranteed in any real region.
- Packet hash = SHA-256(payload_type ‖ payload) truncated (plus path_len for TRACE). It **excludes the path**, so the same packet heard by N observers via different routes has one hash. That is the dedup and join key.
- SNR on the radio is int8 in quarter-dB steps.
- Observer MQTT: topic `meshcore/{IATA}/{OBSERVER_PUBKEY}/packets` (+ `/status`). The JSON payload has numeric fields as strings and a `raw` hex string. `path` shows up inconsistently: a `"C2 -> E2"` string in one tool, an array in another, and only on direct routes.
  → **Decision: decode `raw` as the source of truth.** Use the JSON only for observer id, IATA, timestamp, SNR, and RSSI.

### Still to verify in Phase 1 (will be marked UNVERIFIED until checked)
- ADVERT payload layout (pubkey 32 B, timestamp, 64 B signature, appdata flags / lat-lon / name): check `src/Mesh.cpp` and `src/helpers/AdvertDataHelpers.*`.
- `MAX_HASH_SIZE` (expect 8 B, which matches the 16-hex `hash` field in the JSON): check `MeshCore.h`.
- Flood path semantics: each repeater appends its own hash on retransmit, so the observer's direct neighbor is the last hop.
- Whether the observer's `SNR` is the last-hop SNR only (expected: yes).

### Deliverables
1. `docs/protocol.md`: wire format, field tables, worked hex decode example, hash-collision analysis, source links.
2. `docs/data-sources.md`: brokers and their terms, rate-limit policy.
3. `schema/`: Parquet schema v1 (see below) + `schema.md`.
4. Synthetic generator: places N nodes in a region with a simple distance/SNR link model, floods packets through the known graph, and emits NDJSON in the exact observer JSON format with valid `raw` bytes. It writes **ground truth** (true nodes and links; SPOFs are derived from the true graph in Phase 3) next to the NDJSON so Phase 3 can be scored against it.
5. Replay reader: streams NDJSON at 1×/N×/max speed. It is shared by the ingester's replay mode.
6. `testdata/`: generated sample + ground truth (real capture → Phase 2).
7. Tests: golden-byte decode tests, a generator round-trip test (generate → decode → recover graph), and a collision test using forced 1-byte hashes.

### Status: done (2026-11-04)
- Final schema: `schema/schema.json` + `schema/schema.md`. It changed from the draft: added `reception_id`,
  because `packet_hash` is not unique per row, and switched to hex `path_hops` and `trace_snr_db`.
- **Moved to Phase 2:** recording the real capture and the anonymizer. A broker has to pass
  the terms check in `docs/data-sources.md` first, and the capture only matters once the ingester exists.

## Phase 2: done (2026-11-08)
- Ingester: `ingest/cmd/ingester` (live / replay / synthetic), `internal/ingest` (convert, partitioned writer, S3),
  `internal/source` (MQTT), metrics and health endpoints, graceful drain on SIGTERM.
- Two live settings, per the user's request: `deploy/compose/own-observer.env` and `deploy/compose/public-broker.env`.
  Synthetic stays for unit tests and CI only. Real captures come from the two settings.
- Capture tooling: `RECORD_FILE` on the ingester + `cmd/anonymize`. Workflow in `docs/recording.md`.
- `meshdecode` CLI for demos.
- Open: no real capture yet (needs an observer or a broker read account); MinIO end-to-end run pending
  the user's OK on the `pgsty/minio` image.

## Phase 3: done (2026-11-09), pending the user's review
- Agent workflow set up first: `.claude/agents/*`, hooks, `docs/api-contract.md`, `docs/ai-workflow.md`.
- go-engineer: `LocalStore` + `-local-dir`, fixtures `testdata/parquet/sea-seed1-2h-{hash1,hash3}`, region validation at the topic.
- protocol-researcher: relay roles, advert cadence (47 h), hash-width default. Recorded in protocol.md §2.
- analytics-engineer: `analytics/` (topology, hop resolution, resilience, evaluation, contract JSON).
- Follow-ups: synthetic advert cadence is unrealistic (go lane); EM-lite prior refinement for collisions like `60` (analytics).

## Phase 4: done (2026-11-08), pending the user's review
- protocol-researcher: US preset (910.525 MHz / 62.5 kHz / SF7 / CR5), TX 22 dBm, SX1262 floors, P.526-16 equations, Copernicus licence + attribution, 3DEP fallback.
- analytics-engineer: `terrain.py` (GLO-30 reader via tifffile + imagecodecs, `fetch-dem`), `propagation.py` (FSPL + single knife-edge behind `PropagationModel`), `compare.py` (`residuals.json`, `surprising.json`).
- Follow-ups: SNR saturation near +14 dB inflates residuals on strong links; "never seen" list hits its cap (margin to tune on real data); Deygout or P.526 §4.5 Bullington for multiple ridges; generator CR5 comment (go lane).

## Phase 5: done (2026-11-09), pending the user's review
- analytics-engineer: `placement.py` + `placement` CLI (0.01° candidate cells, new links via the `PropagationModel`, relay-aware gain, SPOFs removed for the top 50). Contract gained `method.dem`, `method.spofs_checked_top`.
- go-engineer: `api/` module (contract endpoints, ETag/304, local + S3 stores, 8 MB cap, metrics, CSP) and the embedded Leaflet map.
- **Validation before serving:** every summary is decoded, checked against an allow-list of the contract's key paths and types,
  privacy rules applied (0.01° cells in range, 2–8 hex ids, names only for repeater/room, RFC 3339 times), then re-encoded;
  the raw file is never served. Took four verifier rounds; each round's bypasses are now test cases.
- Design limit: free-text fields (repeater names, reasons) are not policed; snapping in analytics is the primary control.
- Follow-ups: model calibration from residuals (placement ranking saturates); Dockerfile for the API (Phase 6, platform lane); S3 store untested against MinIO.

## Phase 6: done (2026-11-09), pending the user's review
- Decision (user): **SeaweedFS (official `chrislusf/seaweedfs`) replaces MinIO** as the S3 store, in compose and k3d. pgsty/minio was never approved.
- Tooling: k3d 5.9.0 and Helm 4.3.1 installed with winget (user approved).
- go-engineer: summary freshness gauge in the API. platform-engineer: Dockerfiles (digest-pinned), compose, k3d, Helm chart, CronJobs, Prometheus/Grafana.
- platform-engineer: `api/` + `analytics/` Dockerfiles, every base image pinned by digest; compose on SeaweedFS + an `api` service; `deploy/k3d/cluster.yaml`; Makefile (`cluster images deploy smoke lint clean`); chart `deploy/helm/meshtwin` (SeaweedFS, ingester, API x2, 3 CronJobs, Prometheus, Grafana); `dashboards/meshtwin.json` + `alerts.yml`.
- Verifier rounds: platform FAIL once (compose mosquitto not digest-pinned; no empty-secret guard on SeaweedFS in the chart), analytics FAIL once (Windows rename race in `fetch_glo30`, caught by its own new test). Both fixed; hardening added: SeaweedFS NetworkPolicy, kube API on 127.0.0.1.
- API readiness probe hits `/api/v1/regions`, which keeps the freshness gauge current without user traffic.
- Follow-ups: ingester starts its HTTP server only after S3 answers (startupProbe + one cold-start restart; go lane); Prometheus on emptyDir; our images have tags, not digests, until there's a registry (Phase 8); OSM tiles for a public deploy.

## Phase 7: done (2026-11-10), pending the user's review
- Goal: no data loss across storage outages, flapping links and ingester restarts; bounded memory and disk; drain throttled on limited links.
- go-engineer: disk spool behind the Store interface (fsync, content-addressed keys, so re-uploads are idempotent), newest-first drain with a rate limit, oldest-first eviction at a cap, start without S3, MQTT QoS 1 + persistent session option, `cmd/mqttreplay` test publisher, spool metrics.
- platform-engineer: spool PVC, Recreate strategy, QoS 1 for own-observer only (public brokers stay QoS 0), DDIL dashboard row + alerts, `make chaos` (storage outage, flapping, ingester killed mid-outage, limited link) ending in a zero-loss check by distinct `reception_id`.
- Known gap by design: SIGKILL loses rows still in memory; QoS 0 loses messages while the ingester is down.
- Verifier rounds: Go 4 (storage never retried when the spool write failed; QoS 1 backlog dropped by the rate cap; out-of-order PUBACKs, unbounded goroutines and a send-on-closed-channel panic; then comments), analytics 1, platform 1. Final design: one MQTT worker acks in arrival order, QoS 1 throttles instead of dropping.
- Added read-side dedup by `reception_id` in analytics: delivery is at-least-once.
- `make chaos`: 4 scenarios, zero loss (4,337/4,337, 0 duplicates). Numbers in benchmarks.md.
- Follow-ups: scenario d's assertion is too loose to catch a missing rate cap (behaviour shown, check weak); `expected > 0` guard in the zero-loss Job; mosquitto config checksum hashes only one value; duplicated comment in own-observer.env; ingester startup log wording ("spooling to disk while object storage is down" printed with S3 up); mosquitto restart untested; cluster left on `flushAge=15s` until `make deploy`.
