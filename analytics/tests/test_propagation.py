"""Propagation physics (pure, synthetic terrain, no network) and the measured side on a fixture."""

import itertools
import math
from dataclasses import replace
from pathlib import Path

import numpy as np
import pytest

from meshtwin_analytics import compare
from meshtwin_analytics.cli import _terrain
from meshtwin_analytics.evaluate import load_truth
from meshtwin_analytics.model import Node
from meshtwin_analytics.pipeline import TopologyRun
from meshtwin_analytics.propagation import (
    FsplKnifeEdge,
    PropagationModel,
    Radio,
    Site,
    fspl_db,
    knife_edge_db,
)
from meshtwin_analytics.terrain import FlatTerrain, GridTerrain, mosaic, tile_name, tiles_for_bbox

from .conftest import HASH1

RADIO = Radio()
KM_PER_DEG_LAT = 6371.0 * math.pi / 180


def test_fspl_10km() -> None:
    # 20 log10(10) + 20 log10(910.525) + 32.44 = 20 + 59.1854 + 32.44
    assert fspl_db(10, 910.525) == pytest.approx(111.6254, abs=1e-3)


def test_noise_floor_and_snr_floor() -> None:
    # -174 + 10 log10(62500) + 6
    assert RADIO.noise_floor_dbm == pytest.approx(-120.041, abs=1e-3)
    assert RADIO.snr_floor_db == -7.5


def test_knife_edge() -> None:
    assert knife_edge_db(0) == pytest.approx(6.0, abs=0.05)  # edge exactly on the ray: 6 dB
    assert knife_edge_db(-1) == 0
    vs = np.linspace(-0.78, 5, 200)
    losses = [knife_edge_db(float(v)) for v in vs]
    assert all(b > a for a, b in itertools.pairwise(losses))
    assert losses[0] == pytest.approx(0, abs=0.01)  # continuous at the cut-off


def _north(km: float, h: float) -> tuple[Site, Site]:
    """Two sites km apart along a meridian at 47 N, both h metres above ground."""
    return Site(47.0, -122.0, h), Site(47.0 + km / KM_PER_DEG_LAT, -122.0, h)


def test_flat_short_path_is_clear() -> None:
    p = FsplKnifeEdge(FlatTerrain(), RADIO).predict(*_north(2, 30))
    assert p.distance_km == pytest.approx(2, rel=1e-3)
    assert p.line_of_sight
    assert p.diffraction_loss_db == 0
    assert p.fresnel_clearance > 0.6
    assert p.predicted_snr_db == pytest.approx(
        RADIO.tx_dbm - fspl_db(p.distance_km, RADIO.freq_mhz) - RADIO.noise_floor_dbm
    )


def test_ridge_blocks() -> None:
    # 10 km path, a 200 m east-west ridge at the midpoint, antennas 10 m up on flat ground.
    lats = np.linspace(47.2, 46.9, 301)  # 0.001 deg rows, north first
    z = np.where(np.abs(lats - (47.0 + 5 / KM_PER_DEG_LAT)) < 0.002, 200.0, 0.0)
    grid = GridTerrain(np.repeat(z[:, None], 3, axis=1), 47.2, -122.001, 0.001, 0.001)
    flat = FsplKnifeEdge(FlatTerrain(), RADIO).predict(*_north(10, 10))
    ridge = FsplKnifeEdge(grid, RADIO).predict(*_north(10, 10))
    assert not ridge.line_of_sight
    assert ridge.fresnel_clearance < 0
    assert ridge.diffraction_loss_db > 20
    assert ridge.predicted_snr_db == pytest.approx(
        flat.predicted_snr_db + flat.diffraction_loss_db - ridge.diffraction_loss_db
    )


def test_earth_bulge_blocks_long_low_path() -> None:
    # Bulge at the middle of 60 km: 30e3^2 / (2 * 4/3 * 6371e3) = 53 m, 51 m above the
    # 2 m antennas. First Fresnel radius there: sqrt(0.329 m * 15 km) = 70.3 m, so
    # clearance = -0.725, v = 1.03, J(v) = 14.1 dB. (A knife edge under-states smooth-Earth
    # loss; the single-edge shortcut in propagation.py.)
    p = FsplKnifeEdge(FlatTerrain(), RADIO).predict(*_north(60, 2))
    assert not p.line_of_sight
    assert p.fresnel_clearance == pytest.approx(-0.725, abs=0.01)
    assert p.diffraction_loss_db == pytest.approx(14.1, abs=0.1)


def _plane(lat: np.ndarray, lon: np.ndarray) -> np.ndarray:
    result: np.ndarray = 100 + 300 * (lat - 47) - 200 * (lon + 123)
    return result


def test_grid_bilinear_exact_on_plane() -> None:
    lat = 48 - np.arange(11)[:, None] * 0.1
    lon = -123 + np.arange(11)[None, :] * 0.1
    grid = GridTerrain(_plane(lat, lon), 48, -123, 0.1, 0.1)
    rng = np.random.default_rng(1)
    qlat, qlon = rng.uniform(47, 48, 100), rng.uniform(-123, -122, 100)
    np.testing.assert_allclose(grid.elevation_m(qlat, qlon), _plane(qlat, qlon))


def test_mosaic_seams() -> None:
    # 2 x 2 tiles of 4 x 4 pixel-is-point samples; each tile row 0 is its north edge.
    n = 4
    tiles: dict[tuple[int, int], np.ndarray | None] = {}
    for lat0 in (46, 47):
        for lon0 in (-123, -122):
            lat = lat0 + 1 - np.arange(n)[:, None] / n
            lon = lon0 + np.arange(n)[None, :] / n
            tiles[lat0, lon0] = _plane(lat, lon)
    grid = mosaic(tiles)
    assert grid.z.shape == (8, 8)
    assert (grid.north, grid.west) == (48, -123)
    # Points across both seams (lat 47, lon -122) interpolate the plane exactly.
    qlat = np.array([47.0, 47.1, 46.9, 47.6, 46.4])
    qlon = np.array([-122.0, -122.1, -121.9, -122.05, -122.6])
    np.testing.assert_allclose(grid.elevation_m(qlat, qlon), _plane(qlat, qlon))


def test_mosaic_missing_tile_is_sea_level() -> None:
    grid = mosaic({(47, -123): np.full((4, 4), 50.0), (47, -122): None})
    assert grid.elevation_m(np.array([47.5]), np.array([-121.5]))[0] == 0
    assert grid.elevation_m(np.array([47.5]), np.array([-122.6]))[0] == 50


def test_nodata_is_sea_level() -> None:
    grid = GridTerrain(np.array([[-32767.0, np.nan], [10.0, 10.0]]), 1, 0, 1, 1)
    assert grid.z[0, 0] == 0 and grid.z[0, 1] == 0


def test_tile_names() -> None:
    assert tile_name(47, -123) == "Copernicus_DSM_COG_10_N47_00_W123_00_DEM"
    assert tile_name(-5, 7) == "Copernicus_DSM_COG_10_S05_00_E007_00_DEM"
    assert tiles_for_bbox(47.4, -122.4, 47.8, -122.0) == [(47, -123), (47, -122)]


# --- measurement side on the fixture -----------------------------------------------------

# The generator's link SNR is a mean before the radio's ~14 dB clamp (ingest/internal/synth).
RADIO_SNR_CLAMP_DB = 14.0


def test_observed_snr_matches_truth(run_hash1: TopologyRun) -> None:
    """Observed medians recover the generator's true link SNR. This validates the observed
    half of the residuals; the predicted half is validated by the physics tests above.
    (Predicted vs truth disagrees a lot: the generator uses log-distance with exponent
    7.5 and no terrain. The model is not tuned to it.)"""
    truth = {
        frozenset((lk["a"], lk["b"])): min(lk["snr_db"], RADIO_SNR_CLAMP_DB)
        for lk in load_truth(HASH1 / "truth.json")["links"]
    }
    rows = compare.residuals(run_hash1, FsplKnifeEdge(FlatTerrain(), RADIO), RADIO)
    diffs = [
        abs(r.observed_snr_db - truth[key])
        for r in rows
        if (key := frozenset((r.a.upper(), r.b.upper()))) in truth
    ]
    assert len(diffs) >= 0.95 * len(rows) > 50
    assert float(np.median(diffs)) < 0.5  # measured 0.063 dB over 84 of 85 links


def test_unseen_pairs_follow_the_rules(run_hash1: TopologyRun) -> None:
    model: PropagationModel = FsplKnifeEdge(FlatTerrain(), RADIO)
    unseen = compare.unseen_strong(run_hash1, model, RADIO)
    assert 0 < len(unseen) <= compare.MAX_UNSEEN
    snrs = [u.prediction.predicted_snr_db for u in unseen]
    assert snrs == sorted(snrs, reverse=True)
    for u in unseen:
        assert (u.a, u.b) not in run_hash1.evidence.links
        assert run_hash1.graph.nodes[u.a]["relay"] and run_hash1.graph.nodes[u.b]["relay"]
        assert u.prediction.distance_km <= compare.MAX_UNSEEN_DISTANCE_KM
        assert u.prediction.predicted_snr_db >= RADIO.snr_floor_db + compare.STRONG_MARGIN_DB


def test_dem_without_located_nodes_falls_back_to_flat(
    run_hash1: TopologyRun, tmp_path: Path
) -> None:
    """No located node means no box to load tiles for: flat terrain, not a bare ValueError."""
    unlocated = {k: Node(k, n.role) for k, n in run_hash1.nodes.items()}
    terrain, dem = _terrain(replace(run_hash1, nodes=unlocated), tmp_path)
    assert isinstance(terrain, FlatTerrain)
    assert dem == "flat (no DEM)"
