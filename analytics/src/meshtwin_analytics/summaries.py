"""JSON summaries for the API (docs/api-contract.md) and internal Parquet tables.

Privacy rules applied here, and only here, for everything that leaves analytics:
ids are 8-hex key prefixes, locations go through geo.snap, names only for
infrastructure (repeaters, room servers). The Parquet tables are internal: they keep
full keys and raw coordinates and must stay in the private bucket.
"""

from datetime import UTC, datetime
from typing import Any

import pyarrow as pa

from .aggregate import LinkEvidence, percentile
from .geo import snap
from .model import PLACEHOLDER_PREFIX, Node
from .pipeline import TopologyRun

NAMED_ROLES = frozenset({"repeater", "room"})
SOURCE = "topology-job"


def rfc3339(us: int) -> str:
    return datetime.fromtimestamp(us // 1_000_000, UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def public_id(key: str) -> str:
    """8-hex prefix; a placeholder keeps its hop hex (it is all we know)."""
    return key.removeprefix(PLACEHOLDER_PREFIX) if key.startswith(PLACEHOLDER_PREFIX) else key[:8]


def _public_node(n: Node) -> dict[str, Any]:
    return {
        "id": public_id(n.key),
        "role": n.role,
        "name": n.name if n.role in NAMED_ROLES else None,
        "location": snap(n.lat, n.lon),
    }


def meta(run: TopologyRun, source: str = SOURCE) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "region": run.region,
        "generated_at": run.generated_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "window": {"from": rfc3339(run.from_us), "to": rfc3339(run.to_us)},
        "source": source,
    }


def _snr(le: LinkEvidence) -> dict[str, Any] | None:
    if not le.snr:
        return None
    return {
        "median": round(percentile(le.snr, 50), 1),
        "p10": round(percentile(le.snr, 10), 1),
        "p90": round(percentile(le.snr, 90), 1),
        "samples": len(le.snr),
    }


def _rssi(le: LinkEvidence) -> dict[str, Any] | None:
    if not le.rssi:
        return None
    return {"median": round(percentile(le.rssi, 50), 1), "samples": len(le.rssi)}


def _sorted_edges(run: TopologyRun) -> list[tuple[str, str, LinkEvidence]]:
    edges = []
    for a, b in run.graph.edges:
        le: LinkEvidence = run.graph.edges[a, b]["evidence"]
        edges.append((min(a, b), max(a, b), le))
    return sorted(edges, key=lambda e: (e[0], e[1]))


def topology_json(run: TopologyRun) -> dict[str, Any]:
    g, ev = run.graph, run.evidence
    nodes = []
    for key in sorted(g.nodes):
        attrs = g.nodes[key]
        last = attrs["last_heard_us"]
        nodes.append(
            _public_node(attrs["node"])
            | {
                "observer": attrs["observer"],
                "degree": g.degree(key),
                "last_heard": rfc3339(last) if last is not None else None,
            }
        )
    links = [
        {
            "a": public_id(a),
            "b": public_id(b),
            # Expected number of receptions that crossed this link, rounded.
            "observations": round(le.observations),
            "confidence": round(le.confidence, 3),
            "snr_db": _snr(le),
            "rssi_dbm": _rssi(le),
            "first_seen": rfc3339(le.first_us) if le.first_us is not None else None,
            "last_seen": rfc3339(le.last_us) if le.last_us is not None else None,
        }
        for a, b, le in _sorted_edges(run)
    ]
    return {
        "meta": meta(run),
        "stats": {
            "receptions": run.counts.receptions,
            "flood_receptions": run.counts.flood_receptions,
            "observers": run.counts.observers,
            "nodes": g.number_of_nodes(),
            "links": g.number_of_edges(),
            "ambiguous_hop_share": round(ev.ambiguous_hops / ev.hop_entries, 3)
            if ev.hop_entries
            else 0.0,
            "unresolved_hops": ev.unresolved_hops,
        },
        "nodes": nodes,
        "links": links,
    }


def resilience_json(run: TopologyRun) -> dict[str, Any]:
    res = run.resilience
    return {
        "meta": meta(run),
        "baseline": {
            "nodes": res.nodes,
            "reachable_pairs": res.reachable_pairs,
            "reachable_share": round(res.reachable_share, 3),
        },
        "single_points_of_failure": [
            _public_node(run.nodes[s.node])
            | {
                "articulation_point": s.articulation_point,
                "reachable_pairs_lost": s.reachable_pairs_lost,
                "reachable_share_after": round(s.reachable_share_after, 3),
                "nodes_cut_off": s.nodes_cut_off,
                "confidence": round(s.confidence, 3),
            }
            for s in res.spofs
        ],
        "bridges": [
            {
                "a": public_id(b.a),
                "b": public_id(b.b),
                "reachable_pairs_lost": b.reachable_pairs_lost,
                "confidence": round(b.confidence, 3),
            }
            for b in res.bridges
        ],
    }


def _ts(us: int | None) -> datetime | None:
    return None if us is None else datetime.fromtimestamp(us / 1_000_000, UTC)


def nodes_table(run: TopologyRun) -> pa.Table:
    """Internal: full keys and raw coordinates. Private bucket only."""
    g = run.graph
    rows = []
    for key in sorted(g.nodes):
        a = g.nodes[key]
        n: Node = a["node"]
        rows.append(
            {
                "pubkey": key,
                "id": public_id(key),
                "role": n.role,
                "name": n.name,
                "lat": n.lat,
                "lon": n.lon,
                "observer": a["observer"],
                "relay": a["relay"],
                "degree": g.degree(key),
                "last_heard": _ts(a["last_heard_us"]),
            }
        )
    return pa.Table.from_pylist(rows, schema=_NODES_SCHEMA)


def links_table(run: TopologyRun) -> pa.Table:
    rows = []
    for a, b, le in _sorted_edges(run):
        snr, rssi = _snr(le) or {}, _rssi(le) or {}
        rows.append(
            {
                "a": a,
                "b": b,
                "observations": le.observations,
                "confidence": le.confidence,
                "snr_median": snr.get("median"),
                "snr_p10": snr.get("p10"),
                "snr_p90": snr.get("p90"),
                "snr_samples": len(le.snr),
                "rssi_median": rssi.get("median"),
                "rssi_samples": len(le.rssi),
                "first_seen": _ts(le.first_us),
                "last_seen": _ts(le.last_us),
            }
        )
    return pa.Table.from_pylist(rows, schema=_LINKS_SCHEMA)


def resilience_table(run: TopologyRun) -> pa.Table:
    """One row per finding: kind 'spof' (node in a) or 'bridge' (edge a-b)."""
    res = run.resilience
    rows: list[dict[str, Any]] = [
        {
            "kind": "spof",
            "a": s.node,
            "b": None,
            "articulation_point": s.articulation_point,
            "reachable_pairs_lost": s.reachable_pairs_lost,
            "reachable_share_after": s.reachable_share_after,
            "nodes_cut_off": s.nodes_cut_off,
            "confidence": s.confidence,
        }
        for s in res.spofs
    ]
    rows += [
        {
            "kind": "bridge",
            "a": b.a,
            "b": b.b,
            "articulation_point": None,
            "reachable_pairs_lost": b.reachable_pairs_lost,
            "reachable_share_after": None,
            "nodes_cut_off": None,
            "confidence": b.confidence,
        }
        for b in res.bridges
    ]
    return pa.Table.from_pylist(rows, schema=_RESILIENCE_SCHEMA)


_TS = pa.timestamp("us", tz="UTC")


def _schema(fields: list[tuple[str, pa.DataType]]) -> pa.Schema:
    return pa.schema(fields)


_NODES_SCHEMA = _schema(
    [
        ("pubkey", pa.string()),
        ("id", pa.string()),
        ("role", pa.string()),
        ("name", pa.string()),
        ("lat", pa.float64()),
        ("lon", pa.float64()),
        ("observer", pa.bool_()),
        ("relay", pa.bool_()),
        ("degree", pa.int32()),
        ("last_heard", _TS),
    ]
)
_LINKS_SCHEMA = _schema(
    [
        ("a", pa.string()),
        ("b", pa.string()),
        ("observations", pa.float64()),
        ("confidence", pa.float64()),
        ("snr_median", pa.float64()),
        ("snr_p10", pa.float64()),
        ("snr_p90", pa.float64()),
        ("snr_samples", pa.int32()),
        ("rssi_median", pa.float64()),
        ("rssi_samples", pa.int32()),
        ("first_seen", _TS),
        ("last_seen", _TS),
    ]
)
_RESILIENCE_SCHEMA = _schema(
    [
        ("kind", pa.string()),
        ("a", pa.string()),
        ("b", pa.string()),
        ("articulation_point", pa.bool_()),
        ("reachable_pairs_lost", pa.int64()),
        ("reachable_share_after", pa.float64()),
        ("nodes_cut_off", pa.int32()),
        ("confidence", pa.float64()),
    ]
)
