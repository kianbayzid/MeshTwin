# Benchmarks

Machine: Intel i7-11800H (8C/16T), 16 GB RAM, Windows 11, Go 1.27.3. Single run unless noted.

## Phase 1

| what | result | how to reproduce |
|---|---|---|
| Synthetic generator throughput | **497,111 receptions in 3.13 s ≈ 159k records/s**, 309 MB NDJSON (≈621 B/record) | `go build -o meshgen ./cmd/meshgen && time ./meshgen -seed 7 -duration 24h -rate 30 -out big.ndjson` (from `ingest/`) |
| Decoder fuzzing | ~500k execs/s, **10.1M inputs, 0 failures** (no panics, every decodable input round-trips byte-exactly) | `go test ./internal/meshcore -run=^$ -fuzz=FuzzDecode -fuzztime=20s` |
| 1-byte hash ambiguity | 90 nodes → 70 distinct first bytes. **4,677 ambiguous hop entries in 4,337 receptions** (seed 1, 10 min) | `go test -v -run Collisions ./internal/synth` |
| Synthetic topology (defaults, seeds 1–3) | 90 nodes, avg degree 8–11, 1–7 articulation points | `meshgen -seed N -truth t.json`, then graph stats |

Baseline for later: NDJSON is ~621 B per reception. Phase 2 will record the Parquet+zstd size of the
same data, which gives the compression ratio.

## Phase 2

Measured in a `golang:1.27.3-bookworm` container on the same machine (Docker Desktop, WSL2).

| what | result | how to reproduce |
|---|---|---|
| Convert + encode throughput (JSON parse, decode, row build, sort, Parquet+zstd), single goroutine | **~208k rows/s** (201,698 rows, one hour partition, 0.97 s) | `go test -run=^$ -bench=ConvertAndEncode -benchtime=3x ./internal/ingest` |
| Storage size | NDJSON **621 B/row** → Parquet+zstd **28.5 B/row** = **21.8× smaller** | same benchmark |
| Ingester image | **29 MB** (distroless static, non-root) | `docker build -t meshtwin/ingester:dev ingest/` |

Caveat: synthetic data, with 90 nodes and random ciphertext not stored. Real traffic has more distinct
observers and paths, so expect a lower ratio. Re-measure once a real capture exists. For scale, a busy
region at ~50 receptions/s is ~4.3M rows/day ≈ 120 MB/day of Parquet. One core converts that day in ~21 s.

Not measured yet: end-to-end write latency to MinIO (needs the MinIO container; see Phase 2 notes).
