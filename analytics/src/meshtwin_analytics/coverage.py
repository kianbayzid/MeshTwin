"""Predicted coverage: the best SNR a handheld in each 0.01 degree cell would get from any
repeater or room server, on placement's grid, through the same propagation model.

Privacy: transmitters are moved to their snapped cell centre (`geo.snap`) before the model
sees them. A continuous field computed from a raw position would give that position away.
"""

from dataclasses import dataclass
from typing import Any

from .geo import GRID_DEG, haversine_km, snap
from .pipeline import TopologyRun
from .placement import MAX_LINK_DISTANCE_KM, SOURCE
from .propagation import PropagationModel, Radio, Site
from .summaries import meta

TRANSMITTER_ROLES = ("repeater", "room")  # they forward floods


@dataclass(frozen=True)
class Coverage:
    north: float  # centre of row 0's cells
    west: float  # centre of column 0's cells
    rows: int
    cols: int
    snr_db: list[float | None]  # row-major, row 0 = north; rounded to 0.5 dB


def round_half(x: float) -> float:
    """Round to the nearest 0.5 dB."""
    return round(x * 2) / 2


def coverage(
    cells: list[tuple[float, float]],
    transmitters: list[tuple[float, float]],
    model: PropagationModel,
    radio: Radio,
) -> Coverage:
    """Max predicted SNR per cell over transmitters within MAX_LINK_DISTANCE_KM.

    cells: a full rectangle of grid-cell centres (`placement.grid_cells`).
    transmitters: raw (lat, lon); snapped here, so callers cannot leak a raw position.
    """
    if not cells:
        return Coverage(0.0, 0.0, 0, 0, [])
    steps = round(1 / GRID_DEG)
    lat_i = {round(lat * steps) for lat, _ in cells}
    lon_i = {round(lon * steps) for _, lon in cells}
    top, left = max(lat_i), min(lon_i)
    rows, cols = len(lat_i), len(lon_i)
    if rows * cols != len(cells):
        raise ValueError("cells must fill a rectangle")

    # Several transmitters in one cell predict the same field: model each cell once.
    tx_cells = sorted({(p["lat"], p["lon"]) for t in transmitters if (p := snap(*t)) is not None})
    best: list[float | None] = [None] * (rows * cols)
    for tlat, tlon in tx_cells:
        tx = Site(tlat, tlon, radio.infrastructure_height_m)
        for lat, lon in cells:
            if haversine_km(tlat, tlon, lat, lon) > MAX_LINK_DISTANCE_KM:
                continue
            snr = model.predict(tx, Site(lat, lon, radio.client_height_m)).predicted_snr_db
            i = (top - round(lat * steps)) * cols + (round(lon * steps) - left)
            cur = best[i]
            best[i] = snr if cur is None else max(cur, snr)
    return Coverage(
        top / steps,
        left / steps,
        rows,
        cols,
        [None if v is None else round_half(v) for v in best],
    )


def coverage_json(run: TopologyRun, cov: Coverage, method: dict[str, Any]) -> dict[str, Any]:
    return {
        "meta": meta(run, SOURCE),
        "method": method,
        "grid": {"north": cov.north, "west": cov.west, "rows": cov.rows, "cols": cov.cols},
        "snr_db": cov.snr_db,
    }
