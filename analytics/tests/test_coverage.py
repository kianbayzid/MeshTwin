"""Predicted coverage grid with stub models (no I/O, no DEM)."""

import json
from datetime import UTC, datetime
from types import SimpleNamespace
from typing import Any, cast

import pytest

from meshtwin_analytics.coverage import coverage, coverage_json, round_half
from meshtwin_analytics.geo import haversine_km
from meshtwin_analytics.pipeline import TopologyRun
from meshtwin_analytics.placement import grid_cells
from meshtwin_analytics.propagation import LinkPrediction, Radio, Site

RADIO = Radio()


class DistanceModel:
    """SNR falls 1 dB per km from 10 dB, plus a tiny function of the exact tx position, so a
    test sees if the tx was not snapped. Records the sites it was asked about."""

    name = "dist"
    version = "0"

    def __init__(self) -> None:
        self.calls: list[tuple[Site, Site]] = []

    def predict(self, a: Site, b: Site) -> LinkPrediction:
        self.calls.append((a, b))
        d = haversine_km(a.lat, a.lon, b.lat, b.lon)
        snr = 10.0 - d + (a.lat * 1000 % 1) * 0.01
        return LinkPrediction(d, 0.0, snr, True, 1.0, 0.0)


def _cells() -> list[tuple[float, float]]:
    return grid_cells([(47.00, -122.00), (47.02, -121.97)])  # 5 rows x 6 cols


def test_grid_shape_and_order() -> None:
    cov = coverage(_cells(), [(47.0, -122.0)], DistanceModel(), RADIO)
    assert (cov.rows, cov.cols) == (5, 6)
    assert (cov.north, cov.west) == (47.03, -122.01)
    assert len(cov.snr_db) == 30
    # The transmitter cell (47.00, -122.00) is row 3 (47.03 - 3 x 0.01), column 1.
    best = max(range(30), key=lambda i: cov.snr_db[i] or -99)
    assert divmod(best, cov.cols) == (3, 1)
    # Row 0 is north: farther from a southern transmitter, so weaker than the bottom row.
    top, bottom = cov.snr_db[1], cov.snr_db[4 * 6 + 1]
    assert top is not None and bottom is not None and top < bottom


def test_null_when_no_transmitter_in_range() -> None:
    model = DistanceModel()
    cov = coverage(_cells(), [(47.5, -122.0)], model, RADIO)  # ~55 km away
    assert cov.snr_db == [None] * 30
    assert model.calls == []  # skipped before the model
    assert coverage(_cells(), [], model, RADIO).snr_db == [None] * 30


def test_max_over_transmitters() -> None:
    cells = _cells()
    a = coverage(cells, [(47.0, -122.0)], DistanceModel(), RADIO).snr_db
    b = coverage(cells, [(47.03, -121.96)], DistanceModel(), RADIO).snr_db
    both = coverage(cells, [(47.0, -122.0), (47.03, -121.96)], DistanceModel(), RADIO).snr_db
    for x, y, z in zip(a, b, both, strict=True):
        assert x is not None and y is not None
        assert z == max(x, y)


def test_transmitter_is_snapped_privacy() -> None:
    """Moving a repeater anywhere inside its cell changes no output value."""
    cells = _cells()
    ref = coverage(cells, [(47.0, -122.0)], DistanceModel(), RADIO)
    for lat, lon in [(47.0049, -121.9951), (46.9951, -122.0049), (47.0012345, -122.0033)]:
        model = DistanceModel()
        assert coverage(cells, [(lat, lon)], model, RADIO) == ref
        assert {(a.lat, a.lon) for a, _ in model.calls} == {(47.0, -122.0)}
        assert all(a.height_m == RADIO.infrastructure_height_m for a, _ in model.calls)
        assert all(b.height_m == RADIO.client_height_m for _, b in model.calls)


def test_values_rounded_to_half_db_and_below_floor_kept() -> None:
    cov = coverage(_cells(), [(47.0, -122.0)], DistanceModel(), RADIO)
    values = [v for v in cov.snr_db if v is not None]
    assert all(v * 2 == int(v * 2) for v in values)
    assert round_half(1.26) == 1.5 and round_half(-7.74) == -7.5 and round_half(3.0) == 3.0
    far = coverage(
        grid_cells([(47.0, -122.0), (47.2, -122.0)]), [(47.0, -122.0)], DistanceModel(), RADIO
    )
    assert min(v for v in far.snr_db if v is not None) < RADIO.snr_floor_db


def test_cells_must_be_a_rectangle() -> None:
    with pytest.raises(ValueError):
        coverage(_cells()[:-1], [], DistanceModel(), RADIO)


def test_json_matches_contract() -> None:
    run = cast(
        TopologyRun,
        SimpleNamespace(
            region="SEA",
            generated_at=datetime(2026, 11, 12, 2, 17, tzinfo=UTC),
            from_us=0,
            to_us=1,
        ),
    )
    cov = coverage(_cells(), [(47.0, -122.0), (47.5, -122.0)], DistanceModel(), RADIO)
    method: dict[str, Any] = {"grid_deg": 0.01, "transmitters": 2}
    doc = json.loads(json.dumps(coverage_json(run, cov, method)))
    assert set(doc) == {"meta", "method", "grid", "snr_db"}
    assert doc["meta"]["source"] == "placement-job"
    assert doc["grid"] == {"north": 47.03, "west": -122.01, "rows": 5, "cols": 6}
    assert len(doc["snr_db"]) == doc["grid"]["rows"] * doc["grid"]["cols"]
    for v in (doc["grid"]["north"], doc["grid"]["west"]):
        assert round(v, 2) == v and len(repr(v).split(".")[-1]) <= 2
