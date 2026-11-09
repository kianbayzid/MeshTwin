# MeshTwin analytics

Python (uv) jobs that turn the ingester's Parquet into a link graph, a resilience
report and JSON summaries for the API. Phase 3: topology and resilience.

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

## Future work
- EM-lite prior: after one pass, boost candidates that were resolved often, then resolve again.
  It would help exactly the `60` case above. Skipped to keep the first version explainable.
- Asymmetric links (the model is undirected), TRACE per-hop SNR, direct-route `path[0]` hints.
