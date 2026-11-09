"""Shared fixtures: paths to the Parquet fixtures and one topology run per fixture."""

from pathlib import Path

import pytest

from meshtwin_analytics.pipeline import DAY_US, TopologyRun, default_window, run_topology

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "testdata" / "parquet"
HASH1 = FIXTURES / "sea-seed1-2h-hash1"
HASH3 = FIXTURES / "sea-seed1-2h-hash3"


def _run(root: Path) -> TopologyRun:
    frm, to = default_window(str(root), "SEA", DAY_US)
    return run_topology(str(root), "SEA", frm, to)


@pytest.fixture(scope="session")
def run_hash1() -> TopologyRun:
    return _run(HASH1)


@pytest.fixture(scope="session")
def run_hash3() -> TopologyRun:
    return _run(HASH3)
