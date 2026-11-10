"""Command line: `meshtwin-analytics topology|propagation|placement|evaluate|fetch-dem ...`."""

import argparse
import json
import logging
import re
import sys
import time
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from . import compare, placement
from .evaluate import evaluate, load_truth
from .geo import GRID_DEG
from .load import to_us
from .output import write_all, write_summaries
from .pipeline import (
    DAY_US,
    DEFAULT_ADVERT_LOOKBACK_US,
    TopologyRun,
    default_window,
    run_topology,
)
from .propagation import FsplKnifeEdge, Radio
from .terrain import FlatTerrain, Terrain, fetch_glo30, load_glo30

log = logging.getLogger(__name__)

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
    f.add_argument("--bbox", type=bbox, required=True, help="minlat,minlon,maxlat,maxlon")
    f.add_argument("--dem-dir", type=Path, required=True)
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


def _terrain(run: TopologyRun, dem_dir: Path | None, pad_deg: float = 0.0) -> tuple[Terrain, str]:
    """GLO-30 over the located nodes' box (plus pad_deg), or flat terrain without --dem-dir."""
    points = _located(run)
    if dem_dir is None:
        return FlatTerrain(), "flat (no DEM)"
    if not points:
        log.warning("--dem-dir given but no node has a location: using flat terrain")
        return FlatTerrain(), "flat (no DEM)"
    lats, lons = [p[0] for p in points], [p[1] for p in points]
    terrain = load_glo30(
        dem_dir, min(lats) - pad_deg, min(lons) - pad_deg, max(lats) + pad_deg, max(lons) + pad_deg
    )
    return terrain, "Copernicus GLO-30"


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

    model_meta = {"name": model.name, "version": model.version, "dem": dem}
    docs = {
        "residuals": compare.residuals_json(run, rows, model_meta),
        "surprising": compare.surprising_json(run, rows, unseen, radio),
    }
    surprising = docs["surprising"]
    return {
        "written": write_summaries(run.region, docs, out),
        "model": model_meta,
        "summary": docs["residuals"]["summary"],
        "observed_but_predicted_impossible": len(surprising["observed_but_predicted_impossible"]),
        "predicted_strong_but_never_seen": len(surprising["predicted_strong_but_never_seen"]),
        "profiles": model.profiles,
        "profiles_per_s": round(model.profiles / t_model) if t_model else None,
        "timings_s": {k: round(v, 3) for k, v in run.timings_s.items()}
        | {"dem_load": round(t_dem, 3), "propagation": round(t_model, 3)},
    }


def _placement(run: TopologyRun, dem_dir: Path | None, out: str) -> dict[str, Any]:
    t0 = time.perf_counter()
    # Pad by two cells: candidates reach one cell beyond the nodes' box.
    terrain, dem = _terrain(run, dem_dir, pad_deg=2 * GRID_DEG)
    t_dem = time.perf_counter() - t0

    t0 = time.perf_counter()
    radio = Radio()
    model = FsplKnifeEdge(terrain, radio)
    sites = {k: s for k in run.graph.nodes if (s := compare.site(run.nodes[k], radio)) is not None}
    cells = placement.grid_cells(_located(run))
    if dem_dir is not None:
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
    return {
        "written": write_summaries(run.region, {"placement": doc}, out),
        "method": method,
        "top": doc["candidates"][:5],
        "profiles": model.profiles,
        "profiles_per_s": round(model.profiles / t_model) if t_model else None,
        "timings_s": {k: round(v, 3) for k, v in run.timings_s.items()}
        | {"dem_load": round(t_dem, 3), "placement": round(t_model, 3)},
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
        _print(fetch_glo30(args.dem_dir, *args.bbox))
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
