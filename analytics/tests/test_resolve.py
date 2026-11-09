"""Hop resolution on tiny hand-built chains, where the right answer is obvious."""

import itertools

import numpy as np
import pytest

from meshtwin_analytics.geo import log_link_prior, snap
from meshtwin_analytics.model import Node
from meshtwin_analytics.resolve import HopResolver, forward_backward

# Keys are 64 hex chars; only the prefix matters for matching.
OBS = "00" + "0" * 62
NEAR = "AB11" + "1" * 60
FAR = "AB22" + "2" * 60
RPT_C = "CC" + "3" * 62
COMP = "DD" + "4" * 62
LAT, LON = 47.60, -122.30
KM_LAT = 1 / 111.0  # degrees of latitude per km


def resolver(*nodes: Node) -> HopResolver:
    return HopResolver({n.key: n for n in nodes})


def observer() -> Node:
    return Node(OBS, "repeater", "OBS", LAT, LON)


def test_unambiguous_chain_has_posterior_one() -> None:
    r = resolver(observer(), Node(RPT_C, "repeater", None, LAT + 3 * KM_LAT, LON))
    res = r.resolve(("CC",), None, OBS)
    assert res.hop_probs[RPT_C] == pytest.approx(1.0)
    assert res.pair_probs == {(OBS, RPT_C): pytest.approx(1.0)}
    assert res.last_hop == {RPT_C: pytest.approx(1.0)}
    assert res.ambiguous_hops == 0


def test_near_candidate_beats_far_one() -> None:
    near = Node(NEAR, "repeater", None, LAT + 2 * KM_LAT, LON)
    far = Node(FAR, "repeater", None, LAT + 80 * KM_LAT, LON)
    res = resolver(observer(), near, far).resolve(("AB",), None, OBS)
    assert res.hop_probs[NEAR] > 0.999
    assert res.hop_probs.get(FAR, 0.0) < 1e-3
    assert (OBS, FAR) not in res.pair_probs  # below MIN_PAIR_PROB, dropped
    assert res.ambiguous_hops == 1


def test_context_from_both_neighbours_is_used() -> None:
    # Hop AB sits between C and the observer. FAR is 38 km from C and 40 km from the
    # observer; NEAR is 1 km from both. Joint resolution picks NEAR.
    c = Node(RPT_C, "repeater", None, LAT + 2 * KM_LAT, LON)
    near = Node(NEAR, "repeater", None, LAT + 1 * KM_LAT, LON)
    far = Node(FAR, "repeater", None, LAT + 40 * KM_LAT, LON)
    res = resolver(observer(), c, near, far).resolve(("CC", "AB"), None, OBS)
    assert res.hop_probs[NEAR] > 0.999


def test_self_echo_gives_no_self_link() -> None:
    # Observer relayed the packet itself, then heard C relay it back: path [OBS, C].
    c = Node(RPT_C, "repeater", None, LAT + 3 * KM_LAT, LON)
    res = resolver(observer(), c).resolve(("00", "CC"), None, OBS)
    assert set(res.pair_probs) == {(OBS, RPT_C)}
    # Observer logging its own relayed copy: path [OBS] heard by OBS. No link at all.
    res = resolver(observer(), c).resolve(("00",), None, OBS)
    assert res.pair_probs == {}
    assert res.last_hop == {}


def test_unknown_hop_becomes_placeholder() -> None:
    r = resolver(observer())
    res = r.resolve(("EE",), None, OBS)
    assert r.nodes["unknown:EE"].role == "unknown"
    assert res.unresolved_hops == 1
    assert res.pair_probs == {(OBS, "unknown:EE"): pytest.approx(1.0)}


def test_candidate_tiers_prefer_relay_roles() -> None:
    comp = Node(COMP, "companion")
    rpt = Node("DD" + "5" * 62, "repeater")
    assert [k for k, _ in resolver(comp, rpt).candidates("DD")] == [rpt.key]
    # Only a companion matches: it becomes the (fallback) candidate, not a placeholder.
    assert [k for k, _ in resolver(comp).candidates("DD")] == [COMP]


def test_origin_is_a_fixed_endpoint() -> None:
    origin = Node(COMP, "companion", None, LAT + 1 * KM_LAT, LON)
    c = Node(RPT_C, "repeater", None, LAT + 2 * KM_LAT, LON)
    res = resolver(observer(), origin, c).resolve(("CC",), COMP, OBS)
    assert set(res.pair_probs) == {(RPT_C, COMP), (OBS, RPT_C)}
    assert COMP not in res.hop_probs  # an endpoint is not a relay hop


def test_memo_counts_hits() -> None:
    r = resolver(observer(), Node(RPT_C, "repeater"))
    for _ in range(3):
        r.resolve(("CC",), None, OBS)
    assert (r.misses, r.hits) == (1, 2)


def test_forward_backward_matches_brute_force() -> None:
    rng = np.random.default_rng(0)
    sizes = [1, 3, 2, 3, 1]
    unary = [rng.normal(size=k) for k in sizes]
    pairwise = [rng.normal(size=(a, b)) for a, b in itertools.pairwise(sizes)]
    nodes, pairs = forward_backward(unary, pairwise)

    weights: dict[tuple[int, ...], float] = {}
    for states in itertools.product(*(range(k) for k in sizes)):
        score = sum(unary[i][s] for i, s in enumerate(states))
        score += sum(pairwise[i][states[i], states[i + 1]] for i in range(len(sizes) - 1))
        weights[states] = float(np.exp(score))
    z = sum(weights.values())
    for i, k in enumerate(sizes):
        for s in range(k):
            want = sum(w for st, w in weights.items() if st[i] == s) / z
            assert nodes[i][s] == pytest.approx(want)
    for i in range(len(sizes) - 1):
        for a in range(sizes[i]):
            for b in range(sizes[i + 1]):
                want = sum(w for st, w in weights.items() if st[i] == a and st[i + 1] == b) / z
                assert pairs[i][a, b] == pytest.approx(want)


def test_link_prior_shape() -> None:
    assert np.exp(log_link_prior(15.0)) == pytest.approx(0.5)
    assert np.exp(log_link_prior(2.0)) > 0.95
    assert np.exp(log_link_prior(80.0)) < 1e-6
    assert np.isfinite(log_link_prior(5000.0))  # no overflow
    assert np.exp(log_link_prior(None)) == pytest.approx(0.5)


def test_snap() -> None:
    assert snap(47.545565799, -122.056757) == {"lat": 47.55, "lon": -122.06, "grid_deg": 0.01}
    assert snap(None, -122.0) is None
