"""fetch-dem / terrain-tiles box from the data (--root), and DEM jobs never reading
unfetched tiles as 0 m."""

import json
import logging
import random
from pathlib import Path
from typing import Any

import duckdb
import pytest

from meshtwin_analytics import cli, load, placement
from meshtwin_analytics.pipeline import TopologyRun
from meshtwin_analytics.terrain import FlatTerrain, tile_name, tiles_for_bbox

from .conftest import HASH1


def _tenths(v: float) -> bool:
    return abs(v * 10 - round(v * 10)) < 1e-9


def test_node_bbox_pads_and_snaps_outward() -> None:
    assert cli.node_bbox([], 0.05) is None
    pts = [(47.512345, -122.334567), (47.601234, -122.201234)]
    box = cli.node_bbox(pts, 0.05)
    assert box is not None
    s, w, n, e = box
    assert all(_tenths(v) for v in box)
    assert s <= 47.512345 - 0.05 and n >= 47.601234 + 0.05
    assert w <= -122.334567 - 0.05 and e >= -122.201234 + 0.05
    assert (s, w, n, e) == (47.4, -122.4, 47.7, -122.1)


def test_box_pad_covers_dem_jobs() -> None:
    """The default pad covers placement's DEM box and every coverage/candidate cell."""
    assert cli.DEFAULT_PAD_DEG >= cli.PLACEMENT_DEM_PAD_DEG
    rng = random.Random(1)
    for _ in range(200):
        pts = [(rng.uniform(-60, 60), rng.uniform(-170, 170))]
        pts += [
            (pts[0][0] + rng.uniform(0, 0.5), pts[0][1] + rng.uniform(0, 0.5)) for _ in range(5)
        ]
        box = cli.node_bbox(pts, cli.DEFAULT_PAD_DEG)
        assert box is not None
        s, w, n, e = box
        lats, lons = [p[0] for p in pts], [p[1] for p in pts]
        pad = cli.PLACEMENT_DEM_PAD_DEG
        assert s <= min(lats) - pad and w <= min(lons) - pad
        assert n >= max(lats) + pad and e >= max(lons) + pad
        for lat, lon in placement.grid_cells(pts):
            assert s <= lat <= n and w <= lon <= e


def test_bbox_and_root_are_mutually_exclusive(capsys: pytest.CaptureFixture[str]) -> None:
    for cmd in ("fetch-dem", "terrain-tiles"):
        extra = ["--out", "x"] if cmd == "terrain-tiles" else []
        with pytest.raises(SystemExit) as both:
            cli.main([cmd, "--dem-dir", "d", "--bbox", "47,-123,48,-122", "--root", "r", *extra])
        assert both.value.code == 2
        assert "not allowed with" in capsys.readouterr().err
        with pytest.raises(SystemExit) as neither:
            cli.main([cmd, "--dem-dir", "d", *extra])
        assert neither.value.code == 2
        assert "one of the arguments --bbox --root is required" in capsys.readouterr().err


def _capture_fetch(monkeypatch: pytest.MonkeyPatch) -> list[tuple[Any, ...]]:
    calls: list[tuple[Any, ...]] = []

    def fake(*a: Any) -> dict[str, list[str]]:
        calls.append(a)
        return {"downloaded": [], "present": [], "no_tile": []}

    monkeypatch.setattr(cli, "fetch_glo30", fake)
    return calls


def test_fetch_dem_root_uses_located_nodes(
    run_hash1: TopologyRun,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    caplog: pytest.LogCaptureFixture,
) -> None:
    caplog.set_level(logging.DEBUG)  # every log record, not only warnings
    calls = _capture_fetch(monkeypatch)
    assert cli.main(["fetch-dem", "--dem-dir", str(tmp_path), "--root", str(HASH1)]) == 0
    expected = cli.node_bbox(cli._located(run_hash1), cli.DEFAULT_PAD_DEG)
    assert expected is not None
    assert calls == [(tmp_path, *expected)]
    out = capsys.readouterr()
    assert json.loads(out.out)["box"] == list(expected)
    # Privacy: no raw node coordinate in what the job prints or logs.
    seen = out.out + out.err + caplog.text
    for lat, lon in cli._located(run_hash1):
        for v in (lat, lon):
            assert f"{v:.4f}"[:-1] not in seen


@pytest.mark.parametrize("cmd", ["fetch-dem", "terrain-tiles"])
@pytest.mark.parametrize("empty_root", [False, True], ids=["empty-region", "empty-root"])
def test_no_located_nodes_is_not_a_failure(
    cmd: str,
    empty_root: bool,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Region without data in a shared root, and a truly empty root (before the first flush)."""
    calls = _capture_fetch(monkeypatch)
    out = tmp_path / "out"
    root = tmp_path / "parquet" if empty_root else HASH1
    if empty_root:
        root.mkdir()
    argv = [cmd, "--dem-dir", str(tmp_path / "dem"), "--root", str(root), "--region", "XXX"]
    if cmd == "terrain-tiles":
        argv += ["--out", str(out)]
    assert cli.main(argv) == 0
    report = json.loads(capsys.readouterr().out)
    assert report["box"] is None and report["skipped"] == "no located nodes in XXX yet"
    assert calls == [] and not out.exists() and not (tmp_path / "dem").exists()
    assert "no located nodes in XXX yet: nothing to" in caplog.text


@pytest.mark.parametrize("kind", ["missing", "file"])
def test_bad_local_root_fails(kind: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """A typo'd root or missing mount fails loudly; only an existing empty directory skips."""
    calls = _capture_fetch(monkeypatch)
    root = tmp_path / "parquet"
    if kind == "file":
        root.write_text("not a directory")
    with pytest.raises(FileNotFoundError, match="not an existing directory"):
        cli.main(["fetch-dem", "--dem-dir", str(tmp_path / "dem"), "--root", str(root)])
    assert calls == []


def test_other_io_errors_still_raise() -> None:
    """Only DuckDB's "no files found" means "no data yet"; S3/auth errors must surface."""

    class Con:
        def execute(self, *a: object) -> None:
            raise duckdb.IOException("HTTP Error: 403 Forbidden")

    with pytest.raises(duckdb.IOException, match="403"):
        load.max_rx_us(Con(), "s3://b", "SEA")  # type: ignore[arg-type]


def test_receptions_but_no_located_node_is_skipped(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    calls = _capture_fetch(monkeypatch)
    monkeypatch.setattr(cli, "_located", lambda run: [])
    assert cli.main(["fetch-dem", "--dem-dir", str(tmp_path), "--root", str(HASH1)]) == 0
    assert json.loads(capsys.readouterr().out)["skipped"] == "no located nodes in SEA yet"
    assert calls == []


def test_unfetched_tile_falls_back_to_flat(
    run_hash1: TopologyRun,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
) -> None:
    """A tile fetch-dem never fetched is flat terrain, labelled so, never a silent 0 m."""
    loaded: list[object] = []

    def load(*a: object) -> FlatTerrain:
        loaded.append(a)
        return FlatTerrain()

    monkeypatch.setattr(cli, "load_glo30", load)
    dem = tmp_path / "dem"
    dem.mkdir()
    terrain, label = cli._terrain(run_hash1, dem, cli.PLACEMENT_DEM_PAD_DEG)  # empty dir
    assert label == cli.FLAT and isinstance(terrain, FlatTerrain) and loaded == []
    assert "DEM tiles not fetched" in caplog.text

    box = cli.node_bbox(cli._located(run_hash1), cli.DEFAULT_PAD_DEG)
    assert box is not None
    tiles = tiles_for_bbox(*box)
    for lat, lon in tiles[:-1]:
        (dem / f"{tile_name(lat, lon)}.tif").touch()
    (dem / f"{tile_name(*tiles[-1])}.notile").touch()  # fetch-dem's open-sea marker
    _, label = cli._terrain(run_hash1, dem, cli.PLACEMENT_DEM_PAD_DEG)
    assert label == "Copernicus GLO-30" and len(loaded) == 1


def test_placement_with_empty_dem_dir_is_flat_not_empty(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    """Fresh region: --dem-dir is empty. Placement runs flat, says so, and keeps its candidates
    (on_land on flat 0 m terrain would have dropped every cell as water)."""
    (tmp_path / "dem").mkdir()
    argv = ["placement", "--root", str(HASH1), "--out", str(tmp_path / "out")]
    assert cli.main([*argv, "--dem-dir", str(tmp_path / "dem")]) == 0
    method = json.loads(capsys.readouterr().out)["method"]
    assert method["dem"] == cli.FLAT
    assert method["candidates_scored"] > 0
