"""Placement on hand-built graphs with a stub propagation model (no I/O, no DEM)."""

import random

import networkx as nx
import numpy as np
import pytest

from meshtwin_analytics.geo import haversine_km, snap
from meshtwin_analytics.placement import (
    Candidate,
    base_state,
    grid_cells,
    network_gain,
    on_land,
    predict_links,
    recommend,
)
from meshtwin_analytics.propagation import LinkPrediction, Radio, Site
from meshtwin_analytics.resilience import analyse, relay_graph

RADIO = Radio()


class StepModel:
    """Strong link (20 dB) up to `reach_km`, nothing beyond. Counts predictions."""

    name = "step"
    version = "0"

    def __init__(self, reach_km: float = 6.0) -> None:
        self.reach_km = reach_km
        self.profiles = 0

    def predict(self, a: Site, b: Site) -> LinkPrediction:
        self.profiles += 1
        d = haversine_km(a.lat, a.lon, b.lat, b.lon)
        snr = 20.0 if d <= self.reach_km else -30.0
        return LinkPrediction(d, 0.0, snr, True, 1.0, 0.0)


# Island A (lat 47.00): a triangle of relays, a companion heard by two of them: no SPOF.
# Island B (lat 47.10, 11 km north): one relay RB1 serving two companions: the one SPOF.
POS = {
    "RA1": (47.00, -122.00),
    "RA2": (47.00, -121.99),
    "RA3": (47.00, -121.98),
    "CA": (47.00, -122.01),
    "RB1": (47.10, -122.00),
    "CB1": (47.10, -122.01),
    "CB2": (47.10, -121.99),
}
BETWEEN = (47.05, -122.00)  # ~5.6 km from both islands
PARALLEL = (47.14, -122.00)  # ~4.4 km north of island B, ~15.5 km from A
FAR = (47.50, -122.00)  # ~45 km from everything


def _graph() -> tuple["nx.Graph[str]", dict[str, Site]]:
    g = relay_graph(
        {k: k.startswith("R") for k in POS},
        [
            ("RA1", "RA2", {}),
            ("RA2", "RA3", {}),
            ("RA3", "RA1", {}),
            ("RA1", "CA", {}),
            ("RA2", "CA", {}),
            ("RB1", "CB1", {}),
            ("RB1", "CB2", {}),
        ],
    )
    sites = {k: Site(lat, lon, RADIO.antenna_height_m("repeater")) for k, (lat, lon) in POS.items()}
    return g, sites


def _recommend(cells: list[tuple[float, float]]) -> dict[tuple[float, float], Candidate]:
    g, sites = _graph()
    res = analyse(g)
    out = recommend(g, sites, cells, res.spofs, StepModel(), RADIO)
    return {(c.lat, c.lon): c for c in out}


def test_fixture_has_one_spof() -> None:
    g, _ = _graph()
    assert [s.node for s in analyse(g).spofs] == ["RB1"]


def test_candidate_between_islands_joins_them() -> None:
    c = _recommend([BETWEEN, PARALLEL, FAR])[BETWEEN]
    assert {k for k, _ in c.new_links} == set(POS)
    # Before: A has 4 nodes (6 pairs), B 3 nodes (3 pairs). After: all 7 (21 pairs).
    assert c.reachable_pairs_gained == 21 - 6 - 3
    assert c.coverage_gain_nodes == 3  # island B joins the main group
    assert c.spofs_removed == ["RB1"]
    assert c.score == 1.0


def test_parallel_path_removes_the_spof() -> None:
    c = _recommend([BETWEEN, PARALLEL, FAR])[PARALLEL]
    assert {k for k, _ in c.new_links} == {"RB1", "CB1", "CB2"}
    assert c.reachable_pairs_gained == 0  # B could already talk to itself
    assert c.coverage_gain_nodes == 0
    assert c.spofs_removed == ["RB1"]
    assert c.score == pytest.approx(0.3)  # 0.7 x 0 pairs + 0.3 x 1 of 1 SPOF


def test_far_candidate_scores_zero_without_predicting() -> None:
    _, sites = _graph()
    model = StepModel(reach_km=1000)  # strong everywhere: only the 30 km cut stops it
    assert predict_links(FAR, sites, model, RADIO) == []
    assert model.profiles == 0
    c = _recommend([BETWEEN, PARALLEL, FAR])[FAR]
    assert (c.new_links, c.reachable_pairs_gained, c.coverage_gain_nodes) == ([], 0, 0)
    assert c.spofs_removed == [] and c.score == 0.0


def test_ranking_is_deterministic_and_scores_bounded() -> None:
    cells = grid_cells([*POS.values(), FAR])
    g, sites = _graph()
    spofs = analyse(g).spofs
    first = recommend(g, sites, cells, spofs, StepModel(), RADIO, top=1000)
    shuffled = cells[:]
    random.Random(1).shuffle(shuffled)
    again = recommend(g, sites, shuffled, spofs, StepModel(), RADIO, top=1000)
    assert first == again
    assert len(first) == len(cells)
    scores = [c.score for c in first]
    assert scores == sorted(scores, reverse=True)
    assert all(0.0 <= s <= 1.0 for s in scores)
    assert recommend(g, sites, cells, spofs, StepModel(), RADIO, top=5) == first[:5]


def test_precomputed_base_gives_identical_gain() -> None:
    """recommend() computes the base graph's state once; the result must not change."""
    g, sites = _graph()
    base = base_state(g)
    cells = grid_cells([*POS.values(), FAR])
    gains = [network_gain(g, predict_links(c, sites, StepModel(), RADIO)) for c in cells]
    assert any(gained > 0 for gained, _ in gains)  # the test has teeth
    for c, gain in zip(cells, gains, strict=True):
        assert network_gain(g, predict_links(c, sites, StepModel(), RADIO), base) == gain
    assert base_state(g) == base  # scoring candidates never touched the base graph


def test_cells_are_snapped_grid_cells_padded_by_one() -> None:
    points = [(47.6061, -122.3328), (47.6449, -122.2401)]
    cells = grid_cells(points)
    lats, lons = sorted({c[0] for c in cells}), sorted({c[1] for c in cells})
    assert (lats[0], lats[-1], lons[0], lons[-1]) == (47.6, 47.65, -122.34, -122.23)
    assert len(cells) == len(lats) * len(lons)
    for lat, lon in cells:
        assert snap(lat, lon) == {"lat": lat, "lon": lon, "grid_deg": 0.01}
        assert len(repr(lat).split(".")[-1]) <= 2 and len(repr(lon).split(".")[-1]) <= 2
    assert grid_cells([]) == []


class Heights:
    """Terrain stub: a fixed elevation per cell."""

    def __init__(self, z: dict[tuple[float, float], float]) -> None:
        self.z = z

    def elevation_m(self, lat: np.ndarray, lon: np.ndarray) -> np.ndarray:
        return np.array([self.z[a, b] for a, b in zip(lat, lon, strict=True)])


def test_water_filter_drops_cells_at_or_below_half_a_metre() -> None:
    z = {(47.0, -122.0): -2.0, (47.0, -121.99): 0.0, (47.0, -121.98): 0.5, (47.0, -121.97): 0.6}
    assert on_land(list(z), Heights(z)) == [(47.0, -121.97)]
    assert on_land([], Heights(z)) == []
