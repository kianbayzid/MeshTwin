"""Command line: `meshtwin-analytics <command> ...` (topology, propagation, placement, evaluate,
fetch-dem, terrain-tiles)."""

import argparse
import json
import logging
import math
import re
import sys
import time
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from . import compare, coverage, placement, terrain_tiles
from .evaluate import evaluate, load_truth
from .geo import GRID_DEG
from .load import to_us
from .output import write_all, write_summaries
from .pipeline import (
    DAY_US,
    DEFAULT_ADVERT_LOOKBACK_US,
    NoReceptionsError,
    TopologyRun,
    default_window,
    run_topology,
)
from .propagation import FsplKnifeEdge, Radio
from .storage import output_fs
from .terrain import FlatTerrain, Terrain, fetch_glo30, load_glo30, missing_tiles

log = logging.getLogger(__name__)

FLAT = "flat (no DEM)"
# Placement loads the DEM over the nodes' box plus two cells (candidates reach one cell beyond).
PLACEMENT_DEM_PAD_DEG = 2 * GRID_DEG
# fetch-dem/terrain-tiles --root: pad the nodes' box by this. It must cover what the DEM jobs
# load: placement's 2-cell pad and the coverage grid (1 cell + half a cell of rounding);
# test_box_pad_covers_dem_jobs checks it. 0.05 deg (~5 km) leaves margin.
DEFAULT_PAD_DEG = 0.05

_UNITS_US = {"m": 60_000_000, "h": 3_600_000_000, "d": 86_400_000_000}


def duration_us(text: str) -> int:
    """'24h', '90m', '7d' -> microseconds."""
    m = re.fullmatch(r"(\d+)([mhd])", text.strip())
    if not m:
        raise argparse.ArgumentTypeError(f"duration like 24h, 90m or 7d expected, got {text!r}")
    return int(m.group(1)) * _UNITS_US[m.group(2)]


def iso_us(text: str) -> int:
    """ISO 8601 time -> microseconds; a time without offset is taken as UTC."""
    t = datetime.fromisoformat(text.replace("Z", "+00:00"))
    return to_us(t if t.tzinfo else t.replace(tzinfo=UTC))


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="meshtwin-analytics")
    sub = p.add_subparsers(dest="command", required=True)

    def common(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--root", required=True, help="Parquet root: directory or s3://bucket")
        window(sp)

    def box(sp: argparse.ArgumentParser) -> None:
        g = sp.add_mutually_exclusive_group(required=True)
        g.add_argument("--bbox", type=bbox, help="minlat,minlon,maxlat,maxlon")
        g.add_argument(
            "--root",
            help="Parquet root: the box is the region's located nodes (window options as "
            "topology), padded by --pad-deg and snapped outward to 0.1 degree",
        )
        sp.add_argument("--pad-deg", type=float, default=DEFAULT_PAD_DEG, help="default 0.05")
        window(sp)

    def window(sp: argparse.ArgumentParser) -> None:
        sp.add_argument("--region", default="SEA")
        sp.add_argument("--from", dest="frm", type=iso_us, help="window start (ISO 8601)")
        sp.add_argument("--to", type=iso_us, help="window end, exclusive (ISO 8601)")
        sp.add_argument("--window", type=duration_us, default=DAY_US, help="default 24h")
        sp.add_argument(
            "--advert-lookback",
            type=duration_us,
            default=DEFAULT_ADVERT_LOOKBACK_US,
            help="node registry lookback ending at the window end (default 7d; repeaters "
            "flood-advert only every 47 h)",
        )
        sp.add_argument("--min-confidence", type=float, default=0.5)

    t = sub.add_parser("topology", help="infer the link graph and resilience, write results")
    common(t)
    t.add_argument("--out", required=True, help="output root: directory or s3://bucket")
    pr = sub.add_parser("propagation", help="predicted vs observed SNR per link, write results")
    common(pr)
    pr.add_argument("--out", required=True, help="output root: directory or s3://bucket")
    pr.add_argument("--dem-dir", type=Path, help="Copernicus GLO-30 tiles (default: flat terrain)")
    pl = sub.add_parser("placement", help="score grid cells as new repeater sites, write results")
    common(pl)
    pl.add_argument("--out", required=True, help="output root: directory or s3://bucket")
    pl.add_argument("--dem-dir", type=Path, help="Copernicus GLO-30 tiles (default: flat terrain)")
    e = sub.add_parser("evaluate", help="score the inferred graph against truth.json")
    common(e)
    e.add_argument("--truth", required=True)
    f = sub.add_parser("fetch-dem", help="download Copernicus GLO-30 tiles covering a box")
    box(f)
    f.add_argument("--dem-dir", type=Path, required=True)
    tt = sub.add_parser("terrain-tiles", help="Terrain-RGB PNG tiles from GLO-30 for the 3D map")
    box(tt)
    tt.add_argument("--dem-dir", type=Path, required=True)
    tt.add_argument("--minzoom", type=int, default=8)
    tt.add_argument("--maxzoom", type=int, default=12)
    tt.add_argument("--out", required=True, help="output root: directory or s3://bucket")
    return p


def bbox(text: str) -> tuple[float, float, float, float]:
    """'47.4,-122.5,47.8,-121.9' -> (min_lat, min_lon, max_lat, max_lon)."""
    try:
        v = tuple(float(x) for x in text.split(","))
    except ValueError:
        v = ()
    if len(v) != 4 or not (-90 <= v[0] <= v[2] <= 90 and -180 <= v[1] <= v[3] <= 180):
        raise argparse.ArgumentTypeError(f"minlat,minlon,maxlat,maxlon expected, got {text!r}")
    return v[0], v[1], v[2], v[3]


def _located(run: TopologyRun) -> list[tuple[float, float]]:
    return [
        (n.lat, n.lon)
        for k in run.graph.nodes
        if (n := run.nodes[k]).lat is not None and n.lon is not None
    ]


def node_bbox(
    points: list[tuple[float, float]], pad_deg: float
) -> tuple[float, float, float, float] | None:
    """Box of the points plus pad_deg, snapped outward to 0.1 degree; None without points.

    The snap means the box (logged, printed, used for tile names) never carries a raw
    node coordinate. shortcut: no antimeridian handling, upgrade for a region spanning 180.
    """
    if not points:
        return None
    lats, lons = [p[0] for p in points], [p[1] for p in points]
    w, s, e, n = terrain_tiles.snap_bounds_outward(
        min(lats) - pad_deg, min(lons) - pad_deg, max(lats) + pad_deg, max(lons) + pad_deg
    )
    return s, w, n, e


def _box(args: argparse.Namespace, nothing: str) -> tuple[float, float, float, float] | None:
    """--bbox as given, or the box of the region's located nodes (node_bbox). None, with a
    warning naming `nothing` (what is skipped), while the region has no located node yet."""
    if args.bbox is not None:
        given: tuple[float, float, float, float] = args.bbox
        return given
    if args.pad_deg < 0:
        raise SystemExit("--pad-deg must be >= 0")
    try:
        points = _located(_run(args))
    except NoReceptionsError:
        points = []
    b = node_bbox(points, args.pad_deg)
    if b is None:
        log.warning("no located nodes in %s yet: %s", args.region, nothing)
    return b


def _terrain(run: TopologyRun, dem_dir: Path | None, pad_deg: float = 0.0) -> tuple[Terrain, str]:
    """GLO-30 over the located nodes' box (plus pad_deg), or flat terrain.

    Flat, labelled FLAT, without --dem-dir, without located nodes, or when fetch-dem hasn't
    fetched a tile of the box yet (fresh region, or a node newer than the last fetch):
    honestly flat beats unfetched tiles silently read as 0 m.
    """
    points = _located(run)
    if dem_dir is None:
        return FlatTerrain(), FLAT
    if not points:
        log.warning("--dem-dir given but no node has a location: using flat terrain")
        return FlatTerrain(), FLAT
    lats, lons = [p[0] for p in points], [p[1] for p in points]
    b = (min(lats) - pad_deg, min(lons) - pad_deg, max(lats) + pad_deg, max(lons) + pad_deg)
    missing = missing_tiles(dem_dir, *b)
    if missing:
        log.warning(
            "DEM tiles not fetched in %s (run fetch-dem): %s; using flat terrain", dem_dir, missing
        )
        return FlatTerrain(), FLAT
    return load_glo30(dem_dir, *b), "Copernicus GLO-30"


def _propagation(run: TopologyRun, dem_dir: Path | None, out: str) -> dict[str, Any]:
    t0 = time.perf_counter()
    terrain, dem = _terrain(run, dem_dir)
    t_dem = time.perf_counter() - t0

    t0 = time.perf_counter()
    radio = Radio()
    model = FsplKnifeEdge(terrain, radio)
    rows = compare.residuals(run, model, radio)
    unseen = compare.unseen_strong(run, model, radio)
    t_model = time.perf_counter() - t0

    t0 = time.perf_counter()
    profiles = compare.profiles(run, model, radio)
    t_profiles = time.perf_counter() - t0

    model_meta = {"name": model.name, "version": model.version, "dem": dem}
    docs = {
        "residuals": compare.residuals_json(run, rows, model_meta),
        "surprising": compare.surprising_json(run, rows, unseen, radio),
    }
    surprising = docs["surprising"]
    n = [len(p["ground_m"]) for p in profiles]
    return {
        "written": write_summaries(run.region, docs, out)
        + write_summaries(
            run.region,
            compare.shard_profiles(compare.profiles_json(run, profiles, model_meta, radio)),
            out,
            compact=True,
        ),
        "link_profiles": {
            "count": len(profiles),
            "n_min": min(n, default=0),
            "n_max": max(n, default=0),
        },
        "model": model_meta,
        "summary": docs["residuals"]["summary"],
        "observed_but_predicted_impossible": len(surprising["observed_but_predicted_impossible"]),
        "predicted_strong_but_never_seen": len(surprising["predicted_strong_but_never_seen"]),
        "profiles": model.profiles,
        "profiles_per_s": round(model.profiles / t_model) if t_model else None,
        "timings_s": {k: round(v, 3) for k, v in run.timings_s.items()}
        | {
            "dem_load": round(t_dem, 3),
            "propagation": round(t_model, 3),
            "link_profiles": round(t_profiles, 3),
        },
    }


def _placement(run: TopologyRun, dem_dir: Path | None, out: str) -> dict[str, Any]:
    t0 = time.perf_counter()
    # Pad by two cells: candidates reach one cell beyond the nodes' box.
    terrain, dem = _terrain(run, dem_dir, pad_deg=PLACEMENT_DEM_PAD_DEG)
    t_dem = time.perf_counter() - t0

    t0 = time.perf_counter()
    radio = Radio()
    model = FsplKnifeEdge(terrain, radio)
    sites = {k: s for k in run.graph.nodes if (s := compare.site(run.nodes[k], radio)) is not None}
    cells = placement.grid_cells(_located(run))
    if dem != FLAT:  # flat terrain is all 0 m: it would read every cell as water
        cells = placement.on_land(cells, terrain)
    candidates = placement.recommend(run.graph, sites, cells, run.resilience.spofs, model, radio)
    t_model = time.perf_counter() - t0

    method = {
        "grid_deg": GRID_DEG,
        "candidates_scored": len(cells),
        "antenna_height_m": radio.infrastructure_height_m,
        "model": f"{model.name} v{model.version}",
        "dem": dem,
        "spofs_checked_top": placement.SPOF_CHECK_TOP_K,
    }
    doc = placement.placement_json(run, candidates, method)

    # Coverage: every grid cell, water included. Its own model instance, so its own counter.
    t0 = time.perf_counter()
    cov_model = FsplKnifeEdge(terrain, radio)
    tx = [
        (n.lat, n.lon)
        for k in run.graph.nodes
        if (n := run.nodes[k]).role in coverage.TRANSMITTER_ROLES
        and n.lat is not None
        and n.lon is not None
    ]
    cov = coverage.coverage(placement.grid_cells(_located(run)), tx, cov_model, radio)
    t_cov = time.perf_counter() - t0
    cov_method = {
        "grid_deg": GRID_DEG,
        "model": f"{model.name} v{model.version}",
        "dem": dem,
        "tx_height_m": radio.infrastructure_height_m,
        "rx_height_m": radio.client_height_m,
        "max_distance_km": placement.MAX_LINK_DISTANCE_KM,
        "snr_floor_db": radio.snr_floor_db,
        "transmitters": len(tx),
    }
    covered = [v for v in cov.snr_db if v is not None]
    n_cells = max(1, len(cov.snr_db))
    return {
        "written": write_summaries(run.region, {"placement": doc}, out)
        + write_summaries(
            run.region,
            {"coverage": coverage.coverage_json(run, cov, cov_method)},
            out,
            compact=True,
        ),
        "method": method,
        "top": doc["candidates"][:5],
        "profiles": model.profiles,
        "profiles_per_s": round(model.profiles / t_model) if t_model else None,
        "coverage": {
            "rows": cov.rows,
            "cols": cov.cols,
            "transmitters": len(tx),
            "non_null_fraction": round(len(covered) / n_cells, 4),
            "above_floor_fraction": round(
                sum(v >= radio.snr_floor_db for v in covered) / n_cells, 4
            ),
            "profiles": cov_model.profiles,
            "profiles_per_s": round(cov_model.profiles / t_cov) if t_cov else None,
        },
        "timings_s": {k: round(v, 3) for k, v in run.timings_s.items()}
        | {
            "dem_load": round(t_dem, 3),
            "placement": round(t_model, 3),
            "coverage": round(t_cov, 3),
        },
    }


def _terrain_tiles(args: argparse.Namespace) -> dict[str, Any]:
    """Write summaries/region=R/terrain/{z}/{x}/{y}.png and terrain.json (docs/api-contract.md)."""
    if not 0 <= args.minzoom <= args.maxzoom <= terrain_tiles.MAX_ZOOM:
        raise SystemExit(f"need 0 <= --minzoom <= --maxzoom <= {terrain_tiles.MAX_ZOOM}")
    b = _box(args, "nothing to render")
    if b is None:
        return {"box": None, "skipped": f"no located nodes in {args.region} yet", "written": []}
    west, south, east, north = bounds = terrain_tiles.snap_bounds_outward(*b)
    # Refuse rather than publish flat tiles: an unfetched tile would be drawn as 0 m.
    missing = missing_tiles(args.dem_dir, south, west, north, east)
    if missing:
        raise SystemExit(f"DEM tiles missing in {args.dem_dir} (run fetch-dem): {missing}")
    t0 = time.perf_counter()
    terrain = load_glo30(args.dem_dir, south, west, north, east)
    t_dem = time.perf_counter() - t0

    t0 = time.perf_counter()
    fs, base = output_fs(args.out)
    d = f"{base}/summaries/region={args.region}/terrain"
    count = size = 0
    lo, hi = math.inf, -math.inf
    for t in terrain_tiles.render_all(terrain, bounds, args.minzoom, args.maxzoom):
        fs.create_dir(f"{d}/{t.z}/{t.x}", recursive=True)
        with fs.open_output_stream(f"{d}/{t.z}/{t.x}/{t.y}.png") as f:
            f.write(t.png)
        count, size = count + 1, size + len(t.png)
        lo, hi = min(lo, t.min_m), max(hi, t.max_m)
    t_tiles = time.perf_counter() - t0

    doc = {
        "meta": {
            "schema_version": 1,
            "region": args.region,
            "generated_at": datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "window": None,
            "source": "terrain-job",
        },
        "encoding": "mapbox",
        "tile_size": terrain_tiles.TILE_SIZE,
        "minzoom": args.minzoom,
        "maxzoom": args.maxzoom,
        "bounds": bounds,
        "dem": "Copernicus GLO-30",
        "tiles": count,
    }
    return {
        "written": [*write_summaries(args.region, {"terrain": doc}, args.out), d],
        "tiles": count,
        "bytes": size,
        "bounds": bounds,
        "elevation_m": {"min": round(lo, 1), "max": round(hi, 1)},
        "tiles_per_s": round(count / t_tiles) if t_tiles else None,
        "timings_s": {"dem_load": round(t_dem, 3), "tiles": round(t_tiles, 3)},
    }


def _run(args: argparse.Namespace) -> TopologyRun:
    if (args.frm is None) != (args.to is None):
        raise SystemExit("--from and --to go together")
    if args.frm is None:
        frm, to = default_window(args.root, args.region, args.window)
    else:
        frm, to = args.frm, args.to
    return run_topology(args.root, args.region, frm, to, args.min_confidence, args.advert_lookback)


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    if args.command == "fetch-dem":
        b = _box(args, "nothing to fetch")
        if b is None:
            _print({"box": None, "skipped": f"no located nodes in {args.region} yet"})
        else:
            _print({"box": list(b), **fetch_glo30(args.dem_dir, *b)})
        return 0
    if args.command == "terrain-tiles":
        _print(_terrain_tiles(args))
        return 0
    run = _run(args)
    report: dict[str, Any]
    if args.command == "propagation":
        report = _propagation(run, args.dem_dir, args.out)
    elif args.command == "placement":
        report = _placement(run, args.dem_dir, args.out)
    elif args.command == "topology":
        paths = write_all(run, args.out)
        report = {
            "written": paths,
            "nodes": run.graph.number_of_nodes(),
            "links": run.graph.number_of_edges(),
            "spofs": len(run.resilience.spofs),
            "timings_s": {k: round(v, 3) for k, v in run.timings_s.items()},
            "chain_cache_hit_rate": round(run.cache_hit_rate, 4),
            "distinct_chains": run.cache_misses,
        }
    else:
        report = evaluate(run, load_truth(args.truth))
    _print(report)
    return 0


def _print(report: dict[str, Any]) -> None:
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
