"""Where would one more repeater help most? Score every 0.01 degree grid cell as a site.

For each candidate cell: predict links to every located node within 30 km with the
propagation model, add the candidate to the link graph as a relay with those links, and
measure what the relay-aware reachability (resilience.py) gains. Candidates are grid-cell
centres by construction, the same cells `geo.snap` labels, so their locations are public.
"""

from dataclasses import dataclass, replace
from typing import Any

import networkx as nx
import numpy as np

from .geo import GRID_DEG, haversine_km
from .pipeline import TopologyRun
from .propagation import PropagationModel, Radio, Site
from .resilience import Spof, main_group, reach_sets, reachable_pairs
from .summaries import meta, public_id
from .terrain import Terrain

SOURCE = "placement-job"
MAX_LINK_DISTANCE_KM = 30.0
# A new link needs this much SNR above the demodulation floor: a fade margin for a real
# link, and because the model is optimistic (one knife edge, no clutter).
LINK_MARGIN_DB = 5.0
# shortcut: elevation <= 0.5 m is taken as open water (Puget Sound); lakes above sea level
# (Lake Washington, ~6 m) pass. A land-cover mask is the upgrade.
WATER_MAX_ELEVATION_M = 0.5
SPOF_CHECK_TOP_K = 50  # the SPOF re-check costs one reachability pass per SPOF
DEFAULT_TOP = 50  # the API's maximum `top`
SCORE_PAIRS_WEIGHT = 0.7
SCORE_SPOFS_WEIGHT = 0.3
CANDIDATE = "candidate:"  # graph key of the hypothetical node; never a real key


@dataclass(frozen=True)
class Candidate:
    lat: float  # grid-cell centre
    lon: float
    new_links: list[tuple[str, float]]  # (full key, predicted SNR dB), strongest first
    reachable_pairs_gained: int
    coverage_gain_nodes: int
    spofs_removed: list[str]  # full keys
    score: float = 0.0


def grid_cells(points: list[tuple[float, float]]) -> list[tuple[float, float]]:
    """Every 0.01 degree cell centre in the points' bounding box, padded by one cell.

    Cells are integer multiples of 0.01, written as i / 100: the same float `round(x, 2)`
    gives, so a cell equals `geo.snap` of any point inside it.
    """
    if not points:
        return []
    steps = round(1 / GRID_DEG)
    lat_i = [round(p[0] * steps) for p in points]
    lon_i = [round(p[1] * steps) for p in points]
    return [
        (i / steps, j / steps)
        for i in range(min(lat_i) - 1, max(lat_i) + 2)
        for j in range(min(lon_i) - 1, max(lon_i) + 2)
    ]


def on_land(cells: list[tuple[float, float]], terrain: Terrain) -> list[tuple[float, float]]:
    """Drop cells whose centre is at or below WATER_MAX_ELEVATION_M (open water)."""
    if not cells:
        return []
    lat, lon = np.array(cells).T
    z = terrain.elevation_m(lat, lon)
    return [c for c, h in zip(cells, z, strict=True) if h > WATER_MAX_ELEVATION_M]


def predict_links(
    cell: tuple[float, float], sites: dict[str, Site], model: PropagationModel, radio: Radio
) -> list[tuple[str, float]]:
    """Nodes within 30 km the model says a repeater in this cell would reach with margin."""
    here = Site(cell[0], cell[1], radio.infrastructure_height_m)
    links = []
    for key, s in sites.items():
        if haversine_km(here.lat, here.lon, s.lat, s.lon) > MAX_LINK_DISTANCE_KM:
            continue
        snr = model.predict(here, s).predicted_snr_db
        if snr >= radio.snr_floor_db + LINK_MARGIN_DB:
            links.append((key, snr))
    return sorted(links, key=lambda x: (-x[1], x[0]))


def with_candidate(g: "nx.Graph[str]", links: list[tuple[str, float]]) -> "nx.Graph[str]":
    h = g.copy()
    h.add_node(CANDIDATE, relay=True)
    h.add_edges_from((CANDIDATE, key) for key, _ in links)
    return h


def existing_pairs(reach: dict[str, set[str]]) -> int:
    """Reachable pairs, not counting pairs with the candidate (it must not inflate scores)."""
    return reachable_pairs(reach) - len(reach.get(CANDIDATE, ()))


def pairs_lost(g: "nx.Graph[str]", node: str) -> int:
    """Pairs among the other existing nodes lost when `node` is removed (as resilience.analyse)."""
    reach = reach_sets(g)
    h = g.copy()
    h.remove_node(node)
    return existing_pairs(reach) - len(reach[node] - {CANDIDATE}) - existing_pairs(reach_sets(h))


def network_gain(g: "nx.Graph[str]", links: list[tuple[str, float]]) -> tuple[int, int]:
    """(reachable pairs gained, nodes newly in the largest relay group) from one candidate."""
    if not links:
        return 0, 0
    h = with_candidate(g, links)
    gained = existing_pairs(reach_sets(h)) - existing_pairs(reach_sets(g))
    joined = (main_group(h) - {CANDIDATE}) - main_group(g)
    return gained, len(joined)


def spofs_removed(
    g: "nx.Graph[str]", links: list[tuple[str, float]], spofs: list[Spof]
) -> list[str]:
    """Current SPOFs whose removal loses no pairs any more once the candidate is added."""
    if not links:
        return []
    h = with_candidate(g, links)
    return [s.node for s in spofs if pairs_lost(h, s.node) == 0]


def recommend(
    g: "nx.Graph[str]",
    sites: dict[str, Site],
    cells: list[tuple[float, float]],
    spofs: list[Spof],
    model: PropagationModel,
    radio: Radio,
    top: int = DEFAULT_TOP,
    spof_check_top_k: int = SPOF_CHECK_TOP_K,
) -> list[Candidate]:
    """Score every cell, best first. Pure apart from the model's own counters.

    `sites` holds the located graph nodes; `spofs` the current single points of failure.
    """
    scored = []
    for cell in cells:
        links = predict_links(cell, sites, model, radio)
        gained, joined = network_gain(g, links)
        scored.append(Candidate(cell[0], cell[1], links, gained, joined, []))

    # shortcut: only the top K by pairs gained get the (costly) SPOF re-check, so a cell
    # that adds no pairs but would remove a SPOF can be missed when more than K cells tie.
    # More links first among ties: more parallel paths, more likely to remove a SPOF.
    scored.sort(key=lambda c: (-c.reachable_pairs_gained, -len(c.new_links), c.lat, c.lon))
    for i in range(min(spof_check_top_k, len(scored))):
        c = scored[i]
        scored[i] = replace(c, spofs_removed=spofs_removed(g, c.new_links, spofs))

    max_gained = max((c.reachable_pairs_gained for c in scored), default=0)
    scored = [replace(c, score=score(c, max_gained, len(spofs))) for c in scored]
    scored.sort(key=lambda c: (-c.score, -c.reachable_pairs_gained, c.lat, c.lon))
    return scored[:top]


def score(c: Candidate, max_gained: int, total_spofs: int) -> float:
    """0.7 x pairs gained / best pairs gained + 0.3 x SPOFs removed / all SPOFs, in [0, 1]."""
    pairs = c.reachable_pairs_gained / max_gained if max_gained > 0 else 0.0
    return SCORE_PAIRS_WEIGHT * pairs + SCORE_SPOFS_WEIGHT * len(c.spofs_removed) / max(
        1, total_spofs
    )


# --- JSON (docs/api-contract.md) ----------------------------------------------------------


def placement_json(
    run: TopologyRun, candidates: list[Candidate], method: dict[str, Any]
) -> dict[str, Any]:
    return {
        "meta": meta(run, SOURCE),
        "method": method,
        "candidates": [
            {
                "rank": i,
                # Already a grid-cell centre (grid_cells): no raw point to snap.
                "location": {"lat": c.lat, "lon": c.lon, "grid_deg": GRID_DEG},
                "score": round(c.score, 3),
                "coverage_gain_nodes": c.coverage_gain_nodes,
                "spofs_removed": [public_id(k) for k in c.spofs_removed],
                "reachable_pairs_gained": c.reachable_pairs_gained,
                "new_links": [
                    {"to": public_id(k), "predicted_snr_db": round(snr, 1)}
                    for k, snr in c.new_links
                ],
            }
            for i, c in enumerate(candidates, start=1)
        ],
    }
