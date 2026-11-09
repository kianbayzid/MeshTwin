"""Turn per-reception chain posteriors into per-link evidence and a networkx graph."""

from collections.abc import Iterable
from dataclasses import dataclass, field

import networkx as nx
import numpy as np

from .load import Reception
from .model import RELAY_ROLES, Node
from .resolve import HopResolver

# A node whose role is not a relay role counts as a relay in resilience only if some
# reception resolved it as a hop with at least this posterior. Simple and explainable:
# "we have at least one good reason to believe it forwarded a packet".
OBSERVED_RELAY_MIN_POSTERIOR = 0.5

# SNR/RSSI samples are taken from the last hop into the observer only when that hop's
# posterior is at least this. Counting (not weighting) keeps medians/percentiles plain.
LAST_HOP_MIN_POSTERIOR = 0.5


@dataclass
class LinkEvidence:
    """Everything we accumulate for one undirected node pair."""

    observations: float = 0.0  # sum over receptions of P(pair in that chain)
    confidence: float = 0.0  # max over receptions of P(pair in that chain); see README
    first_us: int | None = None
    last_us: int | None = None
    snr: list[float] = field(default_factory=list)
    rssi: list[float] = field(default_factory=list)

    def add(self, p: float, rx_us: int) -> None:
        self.observations += p
        self.confidence = max(self.confidence, p)
        self.first_us = rx_us if self.first_us is None else min(self.first_us, rx_us)
        self.last_us = rx_us if self.last_us is None else max(self.last_us, rx_us)


@dataclass
class Evidence:
    links: dict[tuple[str, str], LinkEvidence] = field(default_factory=dict)
    hop_probs: dict[str, float] = field(default_factory=dict)  # node -> best hop posterior
    hop_entries: int = 0
    ambiguous_hops: int = 0
    unresolved_hops: int = 0


def accumulate(receptions: Iterable[Reception], resolver: HopResolver) -> Evidence:
    """Resolve every reception's chain and add its pair posteriors to the link totals."""
    ev = Evidence()
    for r in receptions:
        res = resolver.resolve(r.hops, r.origin, r.observer)
        ev.hop_entries += len(r.hops)
        ev.ambiguous_hops += res.ambiguous_hops
        ev.unresolved_hops += res.unresolved_hops
        for node, p in res.hop_probs.items():
            ev.hop_probs[node] = max(ev.hop_probs.get(node, 0.0), p)
        for pair, p in res.pair_probs.items():
            ev.links.setdefault(pair, LinkEvidence()).add(p, r.rx_us)
        for sender, p in res.last_hop.items():
            if p < LAST_HOP_MIN_POSTERIOR:
                continue
            pair = (sender, r.observer) if sender < r.observer else (r.observer, sender)
            link = ev.links[pair]
            if r.snr_db is not None:
                link.snr.append(r.snr_db)
            if r.rssi_dbm is not None:
                link.rssi.append(r.rssi_dbm)
    return ev


def is_relay(node: Node, hop_prob: float) -> bool:
    """Relay in the resilience sense: relay role, placeholder (it was seen in a flood
    path, so it forwarded), or observed relaying with a high posterior."""
    return (
        node.role in RELAY_ROLES or node.is_placeholder or hop_prob >= OBSERVED_RELAY_MIN_POSTERIOR
    )


def percentile(xs: list[float], q: float) -> float:
    return float(np.percentile(np.asarray(xs, dtype=np.float64), q))


def build_graph(
    nodes: dict[str, Node],
    ev: Evidence,
    observers: set[str],
    last_heard_us: dict[str, int],
    min_confidence: float,
    inferred_relays: frozenset[str] = frozenset(),
) -> "nx.Graph[str]":
    """Graph of links with confidence >= min_confidence, plus every observer.

    Nodes with no surviving link are dropped (an isolated node would only add
    unreachable pairs to resilience without telling us anything about the mesh).
    """
    g: nx.Graph[str] = nx.Graph()
    for (a, b), le in ev.links.items():
        if le.confidence >= min_confidence:
            g.add_edge(a, b, evidence=le, confidence=le.confidence)
    g.add_nodes_from(observers)
    for key in g.nodes:
        n = nodes[key]
        heard = [last_heard_us[key]] if key in last_heard_us else []
        for m in g.neighbors(key):
            last = g.edges[key, m]["evidence"].last_us
            if last is not None:
                heard.append(last)
        g.nodes[key].update(
            node=n,
            observer=key in observers,
            relay=key in inferred_relays or is_relay(n, ev.hop_probs.get(key, 0.0)),
            last_heard_us=max(heard) if heard else None,
        )
    return g
