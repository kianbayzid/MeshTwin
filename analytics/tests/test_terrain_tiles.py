"""Terrain-RGB tiles: tile math, encoding, rendering from a synthetic GridTerrain, and the CLI."""

import json
from pathlib import Path

import imagecodecs
import numpy as np
import pytest

from meshtwin_analytics import cli, terrain_tiles
from meshtwin_analytics.terrain import GridTerrain, tile_name, tiles_for_bbox
from meshtwin_analytics.terrain_tiles import (
    decode,
    encode,
    pixel_lonlat,
    render,
    snap_bounds_outward,
    tiles_for_bounds,
)

from .test_end_to_end import _conforms, _contract_example

SEA_BBOX = (47.3, -122.6, 47.9, -121.8)


def _plane() -> GridTerrain:
    """h = 100 * (lat - 47) + 50 * (lon + 123): planar, so bilinear interpolation is exact."""
    lats = np.linspace(49.0, 46.0, 31)  # row 0 is the north edge
    lons = np.linspace(-124.0, -121.0, 31)
    z = 100 * (lats[:, None] - 47) + 50 * (lons[None, :] + 123)
    return GridTerrain(z, north=49.0, west=-124.0, dlat=0.1, dlon=0.1)


def _arr(v: float) -> np.ndarray:
    return np.asarray(v, dtype=np.float64)


def test_pixel_centre_known_values() -> None:
    # z0: the first pixel centre is half a pixel (360/512 deg) east of the antimeridian.
    lon, lat = pixel_lonlat(0, 0, 0, _arr(0), _arr(0))
    assert float(lon) == -180 + 360 / 512
    assert 84.9 < float(lat) < 85.0511  # just inside the Mercator limit
    # The north-west corner of tile z1 (1, 1) is (0, 0); corners are pixel -0.5.
    lon, lat = pixel_lonlat(1, 1, 1, _arr(-0.5), _arr(-0.5))
    assert (float(lon), float(lat)) == pytest.approx((0.0, 0.0), abs=1e-12)
    # Downtown Seattle (47.6062, -122.3321) is OSM tile z12 (656, 1430); its NW corner lon is
    # 656/4096 * 360 - 180.
    lon, _ = pixel_lonlat(12, 656, 1430, _arr(-0.5), _arr(-0.5))
    assert float(lon) == pytest.approx(-122.34375)
    assert tiles_for_bounds([-122.3322, 47.6061, -122.3320, 47.6063], 12) == [(656, 1430)]


def test_pixel_centres_round_trip_through_tile_coordinates() -> None:
    i = np.array([0.0, 100.0, 255.0])
    lon, lat = pixel_lonlat(12, 656, 1430, i, i)
    for k in range(3):
        fx, fy = terrain_tiles._tile_xy(float(lon[k]), float(lat[k]), 12)
        assert (fx, fy) == pytest.approx((656 + (i[k] + 0.5) / 256, 1430 + (i[k] + 0.5) / 256))


def test_tiles_for_bounds() -> None:
    bounds = snap_bounds_outward(*SEA_BBOX)
    # lon -122.6..-121.8 straddles the z8 x=40/41 edge at -122.34.
    assert tiles_for_bounds(bounds, 8) == [(40, 89), (41, 89)]
    counts = [len(tiles_for_bounds(bounds, z)) for z in range(8, 13)]
    assert counts == sorted(counts)  # more tiles every zoom
    # A box ending exactly on a tile edge doesn't pull in the neighbour.
    assert tiles_for_bounds([-180.0, 0.0, 0.0, 85.0], 1) == [(0, 0)]


def test_snap_bounds_outward() -> None:
    assert snap_bounds_outward(*SEA_BBOX) == [-122.6, 47.3, -121.8, 47.9]  # already on 0.1
    assert snap_bounds_outward(47.43, -122.40, 47.78, -122.03) == [-122.4, 47.4, -122.0, 47.8]
    assert snap_bounds_outward(-0.05, -0.05, 0.05, 0.05) == [-0.1, -0.1, 0.1, 0.1]


def test_encode_decode_round_trip() -> None:
    h = np.random.default_rng(1).uniform(-500, 9000, 10_000)
    assert np.abs(decode(encode(h)) - h).max() <= 0.05 + 1e-9  # half the 0.1 m step
    assert encode(np.array(0.0)).tolist() == [1, 134, 160]  # v = 100000 = 1*65536 + 134*256 + 160
    assert encode(np.array(-20000.0)).tolist() == [0, 0, 0]  # clamped, not wrapped
    assert encode(np.array(1e7)).tolist() == [255, 255, 255]


def test_render_samples_the_terrain_and_blanks_outside_bounds() -> None:
    grid, bounds = _plane(), snap_bounds_outward(*SEA_BBOX)
    tile = render(grid, bounds, 12, 656, 1430)  # fully inside bounds
    rgb = imagecodecs.png_decode(tile.png)
    assert rgb.shape == (256, 256, 3) and rgb.dtype == np.uint8
    idx = np.arange(256, dtype=np.float64)
    lon, lat = pixel_lonlat(12, 656, 1430, idx, idx)
    want = 100 * (lat[:, None] - 47) + 50 * (lon[None, :] + 123)
    assert np.abs(decode(rgb) - want).max() <= 0.05 + 1e-6
    assert tile.min_m == pytest.approx(want.min()) and tile.max_m == pytest.approx(want.max())

    edge = render(grid, bounds, 8, 40, 89)  # mostly outside bounds
    h = decode(imagecodecs.png_decode(edge.png))
    lon, lat = pixel_lonlat(8, 40, 89, idx, idx)
    outside = (lon[None, :] < bounds[0]) | (lat[:, None] > bounds[3]) | (lat[:, None] < bounds[1])
    assert outside.any() and (~outside).any()
    assert np.all(h[outside] == 0.0)
    assert np.all(h[~outside] > 0.0)


def _fake_dem(dem_dir: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Empty tile files for the existence check; synthetic terrain instead of reading them."""
    dem_dir.mkdir()
    for lat, lon in tiles_for_bbox(*SEA_BBOX):
        (dem_dir / f"{tile_name(lat, lon)}.tif").touch()
    monkeypatch.setattr(cli, "load_glo30", lambda *a: _plane())


def _args(dem: Path, out: Path) -> list[str]:
    bb = ",".join(str(v) for v in SEA_BBOX)
    return [
        "terrain-tiles",
        "--dem-dir",
        str(dem),
        "--bbox",
        bb,
        "--maxzoom",
        "10",
        "--out",
        str(out),
    ]


def test_cli_writes_tiles_and_contract_json(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    _fake_dem(tmp_path / "dem", monkeypatch)
    out = tmp_path / "out"
    assert cli.main(_args(tmp_path / "dem", out)) == 0
    report = json.loads(capsys.readouterr().out)

    region = out / "summaries" / "region=SEA"
    doc = json.loads((region / "terrain.json").read_text())
    assert _conforms(doc, _contract_example("terrain.json"), "terrain") == []
    assert doc["meta"]["window"] is None and doc["meta"]["source"] == "terrain-job"
    assert doc["bounds"] == [-122.6, 47.3, -121.8, 47.9]
    assert (doc["minzoom"], doc["maxzoom"], doc["encoding"]) == (8, 10, "mapbox")

    pngs = sorted(region.glob("terrain/*/*/*.png"))
    want = [(z, x, y) for z in range(8, 11) for x, y in tiles_for_bounds(doc["bounds"], z)]
    got = [(int(p.parent.parent.name), int(p.parent.name), int(p.stem)) for p in pngs]
    assert sorted(got) == sorted(want)
    assert doc["tiles"] == report["tiles"] == len(pngs)
    assert report["bytes"] == sum(p.stat().st_size for p in pngs)
    assert all(p.read_bytes().startswith(b"\x89PNG\r\n\x1a\n") for p in pngs)
    assert 0 < report["elevation_m"]["min"] < report["elevation_m"]["max"]


def test_cli_refuses_without_dem(tmp_path: Path) -> None:
    (tmp_path / "dem").mkdir()
    with pytest.raises(SystemExit) as e:
        cli.main(_args(tmp_path / "dem", tmp_path / "out"))
    assert e.value.code not in (0, None)
    assert "DEM tiles missing" in str(e.value.code)
    assert not (tmp_path / "out").exists()


def test_cli_rejects_bad_zoom(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    _fake_dem(tmp_path / "dem", monkeypatch)
    with pytest.raises(SystemExit):
        cli.main([*_args(tmp_path / "dem", tmp_path / "out"), "--minzoom", "11"])
    assert not (tmp_path / "out").exists()
