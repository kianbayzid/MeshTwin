"""Write the topology job's results under an output root (directory or s3://bucket)."""

import json
from datetime import UTC, datetime

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
    d = f"{base}/summaries/region={run.region}"
    fs.create_dir(d, recursive=True)
    for name, doc in docs.items():
        with fs.open_output_stream(f"{d}/{name}.json") as f:
            f.write(json.dumps(doc, indent=2).encode())
        written.append(f"{d}/{name}.json")
    return written
