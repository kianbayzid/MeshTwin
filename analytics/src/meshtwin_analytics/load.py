"""DuckDB queries over the ingester's Parquet. Filters are pushed into SQL so DuckDB
prunes partitions (region) and row groups (rx_time) instead of Python filtering rows.

Times are handled as integer microseconds since the epoch (UTC) inside analytics.
"""

from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

import duckdb

from .model import ROLE_NAMES, Node
from .storage import table_glob

# route_type 0 TRANSPORT_FLOOD and 1 FLOOD: the path is the relays actually traversed.
# Direct routes list hops still to go, so they are not evidence of links (protocol.md 2).
FLOOD_ROUTES = (0, 1)
TRACE_PAYLOAD = 9  # path bytes are SNR samples, not hashes


@dataclass(frozen=True)
class Reception:
    rx_us: int
    observer: str
    hops: tuple[str, ...]
    origin: str | None  # full key only (ADVERT, ANON_REQ); 2-hex src ids are too ambiguous
    snr_db: float | None
    rssi_dbm: float | None


@dataclass(frozen=True)
class Counts:
    receptions: int
    flood_receptions: int
    observers: int


def to_us(t: datetime) -> int:
    """Exact integer microseconds (float timestamp() would round)."""
    delta = t - datetime(1970, 1, 1, tzinfo=UTC)
    return (delta.days * 86_400 + delta.seconds) * 1_000_000 + delta.microseconds


def _src(root: str, table: str) -> str:
    return f"read_parquet('{table_glob(root, table)}', hive_partitioning = true)"


def max_rx_us(con: duckdb.DuckDBPyConnection, root: str, region: str) -> int | None:
    """Latest reception time, or None if the region (or the whole root) has none yet."""
    try:
        row = con.execute(
            f"SELECT epoch_us(max(rx_time)) FROM {_src(root, 'receptions')} WHERE region = ?",
            [region],
        ).fetchone()
    except duckdb.IOException as e:
        # An empty root (fresh cluster before the first flush) has nothing to glob: no data,
        # like an empty region. Any other IO error (S3, auth, permissions) still raises.
        if "No files found that match the pattern" not in str(e):
            raise
        # DuckDB says the same for a local root that is missing or a file: a typo or a missing
        # mount must fail, not skip forever.
        if not root.startswith("s3://") and not Path(root).is_dir():
            raise FileNotFoundError(f"--root {root} is not an existing directory") from e
        return None
    return None if row is None or row[0] is None else int(row[0])


def _window_sql() -> str:
    # Bounds are integer microseconds; comparing rx_time to a timestamp (not epoch_us(rx_time)
    # to an int) lets DuckDB use the Parquet min/max statistics to skip row groups.
    ts = "make_timestamp(?::BIGINT)::TIMESTAMPTZ"
    return f"region = ? AND rx_time >= {ts} AND rx_time < {ts}"


def _receptions(root: str) -> str:
    """Receptions in the window, one row per reception_id. At-least-once delivery (MQTT QoS 1
    redelivery, spool re-uploads) can put the same reception in two files (schema.md). The window
    predicate stays inside the subquery so Parquet statistics still prune row groups. Keep the
    earliest copy: rx_time can be ingester receive time, which differs between redeliveries.
    shortcut: copies straddling the window edge count once per window, upgrade if windows overlap.
    Params: region, frm, to."""
    return f"""(SELECT * FROM {_src(root, "receptions")} WHERE {_window_sql()}
              QUALIFY row_number() OVER (PARTITION BY reception_id ORDER BY rx_time) = 1)"""


def counts(con: duckdb.DuckDBPyConnection, root: str, region: str, frm: int, to: int) -> Counts:
    row = con.execute(
        f"""SELECT count(*),
                   count(*) FILTER (WHERE route_type IN {FLOOD_ROUTES}),
                   count(DISTINCT observer_id)
            FROM {_receptions(root)}""",
        [region, frm, to],
    ).fetchone()
    assert row is not None
    return Counts(int(row[0]), int(row[1]), int(row[2]))


def flood_receptions(
    con: duckdb.DuckDBPyConnection, root: str, region: str, frm: int, to: int
) -> list[Reception]:
    """Flood, non-TRACE receptions in [frm, to): the only rows that show traversed links."""
    rows = con.execute(
        f"""SELECT epoch_us(rx_time), observer_id, path_hops,
                   CASE WHEN length(src_id) = 64 THEN upper(src_id) END,
                   snr_db, rssi_dbm
            FROM {_receptions(root)}
            WHERE route_type IN {FLOOD_ROUTES} AND payload_type <> {TRACE_PAYLOAD}
            ORDER BY rx_time""",
        [region, frm, to],
    ).fetchall()
    return [
        Reception(int(t), obs.upper(), tuple(h.upper() for h in hops), origin, snr, rssi)
        for t, obs, hops, origin, snr, rssi in rows
    ]


@dataclass(frozen=True)
class Advert:
    node: Node
    rx_us: int


def latest_adverts(
    con: duckdb.DuckDBPyConnection, root: str, region: str, frm: int, to: int
) -> dict[str, Advert]:
    """Latest advert per pubkey in [frm, to): the node registry (role, name, position)."""
    rows = con.execute(
        f"""SELECT upper(pubkey), role, name, lat, lon, epoch_us(rx_time)
            FROM {_src(root, "adverts")} WHERE {_window_sql()}
            QUALIFY row_number() OVER (PARTITION BY upper(pubkey) ORDER BY rx_time DESC) = 1""",
        [region, frm, to],
    ).fetchall()
    return {
        key: Advert(Node(key, ROLE_NAMES.get(int(role), "unknown"), name, lat, lon), int(t))
        for key, role, name, lat, lon, t in rows
    }
