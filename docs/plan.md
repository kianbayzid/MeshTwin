# MeshTwin plan

## Phases (stop after each)
1. Protocol research → `docs/protocol.md`, Parquet schema, synthetic + replay generators
2. Go ingester (decode → Parquet → MinIO) in docker-compose, tests on recorded packets
3. Python topology graph + resilience analysis
4. Terrain propagation model + predicted-vs-observed report
5. Placement recommender + Go API + map page
6. Containers, k3d, Helm, CronJobs, observability
7. DDIL buffering + chaos demo
8. CI, security scanning, README

## Decisions (2026-11-04)
- Region: **Seattle (SEA)**. Synthetic network and Phase 4 DEM tile cover the Seattle area.
- Coordinates: **stored raw** in private MinIO and **snapped to ~1 km at every output** (API, JSON, map). A test enforces it.
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
