# Parquet schema v2

`schema.json` is the machine-readable source of truth. The Go writer (Phase 2) and the Python
reader (Phase 3) each have a test that fails if they drift from it. This file explains *why*
the schema looks the way it does.

## Layout in object storage

```
s3://meshtwin/{table}/region=SEA/date=2026-11-04/hour=13/part-<writer>-<seq>.parquet
```
Hive-style partitions, so DuckDB and Polars prune by region and time without reading files. One
hour is the unit the hourly topology CronJob reads.

## `receptions`: one row per (packet, observer, reception)

| column | why |
|---|---|
| `schema_version` | in every row, so a reader can handle mixed old/new files during a migration |
| `reception_id` | **per-row identity for idempotent writes and DDIL dedup.** `SHA-256(observer_id ‖ observer timestamp ‖ raw)[:16]`. An MQTT redelivery or a buffered-file resync of the same message gives the same id. Two genuine receptions differ in timestamp. 16 bytes because 8 would give a ~3% birthday collision chance at 10⁹ rows. |
| `packet_hash` | firmware packet identity (path excluded). Joins all receptions of one transmission. **Not unique per row**: an observer can hear one packet several times via different relays. |
| `observer_id` | observer public key (hex), from the topic |
| `region` | IATA code from the topic |
| `rx_time` | observer timestamp if it has a UTC offset, else ingester receive time (see protocol.md §5) |
| `route_type`, `payload_type`, `payload_version` | header fields |
| `path_hash_size`, `hop_count` | from `path_len`. Size is needed to interpret hops and to measure how ambiguous they are. |
| `path_hops` | hop hashes as uppercase hex strings, oldest first. Hex rather than binary so SQL can match them to keys: `starts_with(pubkey, hop)`. Empty for TRACE. |
| `trace_snr_db` | direct TRACE only: per-hop SNR from the path bytes |
| `snr_db`, `rssi_dbm` | last-hop link quality measured by the observer. Null if missing. |
| `payload_len` | size only. **Payload bytes are not stored** (data minimization: they're ciphertext we have no business keeping) |
| `src_id`, `dst_id` | plaintext endpoint ids where the payload exposes them: 64-hex full key (ADVERT/ANON_REQ source) or 2-hex prefix (V1 1-byte hash). Null otherwise. |
| `data_source` | provenance: `live`, `replay` or `synthetic`, the ingester's `-mode`. The console must say whether a region shows real or generated data. Per row, not per file or region, so a bucket that mixes sources stays countable. Replay writes `replay`: a capture file does not record where it came from. Dictionary-encoded like `region`. Added in v2. |

Not stored: transport codes (no analytic use yet), `score`, the JSON's redundant `path`/`route`/`len`.

## `adverts`: decoded public ADVERT fields, one row per reception

Same key columns as `receptions`, plus `pubkey`, `name`, `role`, `advert_time` (sender clock, which is
often wrong on nodes without GPS), `lat`/`lon` at full precision or null, and `data_source` (as in `receptions`).

**Privacy:** `lat`/`lon` are exact here because the terrain model needs them. This bucket is private.
Everything downstream that leaves the analytics layer (API, JSON summaries, map) snaps
coordinates to the ~1 km grid, and a test enforces that.

One row per reception rather than per advert keeps the ingester stateless. Analytics takes the
latest advert per `pubkey`.

## Types and encoding notes
- Row groups: target ~128 MB uncompressed or one hour, whichever comes first (tuned in Phase 2, with
  numbers in benchmarks.md).
- Compression: zstd. Hex strings (`observer_id`, `path_hops`) are low-cardinality and dictionary-encode well.
- Evolution: add columns only (at the end) and bump `schema_version`. Never repurpose a column.

## Migrations

**v1 → v2** (additive): `data_source` appended to both tables. It is non-null in every v2 file, but
v1 files do not have the column at all, so readers must handle both: read with
`union_by_name` (DuckDB `read_parquet(..., union_by_name = true)`), which yields null for v1 rows,
and report those rows as `unknown`. v1 files are not rewritten; `testdata/parquet` stays v1 as the
backward-compatibility case.
