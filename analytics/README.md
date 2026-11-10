# MeshTwin analytics

Python (uv) jobs that turn the ingester's Parquet into a link graph, a resilience
report and JSON summaries for the API. Phase 3: topology and resilience. Phase 4: terrain
propagation and predicted-vs-observed SNR. Phase 5: where to put the next repeater.

```sh
uv sync
uv run meshtwin-analytics topology --root ../testdata/parquet/sea-seed1-2h-hash1 --region SEA --out ./out
uv run meshtwin-analytics evaluate --root ../testdata/parquet/sea-seed1-2h-hash1 \
    --truth ../testdata/parquet/sea-seed1-2h-hash1/truth.json
uv run ruff check . && uv run ruff format --check . && uv run mypy && uv run pytest
```

`--root`/`--out` take a directory or `s3://bucket` (env `S3_ENDPOINT` host:port, `S3_ACCESS_KEY`,
`S3_SECRET_KEY`, `S3_TLS`, the same as the ingester). The window is `--from/--to` (ISO 8601,
`[from, to)`) or `--window 24h` ending at the newest `rx_time` in the data. `--advert-lookback 7d`
sets how far back the node registry looks (see below). `--min-confidence 0.5` drops weaker links.

| module | does |
|---|---|
| `storage.py` | local vs S3: DuckDB httpfs secret, pyarrow output filesystem |
| `load.py` | DuckDB SQL: flood receptions in the window, latest advert per key, counts |
| `geo.py` | distance, the link prior, **`snap`** (the only place coordinates are rounded) |
| `resolve.py` | hop candidates, forward-backward, the chain memo |
| `aggregate.py` | per-link evidence, the networkx graph |
| `resilience.py` | relay-aware reachability, SPOFs, bridges |
| `summaries.py` / `output.py` | contract JSON + internal Parquet, and writing them |
| `evaluate.py` | scoring against `truth.json` |
| `terrain.py` | `Terrain` interface: flat, grid (bilinear), Copernicus GLO-30 tiles + download |
| `propagation.py` | `Radio` assumptions, `PropagationModel` interface, `FsplKnifeEdge` |
| `compare.py` | residuals per measured link, surprising pairs, their JSON |
| `placement.py` | candidate grid cells, predicted new links, reachability gain, score, JSON |
| `pipeline.py` / `cli.py` | wiring, timings, command line |

## The algorithm in plain words

### 1. Which receptions are evidence
Only **flood** receptions (`route_type` 0/1), not TRACE. In a flood every repeater that forwards
appends its key prefix, so the path is the relays actually crossed, oldest first. Direct-route
paths are hops *still to go*, so they say nothing about links already used (`docs/protocol.md` §2).

Each reception becomes a **chain**: `[origin] + path_hops + [observer]`. The origin is used only
when `src_id` is a full 64-hex key (ADVERT, ANON_REQ). A 2-hex `src_id` would just be one more
ambiguous hash, with no relay-tier rule to help, so it is left out.

### 2. Who the nodes are (the registry)
The latest advert per public key gives role, name and position (position may be missing).
Every observer is a known node even without an advert, with role `unknown`. It counts as a relay
(and a first-tier hop candidate) only on **self-evidence**: in packets *it* heard, its own prefix
sits before the last hop (it relayed, a neighbour relayed again, it heard the echo). A hop hash
anywhere else could be someone else's key, so with 1-byte hashes we require
`SELF_ECHO_MIN_RECEPTIONS = 3` such receptions, or one with a 2+ byte hop. An advert always wins.

**Advert lookback.** Repeaters send *flood* adverts only every **47 h** by default; zero-hop
adverts reach one hop and companions advert only when the user asks. A 24 h link window would miss
many positions and roles, so the registry reads adverts over `--advert-lookback` (default 7 days)
ending at the window end, while links use only `--window`. On the 2 h fixtures both cover all data.

### 3. Hop candidates
A hop `h` (1-3 bytes as hex) matches every known node whose key starts with `h`. Tiers:
1. known nodes with a relay role (`RELAY_ROLES = {repeater}`, firmware default);
2. if none: known nodes of any other role (an owner can switch forwarding on for rooms,
   sensors, companions), log prior `log(0.1)`;
3. if none: a placeholder node `unknown:<HEX>` with role `unknown`.

Hops in one packet share one width (from `path_len`) but widths can differ between packets;
prefix matching handles both.

### 4. Resolving a chain jointly (forward-backward)
Treat the chain like an HMM. Position *i* has states = its candidates (an endpoint has one). A
path through the chain picks one node per position and scores

```
score(x) = Σ_i u(x_i) + Σ_i ψ(x_i, x_{i+1})
ψ(a, b)  = log p(d(a, b)),   p(d) = 1 / (1 + exp((d − D50) / S)),   D50 = 15 km, S = 4 km
```

`u` is the tier prior. `p(d)` is a logistic "how plausible is a radio link this long" curve:
p(2 km) ≈ 0.96, p(15 km) = 0.5, p(30 km) ≈ 0.02, p(80 km) ≈ 1e-7. When either node has no
position, `p = 0.5` (neutral). When a node follows itself (an observer hearing its own relayed
copy) the pair scores `log 1 = 0` and yields no link.

P(x) ∝ exp(score(x)). **Forward-backward** sums over all paths in O(n·k²) instead of kⁿ:

```
α_0 = u_0,      α_i(b) = u_i(b) + logsumexp_a [α_{i-1}(a) + ψ(a, b)]
β_{n-1} = 0,    β_i(a) = logsumexp_b [ψ(a, b) + u_{i+1}(b) + β_{i+1}(b)]
P(x_i = a)               = exp(α_i(a) + β_i(a) − log Z),         log Z = logsumexp α_{n-1}
P(x_i = a, x_{i+1} = b)  = exp(α_i(a) + ψ(a, b) + u_{i+1}(b) + β_{i+1}(b) − log Z)
```

All in log space with logsumexp (factor out the max), so long chains cannot underflow. Viterbi
would give one best guess; we want **probabilities** so every link carries a confidence. A test
checks the marginals against brute-force enumeration.

Joint, not hop by hop: a hop's neighbours on both sides vote. If hop `AB` could be a repeater
2 km from the next hop or one 40 km away, the near one wins even if each hop alone looks fine.

**Memo.** Identical chains (same path, origin, observer) repeat many times: every reception of
one advert by one observer through the same relays. Each distinct chain is solved once
(`HopResolver._memo`). On the fixtures 93% of receptions are cache hits (4,825 distinct chains
for 72,454 flood receptions). That is the main performance lever.

### 5. From chains to links
For each reception and each undirected pair (a, b) in its chain, `p` = the pair posterior (max
over positions if the pair occurs twice, e.g. an echo). Per link:

- `observations = Σ p` over receptions: the expected number of receptions that crossed the link.
- `confidence = max p` over receptions: the strongest single piece of evidence.

Why max, not noisy-OR `1 − Π(1 − p_i)`: identical ambiguity repeats. On the hash1 fixture two
repeaters (`60C538…`, `605D52…`) share hash `60` and sit close together, so hundreds of receptions
each give ~0.5 to both readings. Those receptions are **not independent** evidence: they are the
same unresolved question asked again. Noisy-OR would push both readings to ~1.0 and publish false
links; max keeps them honestly at ~0.5. Max is monotone (more evidence never lowers it) and reads
as "at least one reception made us this sure". Repetition is still visible in `observations`.

**SNR/RSSI** come only from the last hop into the observer (`hk → observer`, or
`origin → observer` with an empty path), because the radio measures only that hop. A sample counts
when that pair's posterior is ≥ 0.5 (counted, not weighted, so medians and percentiles stay plain).
`first_seen`/`last_seen` span every reception that gave the pair any posterior (≥ 1e-3).

**Graph.** networkx `Graph`: links with `confidence ≥ min_confidence`, plus all observers. Nodes
with no surviving link are dropped. Pair posteriors below 1e-3 are discarded during resolution.

### 6. Resilience (relay-aware)
Only relays forward floods. A **relay** here is a node with a relay role, a placeholder (it was
seen in a flood path, so it forwarded), or a node of another role that some reception resolved as
a hop with posterior ≥ 0.5. Two nodes can talk iff they are radio neighbours, or both **touch**
(are in, or next to) the same connected component of the relay-only subgraph. A companion between
two repeater islands connects nothing.

1. Find relay components once; map each node to the components it touches; `reach(n)` = its
   neighbours ∪ everyone touching one of its components. Reachable pairs = Σ|reach(n)| / 2.
   Share = pairs / C(V, 2).
2. For each relay r: remove it and recompute. `reachable_pairs_lost` = pairs among the *other*
   nodes that were reachable and no longer are (pairs involving r itself don't count, or every
   relay would be a SPOF). `reachable_share_after` uses C(V−1, 2). `nodes_cut_off` = nodes that
   touched the largest relay component (the backbone) before and no longer touch the largest one
   after. Every r with lost > 0 is a SPOF, sorted by lost, descending.
3. `articulation_point`: networkx articulation points of the relay subgraph. A repeater that alone
   serves some companions is a SPOF without being an articulation point; the tests show both.
4. `bridges`: networkx bridges of the relay subgraph, each with the pairs lost if it fails.
5. Finding `confidence` = the lowest confidence among the links it depends on (the removed node's
   links, or the bridge link). A finding is only as trustworthy as its weakest link.

Cost: one pass is O(V + E) for components plus set unions bounded by V per node; R removals give
O(R × (V + E)) passes. 30 ms for 90 nodes; fine for the hundreds of nodes a region has.

## Outputs and privacy
- `results/topology_nodes|topology_links|resilience/region=SEA/date=…/hour=…/part-0.parquet`
  (hour of the window end). **Internal**: full keys and raw advert coordinates, for Phase 4's
  terrain model. These must stay in the private bucket and never be served.
- `summaries/region=SEA/topology.json`, `resilience.json`: exactly `docs/api-contract.md`. Ids are
  the first 8 hex of the key (a placeholder keeps its hop hex), names only for repeaters and room
  servers, every location through `geo.snap` (0.01° cell, 2 decimals) or `null`. Tests assert no
  raw coordinate, no full key and no companion name appears, and that every contract field is
  present with the right type (the contract examples are parsed from the markdown).

## Evaluation against ground truth
`evaluate` compares the inferred graph with `truth.json`. Limitation: truth lists every radio link,
but flood paths can only reveal links a flood crossed. A companion–companion link never is
(companions don't relay; the observers are repeaters), so plain recall is capped well below 1.
We therefore also report recall over truth links touching ≥ 1 relay and over relay–relay links.
SPOFs: the same `resilience.analyse` runs on the truth graph (relay = role repeater) and the two
SPOF sets are compared.

| seed 1, 2 h, 75,571 receptions | hash1 (1-byte) | hash3 (3-byte) |
|---|---:|---:|
| ambiguous hop entries | 6,961 / 184,490 (3.8%) | 0 |
| link precision (min_confidence 0.5) | 0.994 (315/317) | 1.000 (325/325) |
| link recall, all truth links | 0.645 | 0.666 |
| recall, links touching a relay | 0.966 | 0.997 |
| recall, relay–relay links | 0.988 | 1.000 |
| SPOF precision / recall | 1.0 / 1.0 (6/6) | 1.0 / 1.0 (6/6) |
| SPOF top-5 overlap | 1.0 | 1.0 |
| reachable share, inferred vs truth | 0.582 vs 0.582 | 0.582 vs 0.582 |

The collision cost on hash1: both false links (confidence 0.525 and 0.502) and 10 of the 11
missed relay links involve the `60` pair of repeaters, which sit close together, so geography
can't separate them.

## Propagation (Phase 4)

```sh
uv run meshtwin-analytics fetch-dem --bbox 47.42,-122.40,47.79,-122.03 --dem-dir "$LOCALAPPDATA/meshtwin/dem"
uv run meshtwin-analytics propagation --root ../testdata/parquet/sea-seed1-2h-hash1 --region SEA \
    --dem-dir "$LOCALAPPDATA/meshtwin/dem" --out ./out
```

`propagation` takes the same window options as `topology`, runs the topology inference once, and
writes `summaries/region=SEA/residuals.json` and `surprising.json` (`docs/api-contract.md`). Without
`--dem-dir` the ground is flat (sea level) and `model.dem` says `"flat (no DEM)"`. `fetch-dem`
downloads the 1°×1° tiles covering the box from AWS Open Data (one at a time, to `.part` then
renamed, skipping tiles already there; a 404 means open sea and is skipped). **Keep the cache
outside the repo; DEM tiles are never committed.**

**Reader: `tifffile` + `imagecodecs`.** GLO-30 COGs use DEFLATE with the floating-point predictor,
which tifffile decodes only with imagecodecs; both are pure wheels that load fine under Windows
Application Control, whereas rasterio drags in all of GDAL for reading one array.

### The model in plain words
For each pair of sites:
1. **Profile.** Sample the ground every ~30 m (the DEM resolution, capped at 2,000 points) on the
   straight lat/lon line between them; add the Earth's bulge `d1·d2 / (2·k·R)` with k = 4/3
   (refraction makes the Earth look flatter, ~8,500 km radius, ITU-R P.526-16 §1).
2. **The ray.** A straight line from antenna A (ground + height) to antenna B.
3. **Clearance.** At every point: how far the ground is below the ray, divided by the radius of the
   first Fresnel zone `sqrt(λ·d1·d2/d)`. `fresnel_clearance` is the minimum (negative = blocked);
   `line_of_sight` = no point above the ray. 0.6 or more is the classic "clear" rule.
4. **One knife edge.** The worst point becomes a knife edge with
   `v = h·sqrt(2(d1+d2)/(λ·d1·d2))` (P.526-16 Eq. 26; equal to `−√2 × clearance`), and loss
   `J(v) = 6.9 + 20·log10(sqrt((v−0.1)²+1) + v − 0.1)` for v > −0.78, else 0 (Eq. 31). J(0) = 6 dB:
   an edge exactly on the ray halves the field.
5. **Link budget.** `path loss = FSPL + J`, `FSPL = 20·log10(d_km) + 20·log10(f_MHz) + 32.44`,
   `predicted SNR = TX + gains − path loss − noise floor`, noise floor = `−174 + 10·log10(BW) + NF`.

The model is behind `PropagationModel.predict(a: Site, b: Site)`, so a better one (Deygout,
Longley-Rice) drops in without touching `compare.py`.

| assumption | value | source |
|---|---|---|
| frequency / bandwidth / SF / CR | 910.525 MHz / 62.5 kHz / 7 / 4/5 | MeshCore US preset (api.meshcore.nz/api/v1/config, MeshCore docs/faq.md §2.3) |
| TX power | 22 dBm | `LORA_TX_POWER`, heltec_v3 and rak4631 `platformio.ini` |
| noise figure | 6 dB → noise floor −120.0 dBm | derived from SX1262 datasheet Table 3-8 sensitivity |
| demodulation floor | SF7 −7.5 … SF12 −20 dB | SX1262 datasheet Rev 1.2 Table 6-1 |
| antenna gain | 0 dBi each end | **UNVERIFIED** site assumption |
| antenna height | repeater/room 10 m; companion/sensor/unknown 2 m | **UNVERIFIED** site assumption |

### Residuals and surprises
- **Residuals:** every exported link (confidence ≥ 0.5) with measured last-hop SNR and both ends
  located: `residual = observed median − predicted`. Summary: count, median |residual|, RMSE, bias
  (mean residual). Sorted by |residual|, worst first.
- **Observed but predicted impossible:** measured links the model puts below the demodulation floor.
- **Predicted strong but never seen:** pairs of relays (repeaters + inferred relays), both located,
  both heard in the window, ≤ 30 km apart, predicted ≥ floor + 10 dB, with **no** inferred link at
  any confidence. Top 50 by predicted SNR.
- Reasons are a fixed list of hypotheses to check, not a diagnosis.

**Privacy.** Ids and roles follow the topology rules. `distance_km` is measured between the
**snapped** cell centres and rounded to 0.1 km, so it says nothing finer than the published grid
(a test recomputes it from `topology.json` cells). The predicted SNR, though, is computed from the
exact positions: a weak side channel (moving a node within its cell shifts the prediction by
up to a few dB on short links; recovering a point from it would need the model, the DEM and
several links to that node), accepted.

### Limitations
- **One edge.** Several ridges in a row are under-counted (predicted too strong); Deygout is the
  upgrade. A knife edge also under-states smooth-Earth loss (60 km at 2 m: 14 dB).
- **No clutter.** No buildings or vegetation loss beyond what the DEM contains.
- **The DEM is a surface model (DSM):** it includes tree and roof tops in places, so in forested
  Seattle it over-predicts blockage for antennas below the canopy.
- **Unknown real antennas.** Heights, gains and cable losses vary per install; that appears as a
  bias term, and per-node residuals are where to look for it.
- **Observed SNR saturates** near +14 dB on SX126x radios, so strong links always look worse than
  predicted.

### Measured (seed 1, 2 h, hash1 fixture)
| | flat | Copernicus GLO-30 |
|---|---:|---:|
| residual links | 85 | 85 |
| median \|residual\| / RMSE / bias (dB) | 29.8 / 30.7 / −30.3 | 14.6 / 19.1 / −14.9 |
| line of sight | 85 | 18 |
| observed but predicted impossible | 0 | 1 |
| predicted strong but never seen (cap 50) | 50 | 50 |
| propagation step (410 profiles) | 0.035–0.05 s (~8–12k profiles/s) | 0.06–0.07 s (~5.5–7k profiles/s) + 0.15 s DEM load |
| `propagation` wall clock (incl. topology) | 2.0 s | 2.2 s |

The synthetic generator uses log-distance with exponent 7.5 and no terrain, so the large negative
bias on synthetic data is expected; the model is **not** tuned to it. What the fixture does validate
is the observed half: the observed median per link matches the generator's true link SNR
(clamped at 14 dB) with a median absolute difference of **0.063 dB** (84 of 85 links are true
links). The predicted half is validated by the physics tests (FSPL, J(v), ridge, Earth bulge).

### DEM attribution
> produced using Copernicus WorldDEM-30 © DLR e.V. 2010-2014 and © Airbus Defence and Space GmbH 2014-2018 provided under COPERNICUS by the European Union and ESA; all rights reserved.
> The organisations in charge of the Copernicus programme by law or by delegation do not incur any liability for any use of the Copernicus WorldDEM-30.

(Licence COP-DEM-GLO-30-F; see `docs/data-sources.md`.)

## Placement (Phase 5)

```sh
uv run meshtwin-analytics placement --root ../testdata/parquet/sea-seed1-2h-hash1 --region SEA     --dem-dir "$LOCALAPPDATA/meshtwin/dem" --out ./out
```

Same window options as `topology`; writes `summaries/region=SEA/placement.json`
(`docs/api-contract.md`). Without `--dem-dir` the ground is flat and no water filter runs.

### The method in plain words
1. **Candidates.** Every 0.01° grid-cell centre in the bounding box of the located nodes, padded by
   one cell. With a DEM, cells whose centre is at or below 0.5 m are dropped as open water (Puget
   Sound). These are the same cells `geo.snap` labels, so a candidate's location is public by
   construction (a test checks `snap(cell) == cell`).
2. **New links.** A repeater at the cell (10 m antenna, `Radio.infrastructure_height_m`) is
   predicted against every located node within 30 km through the Phase 4 `PropagationModel`
   interface. A link exists if predicted SNR ≥ demodulation floor + **5 dB** (`LINK_MARGIN_DB`: a
   fade margin, and the model is optimistic).
3. **Effect.** The candidate joins the Phase 3 graph (links with confidence ≥ 0.5) as a relay with
   those links, and the relay-aware reachability is recomputed:
   - `reachable_pairs_gained`: reachable pairs after − before, counted among **existing** nodes
     only (the candidate's own pairs would reward linking to anything).
   - `coverage_gain_nodes`: existing nodes outside the largest relay-connected group before and
     inside it after.
   - `spofs_removed`: current SPOFs (Phase 3) whose removal loses 0 pairs once the candidate is
     there. One reachability pass per SPOF, so only the top 50 cells by pairs gained (ties: more
     new links first) are checked; `method.spofs_checked_top` says so.
4. **Score** in [0, 1]:
   `score = 0.7 × pairs_gained / max pairs_gained over all cells + 0.3 × spofs_removed / total SPOFs`.
   Reachability is what users feel, so it dominates; redundancy is the tie-breaker that matters.
   It is **relative to the run**: 1.0 means "best cell in this run", not "perfect".
5. **Output.** Top 50 (the API's maximum `top`) by score, then pairs gained, then lat, lon: fully
   deterministic. `new_links` strongest first, ids are 8-hex prefixes, SNR to one decimal.

Additive `method` fields (allowed by the contract's versioning rule): `dem` (`"Copernicus GLO-30"`
or `"flat (no DEM)"`) and `spofs_checked_top` (50).

### Assumptions and limitations
- **No land-use, permission or access constraints.** A cell is a cell: a lake above sea level
  (Lake Washington, ~6 m), a freeway or a park all pass. A real site needs a roof, power and an owner.
- **Candidates are rooftop-agnostic grid cells**: ground at the cell centre + 10 m, whatever the
  real building or hill top within the 1 km cell offers.
- **The model is optimistic** (one knife edge, no clutter; Phase 4 bias −15 dB with the DEM,
  −30 dB flat on the fixture). On this fixture the best cells reach 70+ of 73 located nodes, so many
  cells tie on pairs gained and the ranking is decided by SPOFs removed and the position tie-break.
  The 5 dB margin helps a little; calibrating the model on residuals is the real fix.
- One new repeater at a time (greedy): the best *pair* of sites is not searched.
- Nodes without a location can't get predicted links (they still count in the reachability).

### Measured (seed 1, 2 h, hash1 fixture)
| | flat | Copernicus GLO-30 |
|---|---:|---:|
| candidates scored | 1,520 | 1,400 (120 water cells dropped) |
| profiles | 101,322 | 94,260 |
| placement step | 6.7 s (~15k profiles/s) | 11.8 s (~8k profiles/s) + 0.19 s DEM load |
| `placement` wall clock (incl. topology) | ~9 s | ~14 s |

Single stage: well under the 60 s budget, so no distance/FSPL pre-filter beyond the 30 km cut.

**Truth check (qualitative, not tuned to it).** The inferred relay graph has two islands: the main
one (25 relays, around 47.65, −122.24) and an eastern one (5 relays, around 47.55, −122.07) that
holds the two biggest true SPOFs (SEA-RPT-03, SEA-RPT-01); 27 nodes are outside the main group, the
same as in `truth.json`. With the DEM, the top 5 cells (47.52–47.54, −122.09…−122.12) sit on the
eastern island's west edge, 4–5 km from RPT-01 and 5–7 km from RPT-03, and bridge it to the main
group (all 27 nodes join, +1,674 pairs) while removing 5 of the 6 SPOFs. Flat terrain puts rank 1
further west (47.52, −122.18), between the islands. Both are the places a human would look at
first; whether one repeater could really close a ~15 km gap is exactly what the optimistic model
can't promise.

## Future work
- EM-lite prior: after one pass, boost candidates that were resolved often, then resolve again.
  It would help exactly the `60` case above. Skipped to keep the first version explainable.
- Asymmetric links (the model is undirected), TRACE per-hop SNR, direct-route `path[0]` hints.
