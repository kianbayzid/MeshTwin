"""Score an inferred topology against the generator's ground truth (truth.json).

Limitation: truth.json lists every radio link, but only some can ever show up in flood
paths. A companion-companion link is never traversed by a flood (companions don't
relay, and the observers are repeaters), so plain recall is capped well below 1. We
also report recall over truth links touching at least one relay, and over relay-relay
links, which are the ones flood paths can actually reveal.
"""

import json
from pathlib import Path
from typing import Any

import networkx as nx

from .model import RELAY_ROLES
from .pipeline import TopologyRun
from .resilience import Resilience, analyse, relay_graph

TOP_K = 5


def _pr(found: set[Any], truth: set[Any]) -> dict[str, float | int]:
    tp = len(found & truth)
    return {
        "precision": round(tp / len(found), 3) if found else 1.0,
        "recall": round(tp / len(truth), 3) if truth else 1.0,
        "found": len(found),
        "truth": len(truth),
        "true_positives": tp,
    }


def _pair(a: str, b: str) -> frozenset[str]:
    return frozenset((a.upper(), b.upper()))


def truth_graph(truth: dict[str, Any]) -> "nx.Graph[str]":
    """The true network, with relays by role, ready for resilience.analyse."""
    relay = {n["pubkey"].upper(): n["role"] in RELAY_ROLES for n in truth["nodes"]}
    return relay_graph(relay, [(lk["a"].upper(), lk["b"].upper(), {}) for lk in truth["links"]])


def _spofs(res: Resilience) -> list[str]:
    return [s.node for s in res.spofs]


def evaluate(run: TopologyRun, truth: dict[str, Any]) -> dict[str, Any]:
    roles = {n["pubkey"].upper(): n["role"] for n in truth["nodes"]}
    truth_links = {_pair(lk["a"], lk["b"]) for lk in truth["links"]}

    def relays_in(p: frozenset[str]) -> int:
        return sum(roles.get(k) in RELAY_ROLES for k in p)

    inferred = {_pair(a, b) for a, b in run.graph.edges}
    touching_relay = {p for p in truth_links if relays_in(p) >= 1}
    relay_relay = {p for p in truth_links if relays_in(p) == 2}

    truth_res = analyse(truth_graph(truth))
    t_spofs, i_spofs = _spofs(truth_res), _spofs(run.resilience)
    top_t, top_i = set(t_spofs[:TOP_K]), set(i_spofs[:TOP_K])
    return {
        "links": _pr(inferred, truth_links),
        "recall_links_touching_relay": _pr(inferred & touching_relay, touching_relay)["recall"],
        "recall_relay_relay_links": _pr(inferred & relay_relay, relay_relay)["recall"],
        "spof": _pr(set(i_spofs), set(t_spofs)),
        "spof_top5_overlap": round(len(top_t & top_i) / max(1, min(TOP_K, len(top_t))), 3),
        "truth_reachable_share": round(truth_res.reachable_share, 3),
        "inferred_reachable_share": round(run.resilience.reachable_share, 3),
    }


def load_truth(path: str | Path) -> dict[str, Any]:
    with open(path, encoding="utf-8") as f:
        data: dict[str, Any] = json.load(f)
    return data
