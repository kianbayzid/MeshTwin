# API contract (v1)

The read-only REST API (Go, Phase 5) serves JSON summaries that the analytics jobs (Python) write.
**This file is the contract between those two lanes.** Analytics writes exactly these shapes to
`s3://meshtwin/summaries/region={REGION}/{name}.json`; the API validates and serves them. A change here
needs both lanes and the verifier.

## Conventions

- Base path `/api/v1`. `GET` only. JSON, UTF-8, `snake_case`.
- Every response has an `meta` envelope:
  ```json
  "meta": {
    "schema_version": 1,
    "region": "SEA",
    "generated_at": "2026-11-09T14:00:00Z",
    "window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"},
    "source": "topology-job"
  }
  ```
- Times are RFC 3339 UTC. SNR/RSSI in dB/dBm with one decimal place.
- **Errors:** `{"error": {"code": "not_found", "message": "no topology for region XYZ yet"}}` with HTTP
  400 (`bad_request`), 404 (`not_found`) or 503 (`not_ready`: the job hasn't produced this summary yet).
- Caching: `Cache-Control: max-age=60`, and `ETag` = hash of the summary file.

## Privacy rules (enforced by tests in both lanes)

1. **Locations are grid cells, never points.** `location` is the centre of a 0.01° cell (≈1.1 km N–S,
   ≈0.75 km E–W in Seattle): a value on the 0.01° grid, so at most two decimals (JSON writes 47.60 as `47.6`). `"location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.01}`.
   A node without an advertised location has `"location": null`.
2. **Node ids are an 8-hex-character key prefix** (`"id": "351FC1F9"`), not the full key. That's enough to be
   unique in a region, and short enough to be a label rather than an identity. A node seen **only as a hop hash**
   has the hash itself as its id, 2–6 hex characters (e.g. `"id": "C2"`): that's all that is known.
3. **Names only for infrastructure.** `name` is set for repeaters and room servers (named public
   infrastructure) and is `null` for companions, which are personal devices.
4. No message content, ever. There isn't any to give: payloads aren't stored.

## Endpoints

### `GET /healthz`, `GET /readyz`
Liveness: `200 ok`. Readiness: `200 ready` once the summary store is reachable, else `503`.

### `GET /api/v1/regions`
Regions with at least one summary.
```json
{
  "regions": [
    {"region": "SEA", "updated_at": "2026-11-09T14:00:00Z", "nodes": 87, "links": 241,
     "summaries": ["topology", "resilience", "residuals", "placement"]}
  ]
}
```

### `GET /api/v1/regions/{region}/topology`
The inferred link graph for the latest window. Source: the hourly topology job (Phase 3).
Query: `min_confidence` (0.5–1, default 0.5): drop less certain links. The topology job exports only links with
confidence ≥ 0.5, and resilience is computed on those, so the API can filter upward but not below 0.5.
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T14:00:00Z",
           "window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}, "source": "topology-job"},
  "stats": {"receptions": 412833, "flood_receptions": 301277, "observers": 8,
            "nodes": 87, "links": 241, "ambiguous_hop_share": 0.38, "unresolved_hops": 112},
  "nodes": [
    {"id": "351FC1F9", "role": "repeater", "name": "SEA-RPT-01",
     "location": {"lat": 47.62, "lon": -122.35, "grid_deg": 0.01},
     "observer": true, "degree": 9, "last_heard": "2026-11-09T13:58:12Z"},
    {"id": "0562C16B", "role": "companion", "name": null,
     "location": {"lat": 47.54, "lon": -122.11, "grid_deg": 0.01},
     "observer": false, "degree": 2, "last_heard": "2026-11-09T12:03:40Z"}
  ],
  "links": [
    {"a": "351FC1F9", "b": "3491927B",
     "observations": 1840,
     "confidence": 0.97,
     "snr_db": {"median": 6.5, "p10": 2.0, "p90": 9.8, "samples": 1211},
     "rssi_dbm": {"median": -101.0, "samples": 1211},
     "first_seen": "2026-11-08T14:00:31Z", "last_seen": "2026-11-09T13:59:58Z"}
  ]
}
```
- `role`: `repeater` | `companion` | `room` | `sensor` | `unknown`. `unknown` means never advertised: either seen only
  as a hop hash (id = that 2–6 hex hash) or an observer known from its MQTT topic (normal 8-hex id). An advert-less
  observer counts as a relay only with self-echo evidence (see `analytics/README.md`).
- `confidence`: probability the link exists given the hop-hash ambiguity (see `analytics/` README).
- `snr_db` / `rssi_dbm`: only from receptions where this link was the **last hop** into an observer (the only
  hop the radio measures) or from TRACE packets. `null` if never measured.

### `GET /api/v1/regions/{region}/resilience`
Single points of failure in the relay network. Source: the hourly topology job (Phase 3).
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T14:00:00Z",
           "window": {"from": "2026-11-08T14:00:00Z", "to": "2026-11-09T14:00:00Z"}, "source": "topology-job"},
  "baseline": {"nodes": 87, "reachable_pairs": 3610, "reachable_share": 0.97},
  "single_points_of_failure": [
    {"id": "7F3A0B12", "name": "SEA-RPT-14", "role": "repeater",
     "location": {"lat": 47.71, "lon": -122.30, "grid_deg": 0.01},
     "articulation_point": true,
     "reachable_pairs_lost": 612, "reachable_share_after": 0.81,
     "nodes_cut_off": 14, "confidence": 0.88}
  ],
  "bridges": [
    {"a": "7F3A0B12", "b": "1C44D0E9", "reachable_pairs_lost": 340, "confidence": 0.91}
  ]
}
```
- Reachability counts **relay-aware** paths: only repeaters forward floods, so a companion never connects two others.
- `single_points_of_failure` is sorted by `reachable_pairs_lost`, descending. Only nodes that lose > 0 pairs are listed.
- `nodes_cut_off`: nodes that lose all reachability to the **largest** relay-connected group when this node is removed.
  A SPOF inside a smaller island can lose many pairs yet cut 0 nodes off from the main group; read it together with
  `reachable_pairs_lost`.
- `confidence`: the lowest confidence among the links the finding depends on.

### `GET /api/v1/regions/{region}/links/residuals`
Predicted vs observed SNR per measured link. Source: the nightly propagation job (Phase 4).
Query: `sort=residual|abs_residual|observations` (default `abs_residual`), `limit` (default 100, max 1000).
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T03:00:00Z",
           "window": {"from": "2026-11-02T03:00:00Z", "to": "2026-11-09T03:00:00Z"}, "source": "propagation-job"},
  "model": {"name": "fspl+knife-edge", "version": "1", "dem": "Copernicus GLO-30"},
  "summary": {"links": 198, "median_abs_residual_db": 4.1, "rmse_db": 6.3, "bias_db": -1.2},
  "links": [
    {"a": "351FC1F9", "b": "3491927B", "distance_km": 8.4,
     "predicted_snr_db": 3.2, "observed_snr_db": 6.5, "residual_db": 3.3, "samples": 1211,
     "line_of_sight": false, "fresnel_clearance": 0.42, "diffraction_loss_db": 9.7, "confidence": 0.97}
  ]
}
```
- `residual_db = observed − predicted`. Positive means the link is better than the model expects.

### `GET /api/v1/regions/{region}/links/surprising`
Where the model and reality disagree most. Source: the nightly propagation job (Phase 4).
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T03:00:00Z",
           "window": {"from": "2026-11-02T03:00:00Z", "to": "2026-11-09T03:00:00Z"}, "source": "propagation-job"},
  "observed_but_predicted_impossible": [
    {"a": "351FC1F9", "b": "9E10AA03", "distance_km": 31.2, "predicted_snr_db": -19.5,
     "observed_snr_db": 1.0, "samples": 42, "confidence": 0.74,
     "possible_reasons": ["hop-hash misattribution", "ducting", "DEM misses a tall mast"]}
  ],
  "predicted_strong_but_never_seen": [
    {"a": "3491927B", "b": "C0FFEE01", "distance_km": 2.1, "predicted_snr_db": 11.0,
     "both_active": true, "possible_reasons": ["building clutter", "antenna/feedline fault", "node misconfigured"]}
  ]
}
```

### `GET /api/v1/regions/{region}/placement`
Where the next repeater would help most. Source: the nightly placement job (Phase 5).
Query: `top` (default 10, max 50).
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T04:00:00Z",
           "window": {"from": "2026-11-02T04:00:00Z", "to": "2026-11-09T04:00:00Z"}, "source": "placement-job"},
  "method": {"grid_deg": 0.01, "candidates_scored": 1600, "antenna_height_m": 10, "model": "fspl+knife-edge v1"},
  "candidates": [
    {"rank": 1, "location": {"lat": 47.68, "lon": -122.21, "grid_deg": 0.01},
     "score": 0.83,
     "coverage_gain_nodes": 11,
     "spofs_removed": ["7F3A0B12"],
     "reachable_pairs_gained": 540,
     "new_links": [{"to": "351FC1F9", "predicted_snr_db": 5.5}, {"to": "1C44D0E9", "predicted_snr_db": 2.0}]}
  ]
}
```
- Candidate locations are grid cells by construction, so they need no snapping.

### `GET /` and `/static/*`
The Leaflet map page (Phase 5). It only calls the endpoints above.

## Versioning
Additive changes (new fields) keep `v1`; clients ignore unknown fields. Removing or renaming a field, or
changing its meaning, means `/api/v2` and a bump of `meta.schema_version`.
