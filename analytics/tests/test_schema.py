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


def _one_file(table: str) -> Path:
    return next((HASH1 / table).rglob("*.parquet"))


@pytest.mark.parametrize("table", sorted(SCHEMA["tables"]))
def test_parquet_matches_schema_json(table: str) -> None:
    path = _one_file(table)
    # hive_partitioning off: the date/hour path parts are not file columns.
    sql = f"DESCRIBE SELECT * FROM read_parquet('{path.as_posix()}', hive_partitioning = false)"
    rows = duckdb.execute(sql).fetchall()
    got = [(name, typ) for name, typ, *_ in rows]
    want = [(c["name"], DUCKDB_TYPES[c["type"]]) for c in SCHEMA["tables"][table]]
    assert got == want

    # DuckDB does not report nullability; the Parquet repetition (required/optional) does.
    arrow = pq.read_schema(path)
    for col in SCHEMA["tables"][table]:
        assert arrow.field(col["name"]).nullable == col["nullable"], col["name"]


def test_schema_version_in_rows() -> None:
    path = _one_file("receptions").as_posix()
    versions = duckdb.execute(
        f"SELECT DISTINCT schema_version FROM read_parquet('{path}')"
    ).fetchall()
    assert versions == [(SCHEMA["schema_version"],)]
