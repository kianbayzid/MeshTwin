"""CLI end to end on both fixtures, then privacy, contract and evaluation checks on the output.

Privacy is a hard rule (CLAUDE.md): if a test here fails, fix the code, never the test.
"""

import json
import math
import re
from pathlib import Path
from typing import Any

import duckdb
import pyarrow.parquet as pq
import pytest

from meshtwin_analytics.cli import main
from meshtwin_analytics.evaluate import evaluate, load_truth
from meshtwin_analytics.pipeline import TopologyRun

from .conftest import HASH1, HASH3, REPO

FIXTURES = {"hash1": HASH1, "hash3": HASH3}


@pytest.fixture(scope="module", params=sorted(FIXTURES))
def out_dir(request: pytest.FixtureRequest, tmp_path_factory: pytest.TempPathFactory) -> Path:
    out = tmp_path_factory.mktemp(request.param, numbered=False)
    assert main(["topology", "--root", str(FIXTURES[request.param]), "--out", str(out)]) == 0
    return out


def _summary(out: Path, name: str) -> dict[str, Any]:
    data: dict[str, Any] = json.loads(
        (out / "summaries" / "region=SEA" / f"{name}.json").read_text(encoding="utf-8")
    )
    return data


def _walk(x: Any, key: str = "") -> list[tuple[str, Any]]:
    """Every (key, scalar) pair in a JSON document."""
    if isinstance(x, dict):
        return [kv for k, v in x.items() for kv in _walk(v, k)]
    if isinstance(x, list):
        return [kv for v in x for kv in _walk(v, key)]
    return [(key, x)]


def test_cli_writes_all_outputs(out_dir: Path) -> None:
    for table in ("topology_nodes", "topology_links", "resilience"):
        [f] = (out_dir / "results" / table).rglob("*.parquet")
        assert "region=SEA" in f.as_posix()
        pq.read_table(f)
    topo = _summary(out_dir, "topology")
    assert topo["stats"]["receptions"] == 75_571
    assert topo["stats"]["nodes"] == len(topo["nodes"]) > 0
    assert topo["stats"]["links"] == len(topo["links"]) > 0
    assert all(link["confidence"] >= 0.5 for link in topo["links"])


# --- privacy -----------------------------------------------------------------------------


def _raw_coordinates(root: Path) -> set[float]:
    rows = duckdb.execute(
        f"SELECT DISTINCT lat, lon FROM read_parquet('{root.as_posix()}/adverts/*/*/*/*.parquet') "
        "WHERE lat IS NOT NULL"
    ).fetchall()
    return {float(v) for row in rows for v in row}


@pytest.mark.parametrize("name", ["topology", "resilience"])
def test_privacy(out_dir: Path, name: str) -> None:
    doc = _summary(out_dir, name)
    text = json.dumps(doc)
    root = FIXTURES[out_dir.name]

    # 1. Every coordinate is on the 0.01 grid with at most two decimals.
    locations = [v for k, v in _walk_dicts(doc) if k == "location" and v is not None]
    assert locations or name == "resilience"
    for loc in locations:
        assert loc["grid_deg"] == 0.01
        for v in (loc["lat"], loc["lon"]):
            assert round(v, 2) == v
            assert len(repr(v).split(".")[-1]) <= 2
            assert math.isclose(v * 100, round(v * 100), abs_tol=1e-6)

    # 2. No raw advert coordinate appears anywhere, as a number or as text.
    raw = _raw_coordinates(root)
    numbers = {v for _, v in _walk(doc) if isinstance(v, float)}
    assert not numbers & raw
    assert not any(repr(c)[:8] in text for c in raw)  # 5+ decimals of a raw value

    # 3. No full public keys: no hex run longer than the 8-char id.
    assert not re.search(r"[0-9A-Fa-f]{9,}", text)

    # 4. Names only for infrastructure.
    for node in _nodes(doc):
        if node["role"] not in ("repeater", "room"):
            assert node["name"] is None, node


def _walk_dicts(x: Any) -> list[tuple[str, Any]]:
    if isinstance(x, dict):
        return [(k, v) for k, v in x.items()] + [kv for v in x.values() for kv in _walk_dicts(v)]
    if isinstance(x, list):
        return [kv for v in x for kv in _walk_dicts(v)]
    return []


def _nodes(doc: dict[str, Any]) -> list[dict[str, Any]]:
    nodes: list[dict[str, Any]] = doc.get("nodes", []) + doc.get("single_points_of_failure", [])
    return nodes


def test_companion_names_are_null(out_dir: Path) -> None:
    companions = [n for n in _summary(out_dir, "topology")["nodes"] if n["role"] == "companion"]
    assert companions
    assert all(n["name"] is None for n in companions)


# --- contract ----------------------------------------------------------------------------

# Fields the contract allows to be null even though its example shows a value.
NULLABLE = {"location", "name", "snr_db", "rssi_dbm", "last_heard", "first_seen", "last_seen"}


def _contract_example(endpoint: str) -> dict[str, Any]:
    """The JSON example under an endpoint heading in docs/api-contract.md (read, not copied)."""
    md = (REPO / "docs" / "api-contract.md").read_text(encoding="utf-8")
    section = md.split(f"### `GET /api/v1/regions/{{region}}/{endpoint}`", 1)[1]
    block = re.search(r"```json\n(.*?)```", section, re.S)
    assert block
    data: dict[str, Any] = json.loads(block.group(1))
    return data


def _kind(v: Any) -> str:
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, int | float):
        return "number"
    return type(v).__name__


def _conforms(got: Any, want: Any, path: str) -> list[str]:
    """Every field in the example exists in `got` with the same JSON type, recursively."""
    key = path.rsplit(".", 1)[-1].rstrip("[]")
    if got is None:
        return [] if want is None or key in NULLABLE else [f"{path}: null"]
    if want is None:
        return []  # example shows null (e.g. a companion's name); any value type is fine
    if _kind(got) != _kind(want):
        return [f"{path}: {_kind(got)} != {_kind(want)}"]
    if isinstance(want, dict):
        errs = [f"{path}.{k}: missing" for k in want if k not in got]
        return errs + [
            e for k in want if k in got for e in _conforms(got[k], want[k], f"{path}.{k}")
        ]
    if isinstance(want, list) and want:
        return [e for item in got for e in _conforms(item, want[0], f"{path}[]")]
    return []


@pytest.mark.parametrize("name", ["topology", "resilience"])
def test_contract(out_dir: Path, name: str) -> None:
    doc = _summary(out_dir, name)
    assert _conforms(doc, _contract_example(name), name) == []
    assert doc["meta"]["source"] == "topology-job"
    assert re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", doc["meta"]["window"]["to"])


def test_contract_checker_catches_drift() -> None:
    want = {"a": 1, "b": {"c": "x"}, "location": {"lat": 1.0}}
    assert _conforms({"a": 2, "b": {"c": "y"}, "location": None}, want, "t") == []
    assert _conforms({"a": "2", "b": {}, "location": None}, want, "t") == [
        "t.a: str != number",
        "t.b.c: missing",
    ]


def test_spofs_sorted_and_positive(out_dir: Path) -> None:
    spofs = _summary(out_dir, "resilience")["single_points_of_failure"]
    lost = [s["reachable_pairs_lost"] for s in spofs]
    assert lost == sorted(lost, reverse=True)
    assert all(x > 0 for x in lost)


# --- evaluation floors (measured: see analytics/README.md) -------------------------------


def test_evaluation_hash3(run_hash3: TopologyRun) -> None:
    m = evaluate(run_hash3, load_truth(HASH3 / "truth.json"))
    assert m["links"]["precision"] >= 0.99  # measured 1.000
    assert m["recall_links_touching_relay"] >= 0.95  # measured 0.997
    assert m["spof"]["precision"] >= 0.99  # measured 1.0
    assert m["spof"]["recall"] >= 0.99  # measured 1.0


def test_evaluation_hash1(run_hash1: TopologyRun) -> None:
    m = evaluate(run_hash1, load_truth(HASH1 / "truth.json"))
    assert m["links"]["precision"] >= 0.97  # measured 0.994
    assert m["recall_links_touching_relay"] >= 0.93  # measured 0.966
    assert m["spof"]["precision"] >= 0.8  # measured 1.0
    assert m["spof"]["recall"] >= 0.8  # measured 1.0


def test_collisions_cost_something(run_hash1: TopologyRun, run_hash3: TopologyRun) -> None:
    e1, e3 = run_hash1.evidence, run_hash3.evidence
    assert e1.ambiguous_hops / e1.hop_entries > 0.01
    assert e3.ambiguous_hops == 0
    assert run_hash1.cache_hit_rate > 0.8  # identical chains repeat: the memo pays off
