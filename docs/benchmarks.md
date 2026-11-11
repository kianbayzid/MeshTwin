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

Not measured yet: end-to-end write latency to S3 (SeaweedFS since Phase 6).

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

## Phase 4: propagation model (2026-11-08)

`meshtwin-analytics propagation` on `sea-seed1-2h-hash1` (Windows dev laptop, Python 3.12):

| | flat terrain | Copernicus GLO-30 (N47 W123) |
|---|---:|---:|
| Links with observed SNR and both ends located | 85 | 85 |
| Line of sight | 85/85 | 18/85 |
| Median \|residual\| / RMSE / bias (dB) | 29.8 / 30.7 / −30.3 | 14.6 / 19.1 / −14.9 |
| Observed but predicted impossible | 0 | 1 |
| Predicted strong but never seen | 50 (cap) | 50 (cap) |
| Propagation step (410 profiles) | 0.035–0.051 s (≈8–12k profiles/s) | 0.060–0.074 s (≈5.6–6.9k profiles/s) |
| DEM load (one 1°×1° tile) | — | 0.15–0.16 s |
| Whole command (incl. topology inference) | 2.0 s | 2.2 s |

- DEM tile: 39.8 MB (COG, float32 3600×3600), ~27 s to download once; cached outside the repo.
- **Observed side is right:** median |observed − true link SNR| = **0.063 dB** over 84 links (test bound 0.5 dB).
- **Predicted side** is checked against textbook values: FSPL(910.525 MHz, 10 km) = 111.625 dB, J(0) ≈ 6.0 dB,
  noise floor −120.04 dBm, a 60 km / 2 m smooth-Earth path checked by hand.
- **Reading the bias:** the synthetic generator uses a log-distance exponent of 7.5 and no terrain, so a large negative
  bias on synthetic data is expected, and the model is deliberately **not** tuned to it. Adding terrain halves the
  error even so, because the generator's high exponent stands in for real obstruction. The real test is a real capture.

## Phase 5: placement recommender + API (2026-11-09)

Placement (`meshtwin-analytics placement`, `sea-seed1-2h-hash1`):

| | flat terrain | Copernicus GLO-30 |
|---|---:|---:|
| Candidate cells scored (0.01° grid) | 1,520 | 1,400 (120 water cells dropped) |
| Terrain profiles | 101,322 | 94,260 |
| Placement step | 6.7 s (≈15k profiles/s) | 11.8 s (≈8k profiles/s) |
| Whole command (incl. topology) | 9.1 s | 14.2 s |
| Rank 1 | 47.52/−122.18, removes 6/6 SPOFs | 47.52/−122.10, +1,674 pairs, 27 nodes joined, removes 5/6 SPOFs |

- **Truth check (qualitative, not tuned):** the inferred graph has the same two relay islands as the truth (reachable share
  0.582 in both). The top cells sit 4–7 km from the eastern island's two largest true SPOFs and bridge it to the
  main island: the right gap.
- **Caveat:** the optimistic model links the best cells to 70+ of 73 located nodes, so many cells tie on pairs
  gained and SPOFs removed decides the order. Calibrating the model on real residuals is the fix.

API (`api/`, Go 1.27.3, Windows laptop):

| | |
|---|---:|
| Binary size (`-trimpath -ldflags="-s -w"`) | 11.5 MB |
| Unfiltered topology (116 KB on disk, 76 KB re-encoded), `go test -bench` | 60 µs/op (validated once per 30 s cache TTL) |
| Topology with `min_confidence=0.9` (decode + filter + encode) | 1.04 ms/op |
| Unfiltered topology over curl, localhost | 1.35–1.43 ms |
| API tests | 94 cases (incl. one per privacy bypass found in 4 verifier rounds) |

## Phase 6: containers + k3d (2026-11-09)

k3d 5.9.0 (k3s v1.35.5, one server node) on Docker Desktop 29.2.1 / WSL2, same laptop. Smoke run = `values-smoke.yaml`
(seeds the **synthetic test fixture** `sea-seed1-2h-hash1`; real runs use the own-observer or public-broker setting).

| what | result |
|---|---:|
| Image sizes, compressed / on disk | ingester 7 / 29 MB · api 5 / 22 MB · analytics 192 / 792 MB |
| Analytics venv breakdown | 413 MB: pyarrow 170, imagecodecs 87, duckdb 65 |
| Cluster create | 18–35 s |
| Build (cached) + `k3d image import` | 18 s |
| Fresh deploy to all Ready (incl. third-party image pulls + seed hook) | 2 min 4 s |
| `helm upgrade` | 27 s |
| topology job: wall / peak memory | 22 s / 156 MiB |
| propagation job (incl. 6 s first DEM download) | 36 s / 476 MiB |
| placement job (DEM cache hit) | 34 s / 500 MiB |
| Steady state memory | api 11 Mi · ingester 7 Mi · seaweedfs 128 Mi · prometheus 39 Mi · grafana 181 Mi · mosquitto 1 Mi |
| Summary freshness with no user traffic | 40–92 s (readiness probe on `/api/v1/regions` refreshes the gauge) |

- Job peaks are cgroup `memory.peak`: `kubectl top` samples too slowly for jobs this short. Limits in `values.yaml` are set from these.
- The analytics image is big because of pyarrow + imagecodecs. Fine for a CronJob that pulls once per node; a slimmer
  build (drop imagecodecs codecs we don't use) is a later option.

## Phase 7: DDIL buffering + chaos demo (2026-11-10)

Spool micro-benchmarks (`go test -run=^$ -bench=Spool -benchtime=64x ./internal/ingest`, 1 MiB files):

| | Linux container | Windows native |
|---|---:|---:|
| Spool write (fsync file + directory) | 338 MB/s (3.1 ms/file) | 285 MB/s |
| Drain to an in-memory store (read, upload, delete) | 1,385 MB/s | 918 MB/s |

Real drains are bounded by the network or `DRAIN_RATE_BYTES`, not the disk.

`make chaos` on k3d (synthetic **test** capture `sea-seed1-10m.ndjson` published over MQTT QoS 1 at 0.5× speed; `FLUSH_AGE=15s`):

| scenario | result |
|---|---|
| a. storage down 180 s | spool peak 88.9 KB / 12 files, oldest 163 s; ingester Ready throughout; drained 25 s after restore (incl. SeaweedFS start) |
| b. flapping, 3 × 20 s down / 20 s up | peaks 8.5–16.5 KB; drained to 0 inside every 20 s up-window |
| c. ingester pod deleted mid-outage | new pod Ready in 6 s with S3 still down, inherited the spool from its PVC; broker queue gap ~0.7 s (persistent session resumed) |
| d. drain capped at 1,024 B/s | 43.7 KB spooled; drain took ~53 s after storage was back (≥ 42 s expected at the cap) |
| **Zero-loss check** | **4,337 expected, 4,337 distinct in S3, 0 missing, 0 duplicates, 0 unexpected** |

- 0 ingester restarts, 0 evictions; `MeshTwinStorageDown` went pending (never fired: both outages < its 5 min `for:`).
- Script runtime 22 m 39 s (~11 m of scenarios, the rest waiting for the publisher to finish).
- Read-side dedup by `reception_id` (at-least-once delivery): topology command 1.523 s → 1.562 s (+2.6 %) on `sea-seed1-2h-hash1`.
- Not exercised: SIGKILL (rows in memory are lost), QoS 0 public broker (messages lost while the ingester is down), mosquitto restart (queue persistence configured, untested).

## Phase 8: CI + security scanning (2026-11-10)

The workflow has not run on GitHub yet (no remote). These are its steps run locally in the same pinned Linux containers
(Docker Desktop / WSL2, warm module caches).

| job / step | time |
|---|---:|
| go (ingest): gofmt 1 s · vet 27 s · test -race 32 s · golangci-lint 30 s · govulncheck 12 s | ~1.7 min |
| go (api): vet 1 s · test -race 4 s · golangci-lint 4 s · govulncheck 6 s | ~15 s |
| python: uv sync --locked 20 s · ruff 0 s · mypy 5 s · pytest 40 s (89 tests) | ~1.1 min |
| helm: checksum-verified install + `make lint` 14 s · Trivy config scan of the rendered chart 5 s | ~20 s |
| images, cold build: ingester 32 s · api 18 s · analytics 38 s | |
| Trivy: DB download 18 s (133 MB) · scan 1 s per Go image, 8 s analytics | |
| Syft SBOM: 2 s / 3 s / 11 s | |
| `make smoke` on a running cluster (deploy + seed + 3 jobs + 31 s cache wait) | 184 s |

Vulnerability scan (Trivy 0.76.2; the gate fails on HIGH/CRITICAL **with a fix available**):

| image | before Phase 8 | after | gate |
|---|---|---|---|
| ingester (29 MB) | HIGH 1 (`x/net`), MEDIUM 1, UNKNOWN 5 | UNKNOWN 5 | pass |
| api (22 MB) | HIGH 1 (`x/net`), MEDIUM 1, UNKNOWN 5 | UNKNOWN 5 | pass |
| analytics (792 MB) | CRITICAL 2, HIGH 96 (none fixable), + pip: 5 MEDIUM, 1 LOW | OS findings unchanged, pip removed: 0 Python findings | pass |

- govulncheck (call-graph aware): ingest had **2 reachable** vulnerabilities (brotli, klauspost/compress via the Parquet
  writer); now 0 in both modules. Trivy matches versions only, so it also caught `x/net`, which govulncheck showed unreachable.
  The two tools disagree by design; both gates are kept.
- golangci-lint v2 found 56 issues in ingest and 13 in api; 0 after fixes. 27 `//nolint` remain, each naming one rule with a reason.
- SBOMs (SPDX 2.3): ingester 43 packages, api 33, analytics 120.
- The remaining UNKNOWNs are distroless ca-certificates/tzdata advisories (base digest is already the newest) and an x/crypto
  advisory fixed only in an untagged pre-release (not reachable).

## Console Phase 2: terrain tiles (2026-11-11)

| measure | value |
|---|---|
| `terrain-tiles` SEA, bounds `[-122.6, 47.3, -121.8, 47.9]`, z8–12 | 161 tiles, 8.3 MB |
| DEM load (2 GLO-30 tiles) · render + write | 0.79 s · 2.13 s (~75 tiles/s) |
| elevation sampled in bounds | −5.4 m … 1,091.1 m |
| spot checks (decoded z12) | Cougar Mtn 468.8 m (summit ≈ 486 m) · Lake Washington 3.0 m · Elliott Bay 0.0 m |
| one z12 tile over the API | 53 KB PNG |

Docker Desktop Linux container writing to a Windows bind mount. Lake Washington reads 3 m against ~6 m NAVD88:
GLO-30 flattens water and uses EGM2008 heights.

## Console Phase 3: SEA summaries with GLO-30 (2026-11-11)

Synthetic fixture `sea-seed1-2h-hash1` (75,571 receptions), Docker on the dev machine, wall time incl. `uv run` startup.

| job | wall | notes |
|---|---|---|
| topology | 2.98 s | 90 nodes, 317 links, 6 SPOFs |
| propagation, GLO-30 | 2.52 s | DEM load 0.32 s, model 0.08 s for 85 links |
| placement, GLO-30 | 17.4 s | 1,400 candidates, 94,260 profiles, ~8,100 profiles/s |

| residuals (85 links) | flat | GLO-30 |
|---|---|---|
| median abs | 29.8 dB | 14.6 dB |
| RMSE | 30.7 dB | 19.1 dB |
| bias | −30.3 dB | −14.9 dB |

Caveat: the synthetic generator ignores terrain, so the GLO-30 improvement is not evidence about real links
(see `docs/frontend.md`, "Reading the SEA demo data honestly").

## Console Phase 4: link profiles (2026-11-11)

| measure | value |
|---|---|
| profiles written (SEA, GLO-30) | 237 of 317 links (others: same cell or unlocated) |
| samples per profile | 26–256 |
| `profiles.json` | 448,295 B compact (72,748 B gzip); API cap 8 MB ≈ 750 indented / 1,300+ compact profiles |
| time added to the propagation job | 0.06 s |
| grid path vs exact path, 351FC1F9–FDC42289 | clearance −18.66 vs −18.75 × F1; diffraction 41.3 vs 41.4 dB |
