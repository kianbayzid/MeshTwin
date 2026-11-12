# MeshTwin — project conventions

A coverage digital twin for live MeshCore LoRa mesh networks: ingest observer
packets (Go), store as Parquet in S3 (SeaweedFS), analyze topology / terrain propagation /
resilience / placement (Python), serve read-only results (Go API + 3D web console).
Portfolio project: every line must be explainable in an interview. Clear > clever.

## Repo layout
| Path | Contents |
|------|----------|
| `/ingest` | Go ingester: MQTT/replay/synthetic sources, decoder, Parquet writer, DDIL buffer |
| `/api` | Go read-only REST API; serves the console (`web/dist`, embedded) |
| `/web` | Vite + React + TS console (MapLibre 3D map); see `docs/frontend.md` |
| `/analytics` | Python (uv): topology, propagation, comparison, resilience, placement |
| `/schema` | Parquet schema definition + docs (single source of truth) |
| `/testdata` | Small recorded capture (NDJSON) + synthetic generator |
| `/deploy/helm` | Helm chart |
| `/dashboards` | Grafana JSON |
| `/docs` | `protocol.md`, `data-sources.md`, `benchmarks.md`, design notes |

## Lane ownership (agents in `.claude/agents/`, rationale in `docs/ai-workflow.md`)
| Lane | Agent | May edit |
|------|-------|----------|
| Go / ingest / API | `go-engineer` | `ingest/`, `api/`, `schema/` |
| Analytics | `analytics-engineer` | `analytics/` |
| Platform | `platform-engineer` | `deploy/`, `dashboards/`, `.github/`, `Makefile`, `**/Dockerfile`, `**/.dockerignore` |
| Verification | `verifier` | nothing (read + run checks only) |
| Research | `protocol-researcher` | nothing (read + web only) |
| Frontend | lead (main session) for now | `web/` |
| Lead (main session) | — | `README.md`, `docs/`, `CLAUDE.md`, `.claude/`, `testdata/` READMEs; assigns work and merges |

- A lane that needs a change outside its directories stops and reports it; the lead routes it to the owner.
- Contracts between lanes: `schema/schema.json` (Go ↔ Python) and `docs/api-contract.md` (analytics ↔ API).
  Changing either needs both lanes plus the verifier.
- **Every change goes through `verifier` before merge.** No verdict, or `FAIL`, means not merged.
  The owning lane fixes failures; the verifier never edits.
- **Merge order** within a phase: analytics and API lanes first (in either order, each verified), then the
  platform lane, which packages what those lanes produced. Re-run the verifier after each merge.
- Until the repo is under git, "merge" means the lead accepts the change after a `PASS`.
  With git, each lane works on `lane/<agent>/<topic>`.

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
- Go 1.26+ modules, one module per service. Standard library first (`net/http`, `log/slog`, `context`).
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
