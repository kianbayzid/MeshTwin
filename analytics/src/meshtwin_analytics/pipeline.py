"""The topology job end to end: load -> resolve -> aggregate -> graph -> resilience."""

import time
from collections import Counter
from dataclasses import dataclass, field
from datetime import UTC, datetime

import networkx as nx

from . import load
from .aggregate import Evidence, accumulate, build_graph
from .model import Node
from .resilience import Resilience, analyse
from .resolve import HopResolver
from .storage import connect

HOUR_US = 3_600_000_000
DAY_US = 24 * HOUR_US

# Repeaters send FLOOD adverts only every 47 h by default (zero-hop adverts reach one
# hop; companions advert only when the user asks). A 24 h link window would miss most
# positions and roles, so the node registry looks further back than the link window.
DEFAULT_ADVERT_LOOKBACK_US = 7 * DAY_US


# An advert-less observer counts as a relay only on self-evidence: in packets IT heard,
# its own prefix sits before the last hop (it relayed, a neighbour relayed again, it heard
# the echo; protocol.md section 2). Any hop hash elsewhere can be someone else's key; this
# needs a same-prefix node relaying a packet the observer then hears back. With 1-byte
# hashes that can still happen by chance, so we want this many receptions, or one with a
# hop of 2+ bytes (4+ hex chars), where a chance match is very unlikely.
SELF_ECHO_MIN_RECEPTIONS = 3
SELF_ECHO_STRONG_HEX = 4


def self_echo_relays(receptions: list[load.Reception]) -> set[str]:
    """Observers with enough self-echo evidence to be treated as relays."""
    counts: Counter[str] = Counter()
    strong: set[str] = set()
    for r in receptions:
        own = [h for h in r.hops[:-1] if r.observer.startswith(h)]
        if own:
            counts[r.observer] += 1
            if any(len(h) >= SELF_ECHO_STRONG_HEX for h in own):
                strong.add(r.observer)
    return strong | {o for o, n in counts.items() if n >= SELF_ECHO_MIN_RECEPTIONS}


def infer_graph(
    adverts: dict[str, load.Advert], receptions: list[load.Reception], min_confidence: float
) -> tuple["nx.Graph[str]", Evidence, HopResolver]:
    """Registry -> hop resolution -> link graph. Pure (no I/O), so tests can feed it rows."""
    nodes = {k: a.node for k, a in adverts.items()}
    last_heard = {k: a.rx_us for k, a in adverts.items()}
    observers: set[str] = set()
    for r in receptions:
        observers.add(r.observer)
        last_heard[r.observer] = max(last_heard.get(r.observer, 0), r.rx_us)
        if r.origin is not None:
            nodes.setdefault(r.origin, Node(r.origin))
    # Observers are known nodes even without an advert (role unknown), registered before
    # any hop is resolved. An advert always wins over self-echo inference.
    for o in observers:
        nodes.setdefault(o, Node(o))
    inferred_relays = frozenset(self_echo_relays(receptions) - adverts.keys())
    resolver = HopResolver(nodes, relay_keys=inferred_relays)
    evidence = accumulate(receptions, resolver)
    graph = build_graph(
        resolver.nodes, evidence, observers, last_heard, min_confidence, inferred_relays
    )
    return graph, evidence, resolver


@dataclass
class TopologyRun:
    region: str
    from_us: int
    to_us: int
    generated_at: datetime
    counts: load.Counts
    nodes: dict[str, Node]
    evidence: Evidence
    graph: "nx.Graph[str]"
    resilience: Resilience
    cache_hits: int
    cache_misses: int
    timings_s: dict[str, float] = field(default_factory=dict)

    @property
    def cache_hit_rate(self) -> float:
        total = self.cache_hits + self.cache_misses
        return self.cache_hits / total if total else 0.0


def default_window(root: str, region: str, window_us: int) -> tuple[int, int]:
    """[end - window, end) where end = max rx_time rounded up to the next second.

    Ending at the data (not at "now") makes the fixtures work and replays reproducible.
    """
    con = connect(root)
    last = load.max_rx_us(con, root, region)
    if last is None:
        raise ValueError(f"no receptions for region {region} under {root}")
    to = (last // 1_000_000 + 1) * 1_000_000
    return to - window_us, to


def run_topology(
    root: str,
    region: str,
    from_us: int,
    to_us: int,
    min_confidence: float = 0.5,
    advert_lookback_us: int = DEFAULT_ADVERT_LOOKBACK_US,
) -> TopologyRun:
    timings: dict[str, float] = {}
    t0 = time.perf_counter()
    con = connect(root)
    counts = load.counts(con, root, region, from_us, to_us)
    receptions = load.flood_receptions(con, root, region, from_us, to_us)
    adverts = load.latest_adverts(con, root, region, to_us - advert_lookback_us, to_us)
    timings["load"] = time.perf_counter() - t0

    t0 = time.perf_counter()
    graph, evidence, resolver = infer_graph(adverts, receptions, min_confidence)
    # Resolution (new chains only, memo hits are free) vs everything else in this step.
    timings["resolution"] = resolver.solve_seconds
    timings["aggregation"] = time.perf_counter() - t0 - resolver.solve_seconds

    t0 = time.perf_counter()
    res = analyse(graph)
    timings["resilience"] = time.perf_counter() - t0

    return TopologyRun(
        region=region,
        from_us=from_us,
        to_us=to_us,
        generated_at=datetime.now(UTC).replace(microsecond=0),
        counts=counts,
        nodes=resolver.nodes,
        evidence=evidence,
        graph=graph,
        resilience=res,
        cache_hits=resolver.hits,
        cache_misses=resolver.misses,
        timings_s=timings,
    )
