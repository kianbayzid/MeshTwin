# MeshTwin — project conventions

A coverage digital twin for live MeshCore LoRa mesh networks: ingest observer
packets (Go), store as Parquet in MinIO, analyze topology / terrain propagation /
resilience / placement (Python), serve read-only results (Go API + Leaflet map).
Portfolio project: every line must be explainable in an interview. Clear > clever.

## Repo layout
| Path | Contents |
|------|----------|
| `/ingest` | Go ingester: MQTT/replay/synthetic sources, decoder, Parquet writer, DDIL buffer |
| `/api` | Go read-only REST API + static Leaflet page |
| `/analytics` | Python (uv): topology, propagation, comparison, resilience, placement |
| `/schema` | Parquet schema definition + docs (single source of truth) |
| `/testdata` | Small recorded capture (NDJSON) + synthetic generator |
| `/deploy/helm` | Helm chart |
| `/dashboards` | Grafana JSON |
| `/docs` | `protocol.md`, `data-sources.md`, `benchmarks.md`, design notes |

## Workflow
- Work in the phases defined in `docs/plan.md`. **Stop after each phase** for the user to run and question it.
- End of each phase: design decisions + trade-offs, three interview questions with answers, numbers into `docs/benchmarks.md`.
- Tests ship with the code that needs them, in the same change.
- Commit only when the user asks.

## MeshCore facts
- Never guess protocol details. Cite the source (file + link) in `docs/protocol.md`.
  Primary sources: `meshcore-dev/MeshCore` `src/Packet.h`, `src/Packet.cpp`; observer JSON:
  `Cisien/meshcoretomqtt`, `agessaman` MQTT observer firmware docs.
- If something is uncertain, mark it **UNVERIFIED** in the docs with where to check.
- Path hop hashes are 1–3 byte prefixes of public keys and **collide**. Never treat a hop hash
  as a node identity; resolution is probabilistic and must carry a confidence.

## Privacy (non-negotiable)
- Metadata and public data only. Never decrypt or attempt to decrypt payloads.
- Any location leaving the analytics layer (API, JSON summaries, map, screenshots, docs) is
  snapped to a ~1 km grid. Raw coordinates never reach the API.
- Broker connections: configurable, rate-limited, polite. Every source listed in `docs/data-sources.md` with its terms.

## Go (`/ingest`, `/api`)
- Go 1.25+ modules, one module per service. Standard library first (`net/http`, `log/slog`, `context`).
- Known deps only: `eclipse/paho.mqtt.golang`, `parquet-go/parquet-go`, `minio/minio-go/v7`, `prometheus/client_golang`.
- `gofmt`, `golangci-lint`. Table-driven tests with `testing`; no assertion libraries.
- Errors are wrapped with `%w` and context; no panics outside `main`.
- Config via flags + env (12-factor); no config files baked into images.

## Python (`/analytics`)
- Python 3.12+, managed by `uv`. `ruff` (lint + format) and `mypy --strict` must pass.
- DuckDB / Polars for queries, networkx for graphs, numpy for math. `pytest` for tests.
- Pure functions for the math (propagation, scoring) so they test without I/O.
- The propagation model sits behind one small interface so it can be swapped later.

## Data
- Parquet schema lives in `/schema`; Go and Python both conform to it. Every file carries `schema_version`.
- Partitioning: `region=<IATA>/date=YYYY-MM-DD/hour=HH/`.
- `packet_hash` (firmware hash, path excluded) joins all receptions of one transmission but is NOT unique per row:
  one observer can hear a packet several times via different relays. Per-row dedup key is `reception_id`
  (see `schema/schema.md`). The same packet heard N times is N rows — that's the coverage data.

## Platform notes
- Dev machine is Windows; targets are Linux containers + k3d. Shell scripts are POSIX sh; Makefile targets assume Linux/WSL/Git Bash.
