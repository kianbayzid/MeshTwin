"""At-least-once delivery: the same reception landing in two Parquet files must count once."""

import shutil
from pathlib import Path

import duckdb
import pytest

from meshtwin_analytics.pipeline import TopologyRun

from .conftest import HASH1, _run


def test_duplicate_files_count_once(tmp_path: Path, run_hash1: TopologyRun) -> None:
    root = tmp_path / "dup"
    shutil.copytree(HASH1, root)
    files = list(root.glob("*/region=*/date=*/hour=*/*.parquet"))
    assert files
    for f in files:  # a redelivered copy of every file, same partition, new file name
        shutil.copy(f, f.with_name("redelivered-" + f.name))

    glob = f"{root.as_posix()}/receptions/*/*/*/*.parquet"
    row = duckdb.connect().execute(f"SELECT count(*) FROM '{glob}'").fetchone()
    assert row is not None and row[0] >= 2 * run_hash1.counts.receptions  # really duplicated

    run = _run(root)
    assert run.counts == run_hash1.counts
    assert run.nodes == run_hash1.nodes
    assert run.evidence.links.keys() == run_hash1.evidence.links.keys()
    for pair, want in run_hash1.evidence.links.items():
        got = run.evidence.links[pair]
        assert got.observations == pytest.approx(want.observations)
        assert sorted(got.snr) == sorted(want.snr)
        assert sorted(got.rssi) == sorted(want.rssi)
    assert set(run.graph.edges) == set(run_hash1.graph.edges)
