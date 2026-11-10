"""Ground elevation for the propagation model: flat, an in-memory grid, or Copernicus GLO-30.

GLO-30 tiles are 1 x 1 degree GeoTIFFs named after their south-west corner, float32
metres, "pixel is point": pixel (0, 0) is the sample AT (lat + 1, lon), rows go south
and columns east in steps of 1/rows and 1/cols degrees. Tiles are read with tifffile
(+ imagecodecs for the floating-point predictor the COGs use).
"""

import logging
import math
import os
import shutil
import tempfile
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

import numpy as np
import numpy.typing as npt
import tifffile

log = logging.getLogger(__name__)

Array = npt.NDArray[np.float64]

# GLO-30 has no nodata tag and no tiles over open sea (a missing tile is read as 0 m).
# Defensively, anything below the lowest land (-430 m, Dead Sea shore) or NaN is also
# read as 0 m (sea level) rather than as a deep pit that would block every path.
NODATA_BELOW_M = -1000.0

GLO30_URL = "https://copernicus-dem-30m.s3.amazonaws.com/{name}/{name}.tif"


class Terrain(Protocol):
    def elevation_m(self, lat: Array, lon: Array) -> Array: ...


class FlatTerrain:
    """Sea level everywhere: the model reduces to free space plus Earth curvature."""

    def elevation_m(self, lat: Array, lon: Array) -> Array:
        return np.zeros(np.broadcast(lat, lon).shape)


@dataclass(frozen=True)
class GridTerrain:
    """Elevation samples on a regular lat/lon grid; row 0 is the north edge.

    `z[i, j]` is the elevation at (north - i*dlat, west + j*dlon). Points between
    samples are bilinearly interpolated; points outside the grid take the nearest edge.
    """

    z: Array
    north: float
    west: float
    dlat: float
    dlon: float

    def __post_init__(self) -> None:
        z = np.asarray(self.z, dtype=np.float64)
        if z.ndim != 2 or min(z.shape) < 2:
            raise ValueError(f"grid must be 2-D and at least 2x2, got {z.shape}")
        object.__setattr__(self, "z", np.where(z > NODATA_BELOW_M, z, 0.0))

    def elevation_m(self, lat: Array, lon: Array) -> Array:
        rows, cols = self.z.shape
        r = np.clip((self.north - np.asarray(lat)) / self.dlat, 0, rows - 1)
        c = np.clip((np.asarray(lon) - self.west) / self.dlon, 0, cols - 1)
        r0 = np.minimum(np.floor(r).astype(np.intp), rows - 2)
        c0 = np.minimum(np.floor(c).astype(np.intp), cols - 2)
        fr, fc = r - r0, c - c0
        z = self.z
        top = z[r0, c0] * (1 - fc) + z[r0, c0 + 1] * fc
        bottom = z[r0 + 1, c0] * (1 - fc) + z[r0 + 1, c0 + 1] * fc
        result: Array = top * (1 - fr) + bottom * fr
        return result


# --- Copernicus GLO-30 tiles ---------------------------------------------------------------


def tile_name(lat: int, lon: int) -> str:
    """Name of the tile whose south-west corner is (lat, lon), e.g. N47_00_W123_00."""
    ns = f"{'N' if lat >= 0 else 'S'}{abs(lat):02d}"
    ew = f"{'E' if lon >= 0 else 'W'}{abs(lon):03d}"
    return f"Copernicus_DSM_COG_10_{ns}_00_{ew}_00_DEM"


def tiles_for_bbox(
    min_lat: float, min_lon: float, max_lat: float, max_lon: float
) -> list[tuple[int, int]]:
    """South-west corners of every 1-degree tile the box touches."""
    return [
        (lat, lon)
        for lat in range(math.floor(min_lat), math.floor(max_lat) + 1)
        for lon in range(math.floor(min_lon), math.floor(max_lon) + 1)
    ]


def mosaic(tiles: dict[tuple[int, int], Array | None]) -> GridTerrain:
    """Stack 1-degree tiles (keyed by SW corner) into one grid. Missing tiles become 0 m.

    Tiles are pixel-is-point with the north/west edge included, so a tile's last row is
    one step north of the next tile's first row: plain stacking keeps the spacing even.
    """
    present = [t for t in tiles.values() if t is not None]
    if not present:
        raise ValueError("no DEM tiles to mosaic")
    shape = present[0].shape
    # shortcut: all tiles must share one shape; GLO-30 narrows columns above 50 deg
    # latitude, so a region spanning 50 N would need resampling first.
    if any(t.shape != shape for t in present):
        raise ValueError("DEM tiles differ in shape; mosaic needs one resolution")
    lats = sorted({lat for lat, _ in tiles}, reverse=True)
    lons = sorted({lon for _, lon in tiles})
    blank = np.zeros(shape)
    grid = np.block([[_or(tiles.get((lat, lon)), blank) for lon in lons] for lat in lats])
    return GridTerrain(grid, north=lats[0] + 1, west=lons[0], dlat=1 / shape[0], dlon=1 / shape[1])


def _or(tile: Array | None, blank: Array) -> Array:
    return blank if tile is None else tile


def read_tile(path: Path, lat: int, lon: int) -> Array:
    """Read one GLO-30 tile and check its georeference matches the naming convention."""
    with tifffile.TiffFile(path) as tif:
        page = tif.pages.first
        tie = page.tags["ModelTiepointTag"].value
        scale = page.tags["ModelPixelScaleTag"].value
        z = np.asarray(page.asarray(), dtype=np.float64)
    rows, cols = z.shape
    expected = (lon, lat + 1, 1 / cols, 1 / rows)
    if not np.allclose((tie[3], tie[4], scale[0], scale[1]), expected):
        raise ValueError(f"{path}: georeference {tie}, {scale} is not a GLO-30 tile at {lat},{lon}")
    return z


def load_glo30(
    dem_dir: Path, min_lat: float, min_lon: float, max_lat: float, max_lon: float
) -> GridTerrain:
    """Mosaic the tiles in dem_dir that cover the box (run `fetch-dem` first)."""
    tiles: dict[tuple[int, int], Array | None] = {}
    for lat, lon in tiles_for_bbox(min_lat, min_lon, max_lat, max_lon):
        path = dem_dir / f"{tile_name(lat, lon)}.tif"
        if path.exists():
            tiles[lat, lon] = read_tile(path, lat, lon)
        else:
            log.warning("no DEM tile %s (open sea, or not fetched): using 0 m", path.name)
            tiles[lat, lon] = None
    # shortcut: holds every tile in memory (52 MB per tile at 30 m); crop to the box
    # if regions grow beyond a few tiles.
    return mosaic(tiles)


def fetch_glo30(
    dem_dir: Path, min_lat: float, min_lon: float, max_lat: float, max_lon: float
) -> dict[str, list[str]]:
    """Download missing tiles for the box from AWS Open Data, one at a time.

    Writes to a unique temp file in dem_dir and renames, so an interrupted download
    never looks like a tile, and two jobs sharing the DEM cache (propagation and
    placement CronJobs) never write the same file. A 404 means GLO-30 has no tile
    there (open sea).
    """
    dem_dir.mkdir(parents=True, exist_ok=True)
    report: dict[str, list[str]] = {"downloaded": [], "present": [], "no_tile": []}
    for lat, lon in tiles_for_bbox(min_lat, min_lon, max_lat, max_lon):
        name = tile_name(lat, lon)
        path = dem_dir / f"{name}.tif"
        if path.exists():
            report["present"].append(name)
            continue
        url = GLO30_URL.format(name=name)
        with tempfile.NamedTemporaryFile(
            dir=dem_dir, prefix=f"{name}.", suffix=".part", delete=False
        ) as f:
            part = Path(f.name)
        try:
            with urllib.request.urlopen(url, timeout=120) as r, open(part, "wb") as out:
                shutil.copyfileobj(r, out)
            # Atomic on one filesystem. POSIX: the last of two concurrent fetches wins.
            # Windows refuses to replace a file another rename is touching (PermissionError);
            # if the tile is there, the other fetch won and our copy is redundant.
            try:
                os.replace(part, path)
            except PermissionError:
                if not path.exists():
                    raise
                part.unlink(missing_ok=True)
        except urllib.error.HTTPError as e:
            part.unlink(missing_ok=True)
            if e.code != 404:
                raise
            report["no_tile"].append(name)
            continue
        except BaseException:
            part.unlink(missing_ok=True)
            raise
        report["downloaded"].append(name)
    return report
