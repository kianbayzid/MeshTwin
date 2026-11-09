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

## Phase 3

Fixtures `testdata/parquet/sea-seed1-2h-{hash1,hash3}`: 75,571 receptions each (72,454 flood), 90 nodes, 488 true links.
i7-11800H, Windows, Python 3.12, 3 runs.

| stage | hash1 | hash3 |
|---|---|---|
| DuckDB load (filters pushed into SQL) | 0.36–0.42 s | 0.35–0.47 s |
| Hop resolution (forward-backward, new chains only) | 0.62–0.70 s | 0.66–0.91 s |
| Aggregation + graph | 0.37–0.43 s | 0.40–0.53 s |
| Resilience (remove each relay) | 0.03 s | 0.03 s |
| **Whole CLI command** (incl. Python startup, writing outputs) | **≈2.0 s** | **≈2.0 s** |
| Distinct chains / memo hit rate | 4,825 / **93.3%** | 4,854 / 93.3% |

Accuracy against ground truth (`meshtwin-analytics evaluate`, min_confidence 0.5):

| | hash1 (1-byte, collisions) | hash3 (3-byte) |
|---|---|---|
| Ambiguous hop entries | 6,961 / 184,490 (3.8%) | 0 |
| Link precision | **0.994** (315/317) | **1.000** (325/325) |
| Recall: relay–relay links | 0.988 | 1.000 |
| Recall: links touching a relay | 0.966 | 0.997 |
| Recall: all truth links | 0.645 | 0.666 |
| SPOF precision / recall (6 true SPOFs) | **1.0 / 1.0** | **1.0 / 1.0** |

Reading it: plain recall is capped because companion↔companion links never appear in flood paths (companions don't
relay), so the paths can't reveal them. That's a limit of the data, not of the method. On hash1, both false links and
10 of the 11 missed relay links come from one pair of nearby repeaters sharing hash `60`; the distance prior can't
separate them. Caveat: the synthetic generator advertises far more often than real repeaters (every 47 h by default),
so real data will have more `unknown` hops. Re-measure on a real capture.
