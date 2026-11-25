"""Data provenance: schema v1 files (no data_source column) and v2 files read together, and
topology meta.data_sources counts reception rows per source."""

import shutil
from pathlib import Path

import duckdb

from meshtwin_analytics.load import DATA_SOURCES
from meshtwin_analytics.pipeline import TopologyRun
from meshtwin_analytics.summaries import topology_json

from .conftest import HASH1, _run


def _to_v2(f: Path, source: str) -> int:
    """Rewrite one v1 fixture file in place as schema v2 tagged `source`; returns its rows."""
    tmp = f.with_suffix(".v2")
    duckdb.execute(
        f"""COPY (SELECT * REPLACE (2::SMALLINT AS schema_version), '{source}' AS data_source
                  FROM read_parquet('{f.as_posix()}', hive_partitioning = false))
            TO '{tmp.as_posix()}' (FORMAT parquet)"""
    )
    tmp.replace(f)
    row = duckdb.execute(f"SELECT count(*) FROM read_parquet('{f.as_posix()}')").fetchone()
    assert row is not None
    return int(row[0])


def _files(root: Path, table: str) -> list[Path]:
    return sorted((root / table).rglob("*.parquet"))


def _same_topology(run: TopologyRun, base: TopologyRun) -> None:
    assert (run.counts.receptions, run.counts.flood_receptions, run.counts.observers) == (
        base.counts.receptions,
        base.counts.flood_receptions,
        base.counts.observers,
    )
    assert run.nodes == base.nodes
    assert set(run.graph.edges) == set(base.graph.edges)


def test_v1_only_is_all_unknown(run_hash1: TopologyRun) -> None:
    ds = topology_json(run_hash1)["meta"]["data_sources"]
    assert list(ds) == list(DATA_SOURCES)
    assert ds == {"live": 0, "replay": 0, "synthetic": 0, "unknown": run_hash1.counts.receptions}


def test_v2_only(tmp_path: Path, run_hash1: TopologyRun) -> None:
    root = tmp_path / "v2"
    shutil.copytree(HASH1, root)
    n = sum(_to_v2(f, "synthetic") for t in ("receptions", "adverts") for f in _files(root, t))
    assert n > 0

    run = _run(root)
    _same_topology(run, run_hash1)
    ds = topology_json(run)["meta"]["data_sources"]
    assert ds == {"live": 0, "replay": 0, "synthetic": run.counts.receptions, "unknown": 0}


def test_mixed_v1_v2(tmp_path: Path, run_hash1: TopologyRun) -> None:
    root = tmp_path / "mixed"
    shutil.copytree(HASH1, root)
    rx = _files(root, "receptions")
    assert len(rx) >= 3
    live = _to_v2(rx[0], "live")
    replay = _to_v2(rx[1], "replay")
    _to_v2(_files(root, "adverts")[0], "live")  # adverts mixed too: the registry must still read

    run = _run(root)
    _same_topology(run, run_hash1)
    ds = topology_json(run)["meta"]["data_sources"]
    assert ds == {
        "live": live,
        "replay": replay,
        "synthetic": 0,
        "unknown": run.counts.receptions - live - replay,
    }
    assert live > 0 and replay > 0 and ds["unknown"] > 0  # really three sources in one window
