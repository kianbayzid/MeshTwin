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
     "summaries": ["topology", "resilience", "residuals", "surprising", "profiles", "placement", "terrain"]}
  ]
}
```
- `updated_at` is the newest `generated_at` among the **data** summaries. Terrain doesn't count (it changes only with
  the DEM), so a region with only `terrain` listed has `"updated_at": null`.
- `nodes` and `links` come from the topology summary and are `null` while it doesn't exist.

### `GET /api/v1/regions/{region}/topology`
The inferred link graph for the latest window. Source: the hourly topology job (Phase 3).
Query: `min_confidence` (0.5–1, default 0.5): drop less certain links. The topology job exports only links with
confidence ≥ 0.5, and resilience is computed on those, so the API can filter upward but not below 0.5.
Filtering only removes links: node `degree` and `stats` stay as the job computed them (the slider declutters the map; the API is not a second analytics engine).
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
- `fresnel_clearance` is the smallest (ground clearance below the ray) / (first Fresnel zone radius) along the path:
  a ratio, not a percentage. Negative means the ground blocks the line of sight; ≥ 0.6 is the usual "clear" rule.
- All `sort` orders are descending: `residual` puts links much better than the model first; ties break on `a`, then `b`.
- `summary.median_abs_residual_db`, `rmse_db` and `bias_db` are `null` when `links` is 0 (a 0 would claim a perfect fit).
- `model.dem` is `"Copernicus GLO-30"` or `"flat (no DEM)"` when the job ran without terrain.
- Published `distance_km` is measured between the **snapped** cell centres (0.1 km), so it reveals nothing finer than `location`.

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

### `GET /api/v1/regions/{region}/links/{a}/{b}/profile`
The terrain profile under one link, for the link inspector's chart. Source: the nightly propagation job, which
writes all profiles to `summaries/region={REGION}/profiles.json` (shape below); the API returns one entry.
`{a}` and `{b}` are node ids (2–8 hex, as in topology) in either order. 404 `not_found` if the pair has no profile.
The response keeps the stored orientation: `profile.a` is always index 0, whichever order the URL used.
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T03:00:00Z",
           "window": {"from": "2026-11-02T03:00:00Z", "to": "2026-11-09T03:00:00Z"}, "source": "propagation-job"},
  "model": {"name": "fspl+knife-edge", "version": "1", "dem": "Copernicus GLO-30"},
  "params": {"freq_mhz": 910.525, "k_factor": 1.333, "grid_deg": 0.01},
  "profile": {
    "a": "351FC1F9", "b": "3491927B", "distance_km": 8.4,
    "antenna_a_m": 10.0, "antenna_b_m": 10.0,
    "ground_m": [52.1, 54.0, 61.3, 58.2],
    "bulge_m": [0.0, 1.5, 1.5, 0.0],
    "fresnel_r1_m": [0.0, 15.4, 15.4, 0.0],
    "line_of_sight": false, "fresnel_clearance": -0.31, "diffraction_loss_db": 8.9
  }
}
```
- **Privacy: the path runs between the two snapped 0.01° cell centres, not the nodes' exact positions.** An elevation
  sequence along the exact path could be matched against the public DEM to recover a location, so exact paths never
  leave analytics. `distance_km` is the same snapped distance as in residuals (0.1 km).
- Arrays have the same length `n` (2 ≤ n ≤ 256), sampled at equal spacing from `a` (index 0) to `b` (index n−1):
  sample `i` is at `distance_km × i / (n − 1)`. Values in metres, one decimal.
  - `ground_m`: DEM elevation (same GLO-30 + `GridTerrain` interpolation as the model).
  - `bulge_m`: effective Earth-curvature rise `d1·d2 / (2·k·R)`; the chart draws terrain as `ground_m + bulge_m`.
  - `fresnel_r1_m`: first Fresnel zone radius `sqrt(λ·d1·d2/d)`; 0 at both ends.
- Line of sight runs from `ground_m[0] + antenna_a_m` to `ground_m[n−1] + antenna_b_m`.
- `line_of_sight`, `fresnel_clearance`, `diffraction_loss_db` are the model's values **for this grid-cell path**, computed
  by the same function the model uses. They can differ from the same link's numbers in `links/residuals`, which come
  from exact positions. The UI says so.
- Profiles exist for topology links whose two nodes have locations in **different** cells (same-cell links have no
  meaningful profile).
- `profiles.json` is `{"meta", "model", "params", "profiles": [<profile>, ...]}`; it is not served whole.
  All profile fields are required and non-null; an empty `profiles` list is valid (every lookup is then 404).
  Ids in the URL match case-insensitively. Written as compact JSON: the 8 MB summary cap is about 750 profiles
  of 256 samples when indented, and an oversized file is refused whole (lookups 503), never truncated.

### `GET /api/v1/regions/{region}/placement`
Where the next repeater would help most. Source: the nightly placement job (Phase 5).
Query: `top` (default 10, max 50).
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-09T04:00:00Z",
           "window": {"from": "2026-11-02T04:00:00Z", "to": "2026-11-09T04:00:00Z"}, "source": "placement-job"},
  "method": {"grid_deg": 0.01, "candidates_scored": 1600, "antenna_height_m": 10, "model": "fspl+knife-edge v1",
             "dem": "Copernicus GLO-30", "spofs_checked_top": 50},
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
- `score` is in [0, 1] and relative to the run: 0.7 × pairs gained / best pairs gained + 0.3 × SPOFs removed / total SPOFs.
- `spofs_removed` is computed only for the `method.spofs_checked_top` cells with the most pairs gained; other cells show `[]` (never over-claims).
- `method.dem` is `"Copernicus GLO-30"` or `"flat (no DEM)"`, as in residuals.

### `GET /api/v1/regions/{region}/terrain.json`
Metadata for the 3D map's elevation tiles. Source: the terrain job (`meshtwin-analytics terrain-tiles`, Console
Phase 2). The tiles are sampled from **the same GLO-30 mosaic and bilinear interpolation (`GridTerrain`) the
propagation model uses**, so the 3D map shows the ground the model saw.
```json
{
  "meta": {"schema_version": 1, "region": "SEA", "generated_at": "2026-11-11T03:00:00Z",
           "window": null, "source": "terrain-job"},
  "encoding": "mapbox", "tile_size": 256, "minzoom": 8, "maxzoom": 12,
  "bounds": [-122.6, 47.3, -121.8, 47.9],
  "dem": "Copernicus GLO-30",
  "tiles": 112
}
```
- `meta.window` is `null`: terrain isn't data from a time window. This is the only summary where it may be null.
- `bounds` is `[west, south, east, north]` in degrees, snapped **outward** to 0.1°. It's the coverage box, not a node location.
- `encoding: "mapbox"` (Terrain-RGB): `elevation_m = -10000 + (R × 65536 + G × 256 + B) × 0.1`.
- `tiles` is the number of PNG tiles written (for the readiness check and benchmarks).
- `dem` is always `"Copernicus GLO-30"`: the job refuses to run without a DEM rather than publish flat tiles.

### `GET /api/v1/regions/{region}/terrain/{z}/{x}/{y}.png`
One 256 × 256 Terrain-RGB PNG in XYZ (slippy-map, Web Mercator) numbering. Stored at
`summaries/region={REGION}/terrain/{z}/{x}/{y}.png`.
- `z`, `x`, `y` are decimal integers with `0 ≤ z ≤ 14` and `0 ≤ x, y < 2^z`; anything else is `400 bad_request`.
- A tile outside `bounds` or the zoom range is `404 not_found` (the map then shows no relief there; it is not an error for the UI).
- The API serves the bytes as `image/png` only after checking the PNG signature and a 1 MB size cap. Same `ETag`
  rule as JSON; `Cache-Control: max-age=3600` (terrain changes only when the DEM does).
- Privacy: elevation tiles contain no node data. They are still limited to the region's bounds.

### `GET /` and `/static/*`
The Leaflet map page (Phase 5). It only calls the endpoints above.

## Versioning
Additive changes (new fields) keep `v1`; clients ignore unknown fields. Removing or renaming a field, or
changing its meaning, means `/api/v2` and a bump of `meta.schema_version`.
