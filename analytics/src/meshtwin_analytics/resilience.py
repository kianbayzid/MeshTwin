"""Relay-aware reachability and single points of failure.

Only relays forward floods, so two nodes can talk iff they are radio neighbours, or
both touch the same connected component of the relay-only subgraph ("touch" = be a
member of it or a neighbour of a member). A companion between two components connects
nothing.

Cost: one reachability pass is O(V + E) to find relay components plus the set unions
(bounded by V per node). Removing each of R relays in turn is O(R x (V + E)) passes,
fine for the hundreds of nodes a region has.
"""

from collections.abc import Iterator
from dataclasses import dataclass
from typing import Any, cast

import networkx as nx


def _relays(g: "nx.Graph[str]") -> set[str]:
    return {n for n, r in g.nodes(data="relay") if r}


def reach_sets(g: "nx.Graph[str]") -> dict[str, set[str]]:
    """node -> every other node it can reach (relay-aware)."""
    relays = _relays(g)
    comp_of: dict[str, int] = {}
    for i, comp in enumerate(nx.connected_components(g.subgraph(relays))):
        for r in comp:
            comp_of[r] = i
    # touches[n] = relay components n can hand a packet to (or receive one from).
    touches: dict[str, set[int]] = {n: set() for n in g}
    for n in g:
        if n in comp_of:
            touches[n].add(comp_of[n])
        for m in g.neighbors(n):
            if m in comp_of:
                touches[n].add(comp_of[m])
    members: dict[int, set[str]] = {}
    for n, cs in touches.items():
        for c in cs:
            members.setdefault(c, set()).add(n)
    reach: dict[str, set[str]] = {}
    for n in g:
        s = set(g.neighbors(n))
        for c in touches[n]:
            s |= members[c]
        s.discard(n)
        reach[n] = s
    return reach


def reachable_pairs(reach: dict[str, set[str]]) -> int:
    # Reachability is symmetric, so every pair is counted twice.
    return sum(len(s) for s in reach.values()) // 2


def _share(pairs: int, n: int) -> float:
    total = n * (n - 1) // 2
    return pairs / total if total else 0.0


def main_group(g: "nx.Graph[str]") -> set[str]:
    """Nodes touching the largest relay component: 'connected to the backbone'."""
    relays = _relays(g)
    comps = list(nx.connected_components(g.subgraph(relays)))
    if not comps:
        return set()
    biggest = max(comps, key=lambda c: (len(c), sorted(c)))
    return set(biggest) | {m for r in biggest for m in g.neighbors(r)}


def _min_confidence(g: "nx.Graph[str]", edges: list[tuple[str, str]]) -> float:
    confs = [float(g.edges[e].get("confidence", 1.0)) for e in edges]
    return min(confs) if confs else 1.0


@dataclass(frozen=True)
class Spof:
    node: str
    articulation_point: bool
    reachable_pairs_lost: int
    reachable_share_after: float
    nodes_cut_off: int
    confidence: float


@dataclass(frozen=True)
class Bridge:
    a: str
    b: str
    reachable_pairs_lost: int
    confidence: float


@dataclass(frozen=True)
class Resilience:
    nodes: int
    reachable_pairs: int
    reachable_share: float
    spofs: list[Spof]  # every relay whose removal loses pairs, most damaging first
    bridges: list[Bridge]


def analyse(g: "nx.Graph[str]") -> Resilience:
    """Baseline reachability, then remove each relay (and each relay-relay bridge) in turn.

    Edge attribute `confidence` (default 1.0) feeds the finding confidence: the lowest
    confidence among the links the finding depends on (the removed node's links, or the
    bridge itself). A finding is only as trustworthy as its weakest link.
    """
    reach = reach_sets(g)
    base = reachable_pairs(reach)
    main = main_group(g)
    relays = _relays(g)
    aps = set(nx.articulation_points(g.subgraph(relays)))

    spofs = []
    for r in sorted(relays):
        h = g.copy()
        h.remove_node(r)
        after = reachable_pairs(reach_sets(h))
        # Pairs involving r itself are always lost; only count damage to the others.
        lost = base - len(reach[r]) - after
        if lost <= 0:
            continue
        cut = len((main - {r}) - main_group(h))
        spofs.append(
            Spof(
                node=r,
                articulation_point=r in aps,
                reachable_pairs_lost=lost,
                reachable_share_after=_share(after, h.number_of_nodes()),
                nodes_cut_off=cut,
                confidence=_min_confidence(g, [(r, m) for m in g.neighbors(r)]),
            )
        )
    spofs.sort(key=lambda s: (-s.reachable_pairs_lost, s.node))

    bridges = []
    # cast: types-networkx annotates bridges() as yielding nodes; it yields edges.
    for a, b in cast("Iterator[tuple[str, str]]", nx.bridges(g.subgraph(relays))):
        h = g.copy()
        h.remove_edge(a, b)
        lost = base - reachable_pairs(reach_sets(h))
        a, b = min(a, b), max(a, b)
        bridges.append(Bridge(a, b, lost, _min_confidence(g, [(a, b)])))
    bridges.sort(key=lambda x: (-x.reachable_pairs_lost, x.a, x.b))

    return Resilience(
        nodes=g.number_of_nodes(),
        reachable_pairs=base,
        reachable_share=_share(base, g.number_of_nodes()),
        spofs=spofs,
        bridges=bridges,
    )


def relay_graph(
    nodes: dict[str, bool], links: list[tuple[str, str, dict[str, Any]]]
) -> "nx.Graph[str]":
    """Build a graph for `analyse` from node -> is_relay and (a, b, attrs) links."""
    g: nx.Graph[str] = nx.Graph()
    for n, relay in nodes.items():
        g.add_node(n, relay=relay)
    g.add_edges_from(links)
    return g
