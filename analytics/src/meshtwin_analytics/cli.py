"""Command line: `meshtwin-analytics topology ...` and `meshtwin-analytics evaluate ...`."""

import argparse
import json
import re
import sys
from datetime import UTC, datetime

from .evaluate import evaluate, load_truth
from .load import to_us
from .output import write_all
from .pipeline import (
    DAY_US,
    DEFAULT_ADVERT_LOOKBACK_US,
    TopologyRun,
    default_window,
    run_topology,
)

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
    e = sub.add_parser("evaluate", help="score the inferred graph against truth.json")
    common(e)
    e.add_argument("--truth", required=True)
    return p


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
    run = _run(args)
    if args.command == "topology":
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
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0
