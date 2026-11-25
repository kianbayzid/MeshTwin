"""The Parquet the Go ingester writes matches schema/schema.json, as DuckDB sees it.

Mirror of Go's TestSchemaMatchesJSON: the JSON is read, never hardcoded here.
"""

import json
from pathlib import Path

import duckdb
import pyarrow.parquet as pq
import pytest

from .conftest import HASH1, REPO

SCHEMA = json.loads((REPO / "schema" / "schema.json").read_text(encoding="utf-8"))

# schema.json type -> what DuckDB reports. DuckDB has one TIMESTAMPTZ and one BLOB type.
DUCKDB_TYPES = {
    "int8": "TINYINT",
    "int16": "SMALLINT",
    "float32": "FLOAT",
    "float64": "DOUBLE",
    "string": "VARCHAR",
    "list<string>": "VARCHAR[]",
    "list<float32>": "FLOAT[]",
    "timestamp[us, UTC]": "TIMESTAMP WITH TIME ZONE",
    "timestamp[ms, UTC]": "TIMESTAMP WITH TIME ZONE",
    "fixed_len_binary[8]": "BLOB",
    "fixed_len_binary[16]": "BLOB",
}


# Columns each schema version appended. The fixtures stay at the version they were generated
# with (regenerating needs Docker), so they are checked against schema.json minus the columns
# added after that version. Regenerate the fixtures to check the newest columns from Go output.
ADDED_IN = {2: {"data_source"}}


def _one_file(table: str) -> Path:
    return next((HASH1 / table).rglob("*.parquet"))


def _fixture_version() -> int:
    path = _one_file("receptions").as_posix()
    rows = duckdb.execute(f"SELECT DISTINCT schema_version FROM read_parquet('{path}')").fetchall()
    assert len(rows) == 1
    return int(rows[0][0])


def _later_columns(version: int) -> set[str]:
    return {c for v in range(version + 1, SCHEMA["schema_version"] + 1) for c in ADDED_IN[v]}


@pytest.mark.parametrize("table", sorted(SCHEMA["tables"]))
def test_parquet_matches_schema_json(table: str) -> None:
    path = _one_file(table)
    # hive_partitioning off: the date/hour path parts are not file columns.
    sql = f"DESCRIBE SELECT * FROM read_parquet('{path.as_posix()}', hive_partitioning = false)"
    rows = duckdb.execute(sql).fetchall()
    got = [(name, typ) for name, typ, *_ in rows]
    later = _later_columns(_fixture_version())
    cols = [c for c in SCHEMA["tables"][table] if c["name"] not in later]
    want = [(c["name"], DUCKDB_TYPES[c["type"]]) for c in cols]
    assert got == want

    # DuckDB does not report nullability; the Parquet repetition (required/optional) does.
    arrow = pq.read_schema(path)
    for col in cols:
        assert arrow.field(col["name"]).nullable == col["nullable"], col["name"]


def test_schema_version_in_rows() -> None:
    # Fixtures may lag schema.json, never lead it, and every newer version lists its columns
    # in ADDED_IN (a KeyError here means a schema bump without updating this file).
    version = _fixture_version()
    assert 1 <= version <= SCHEMA["schema_version"]
    later = _later_columns(version)
    assert all(later <= {c["name"] for c in SCHEMA["tables"][t]} for t in SCHEMA["tables"])
