"""S3/MinIO wiring: only the config builder (no MinIO in unit tests)."""

from meshtwin_analytics.storage import S3Config, table_glob


def test_from_env_defaults_to_plain_http_minio() -> None:
    cfg = S3Config.from_env(
        {"S3_ENDPOINT": "minio:9000", "S3_ACCESS_KEY": "k", "S3_SECRET_KEY": "s"}
    )
    assert cfg == S3Config("minio:9000", "k", "s", tls=False)


def test_from_env_tls_and_scheme() -> None:
    assert S3Config.from_env({"S3_ENDPOINT": "minio:9000", "S3_TLS": "true"}).tls
    cfg = S3Config.from_env({"S3_ENDPOINT": "https://s3.example.com"})
    assert cfg.endpoint == "s3.example.com"
    assert cfg.tls


def test_duckdb_secret_sql() -> None:
    sql = S3Config("minio:9000", "key", "it's", tls=False).duckdb_secret_sql()
    assert "TYPE s3" in sql
    assert "ENDPOINT 'minio:9000'" in sql
    assert "URL_STYLE 'path'" in sql
    assert "USE_SSL false" in sql
    assert "SECRET 'it''s'" in sql  # quotes escaped


def test_table_glob_for_bucket() -> None:
    assert (
        table_glob("s3://meshtwin/", "receptions")
        == "s3://meshtwin/receptions/region=*/date=*/hour=*/*.parquet"
    )
