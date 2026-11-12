"""Write the jobs' results under an output root (directory or s3://bucket)."""

import json
from datetime import UTC, datetime
from typing import Any

import pyarrow.parquet as pq

from . import summaries
from .pipeline import TopologyRun
from .storage import output_fs


def write_all(run: TopologyRun, out_root: str) -> list[str]:
    """Writes internal Parquet results and the two API summaries. Returns the paths."""
    fs, base = output_fs(out_root)
    end = datetime.fromtimestamp(run.to_us / 1_000_000, UTC)
    part = f"region={run.region}/date={end:%Y-%m-%d}/hour={end:%H}"
    written = []

    tables = {
        "topology_nodes": summaries.nodes_table(run),
        "topology_links": summaries.links_table(run),
        "resilience": summaries.resilience_table(run),
    }
    for name, table in tables.items():
        d = f"{base}/results/{name}/{part}"
        fs.create_dir(d, recursive=True)
        pq.write_table(table, f"{d}/part-0.parquet", filesystem=fs, compression="zstd")
        written.append(f"{d}/part-0.parquet")

    docs = {
        "topology": summaries.topology_json(run),
        "resilience": summaries.resilience_json(run),
    }
    return written + write_summaries(run.region, docs, out_root)


def write_summaries(
    region: str, docs: dict[str, dict[str, Any]], out_root: str, compact: bool = False
) -> list[str]:
    """Writes summaries/region=<R>/<name>.json for each doc (name may hold a '/', e.g.
    "profiles/A"). Returns the paths.

    compact: no whitespace, for big files (the API refuses a summary over 8 MB).
    """
    fs, base = output_fs(out_root)
    d = f"{base}/summaries/region={region}"
    written = []
    for name, doc in docs.items():
        path = f"{d}/{name}.json"
        fs.create_dir(path.rsplit("/", 1)[0], recursive=True)
        with fs.open_output_stream(path) as f:
            text = json.dumps(doc, separators=(",", ":")) if compact else json.dumps(doc, indent=2)
            f.write(text.encode())
        written.append(path)
    return written
