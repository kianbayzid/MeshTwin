"""Mapbox Terrain-RGB tiles for the 3D map, sampled from the propagation model's own terrain.

XYZ (slippy-map) Web Mercator tiles, 256 x 256. Elevation is read with the same
`Terrain.elevation_m` (GLO-30 mosaic, bilinear) the propagation model uses, so the map
shows the ground the model saw. Encoding: v = round((h + 10000) * 10), R/G/B = the three
bytes of v, decode h = -10000 + v * 0.1 (docs/api-contract.md, terrain.json).
"""

import math
from collections.abc import Iterator
from dataclasses import dataclass

import imagecodecs
import numpy as np
import numpy.typing as npt

from .terrain import Array, Terrain

TILE_SIZE = 256
MAX_ZOOM = 14  # the API rejects z > 14
_V_MAX = 2**24 - 1


def pixel_lonlat(z: int, x: int, y: int, i: Array, j: Array) -> tuple[Array, Array]:
    """(lon, lat) in degrees of the centre of pixel column i, row j of tile (z, x, y)."""
    n = 2**z
    lon = (x + (np.asarray(i) + 0.5) / TILE_SIZE) / n * 360.0 - 180.0
    merc_y = math.pi * (1 - 2 * (y + (np.asarray(j) + 0.5) / TILE_SIZE) / n)
    lat = np.degrees(np.arctan(np.sinh(merc_y)))
    return lon, lat


def _tile_xy(lon: float, lat: float, z: int) -> tuple[float, float]:
    """Fractional tile coordinates of a point."""
    n = 2**z
    r = math.radians(lat)
    return (lon + 180.0) / 360.0 * n, (1 - math.asinh(math.tan(r)) / math.pi) / 2 * n


def tiles_for_bounds(bounds: list[float], z: int) -> list[tuple[int, int]]:
    """(x, y) of every tile at zoom z that overlaps bounds [west, south, east, north]."""
    west, south, east, north = bounds
    n = 2**z
    x0, y0 = _tile_xy(west, north, z)
    x1, y1 = _tile_xy(east, south, z)
    # ceil - 1 on the far edge: a box ending exactly on a tile edge doesn't need the next tile.
    xs = range(max(0, math.floor(x0)), min(n - 1, max(math.floor(x0), math.ceil(x1) - 1)) + 1)
    ys = range(max(0, math.floor(y0)), min(n - 1, max(math.floor(y0), math.ceil(y1) - 1)) + 1)
    return [(x, y) for x in xs for y in ys]


def snap_bounds_outward(
    min_lat: float, min_lon: float, max_lat: float, max_lon: float
) -> list[float]:
    """[west, south, east, north] snapped outward to 0.1 degree."""

    # Work in tenths; round(.., 9) first so 47.3 (472.99.. tenths) doesn't floor to 47.2.
    def down(v: float) -> float:
        return math.floor(round(v * 10, 9)) / 10

    def up(v: float) -> float:
        return math.ceil(round(v * 10, 9)) / 10

    return [down(min_lon), down(min_lat), up(max_lon), up(max_lat)]


def encode(h: Array) -> npt.NDArray[np.uint8]:
    """Elevation in metres -> (..., 3) uint8 Terrain-RGB. Clamped to 24 bits."""
    v = np.clip(np.round((np.asarray(h) + 10000.0) * 10.0), 0, _V_MAX).astype(np.uint32)
    return np.stack([v // 65536, (v // 256) % 256, v % 256], axis=-1).astype(np.uint8)


def decode(rgb: npt.NDArray[np.uint8]) -> Array:
    """(..., 3) Terrain-RGB -> elevation in metres."""
    c = rgb.astype(np.float64)
    result: Array = -10000.0 + (c[..., 0] * 65536 + c[..., 1] * 256 + c[..., 2]) * 0.1
    return result


@dataclass(frozen=True)
class Tile:
    z: int
    x: int
    y: int
    png: bytes
    min_m: float  # elevation range sampled inside bounds (NaN if none)
    max_m: float


def render(terrain: Terrain, bounds: list[float], z: int, x: int, y: int) -> Tile:
    """One tile: one vectorised elevation_m call; pixels outside bounds are 0 m (no data)."""
    idx = np.arange(TILE_SIZE, dtype=np.float64)
    lon, lat = pixel_lonlat(z, x, y, idx, idx)  # lon depends on the column, lat on the row
    lat2, lon2 = lat[:, None], lon[None, :]
    h = terrain.elevation_m(lat2, lon2)
    west, south, east, north = bounds
    inside = (lon2 >= west) & (lon2 <= east) & (lat2 >= south) & (lat2 <= north)
    h = np.where(inside, h, 0.0)
    png = bytes(imagecodecs.png_encode(encode(h)))
    sampled = h[inside]
    lo, hi = (float(sampled.min()), float(sampled.max())) if sampled.size else (math.nan, math.nan)
    return Tile(z, x, y, png, lo, hi)


def render_all(terrain: Terrain, bounds: list[float], minzoom: int, maxzoom: int) -> Iterator[Tile]:
    for z in range(minzoom, maxzoom + 1):
        for x, y in tiles_for_bounds(bounds, z):
            yield render(terrain, bounds, z, x, y)
