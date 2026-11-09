"""Where data lives: a local directory or an S3/MinIO bucket.

Reads go through DuckDB (httpfs for S3); writes go through a pyarrow filesystem.
Both are configured from the same env vars the Go ingester uses
(S3_ENDPOINT host:port, S3_ACCESS_KEY, S3_SECRET_KEY, S3_TLS=true|false).
"""

import os
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

import duckdb
from pyarrow import fs


@dataclass(frozen=True)
class S3Config:
    endpoint: str  # host:port, no scheme (MinIO style)
    access_key: str
    secret_key: str
    tls: bool

    @classmethod
    def from_env(cls, env: Mapping[str, str] = os.environ) -> "S3Config":
        endpoint = env.get("S3_ENDPOINT", "localhost:9000")
        # Accept "http://minio:9000" too, and let the scheme decide TLS when S3_TLS is unset.
        tls = env.get("S3_TLS", "true" if endpoint.startswith("https://") else "false") == "true"
        endpoint = endpoint.removeprefix("http://").removeprefix("https://")
        return cls(endpoint, env.get("S3_ACCESS_KEY", ""), env.get("S3_SECRET_KEY", ""), tls)

    def duckdb_secret_sql(self) -> str:
        """DuckDB httpfs secret. Path-style URLs because MinIO serves buckets as paths."""

        def q(s: str) -> str:
            return "'" + s.replace("'", "''") + "'"

        return (
            "CREATE OR REPLACE SECRET meshtwin_s3 (TYPE s3, "
            f"KEY_ID {q(self.access_key)}, SECRET {q(self.secret_key)}, "
            f"ENDPOINT {q(self.endpoint)}, URL_STYLE 'path', "
            f"USE_SSL {'true' if self.tls else 'false'}, REGION 'us-east-1')"
        )

    def arrow_filesystem(self) -> fs.S3FileSystem:
        return fs.S3FileSystem(
            access_key=self.access_key,
            secret_key=self.secret_key,
            endpoint_override=self.endpoint,
            scheme="https" if self.tls else "http",
            region="us-east-1",
        )


def is_s3(root: str) -> bool:
    return root.startswith("s3://")


def connect(root: str) -> duckdb.DuckDBPyConnection:
    """A DuckDB connection in UTC, with S3 credentials when root is a bucket."""
    con = duckdb.connect()
    con.execute("SET TimeZone = 'UTC'")
    if is_s3(root):
        con.execute("INSTALL httpfs")
        con.execute("LOAD httpfs")
        con.execute(S3Config.from_env().duckdb_secret_sql())
    return con


def table_glob(root: str, table: str) -> str:
    """Glob over every Hive partition of one table (region/date/hour)."""
    base = root.rstrip("/") if is_s3(root) else Path(root).resolve().as_posix()
    return f"{base}/{table}/region=*/date=*/hour=*/*.parquet"


def output_fs(root: str) -> tuple[fs.FileSystem, str]:
    """(filesystem, base path) for writing results under root."""
    if is_s3(root):
        return S3Config.from_env().arrow_filesystem(), root.removeprefix("s3://").rstrip("/")
    return fs.LocalFileSystem(), Path(root).resolve().as_posix()
