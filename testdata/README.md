# testdata

| file | what |
|---|---|
| `synthetic/sea-seed1-10m.ndjson` | 10 min of synthetic Seattle traffic: 90 nodes, 8 observers, 1-byte hashes, 4,337 receptions |
| `synthetic/sea-seed1-truth.json` | the answer key for it: true nodes (exact synthetic coordinates) and true links |
| `recorded/` | *(Phase 2)* anonymized real capture |

Each NDJSON line is one MQTT message as received:
```json
{"recv_time":"2026-11-01T00:00:08.197Z","topic":"meshcore/SEA/<observer key>/packets","payload":{…observer JSON…}}
```

Regenerate (deterministic: the same seed gives identical bytes):
```sh
cd ingest
go run ./cmd/meshgen -seed 1 -duration 10m -rate 20 \
  -out ../testdata/synthetic/sea-seed1-10m.ndjson -truth ../testdata/synthetic/sea-seed1-truth.json
```
