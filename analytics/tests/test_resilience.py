"""Relay-aware reachability on hand-built graphs."""

import pytest

from meshtwin_analytics.resilience import analyse, reach_sets, reachable_pairs, relay_graph


def test_companion_in_the_middle_connects_nothing() -> None:
    # C1 - R1 - C - R2 - C2: C hears both repeaters but does not forward.
    g = relay_graph(
        {"R1": True, "R2": True, "C": False, "C1": False, "C2": False},
        [("C1", "R1", {}), ("R1", "C", {}), ("C", "R2", {}), ("R2", "C2", {})],
    )
    reach = reach_sets(g)
    assert "C2" not in reach["C1"]
    assert "R2" not in reach["R1"]
    assert reach["C"] == {"R1", "R2", "C1", "C2"}  # C itself can talk to both sides
    assert reachable_pairs(reach) == 6


def test_star_of_companions_makes_the_repeater_a_spof() -> None:
    companions = [f"C{i}" for i in range(4)]
    g = relay_graph(
        {"R": True} | dict.fromkeys(companions, False),
        [("R", c, {"confidence": 0.9 - i / 10}) for i, c in enumerate(companions)],
    )
    res = analyse(g)
    assert res.reachable_pairs == 10  # all 5 nodes reach each other
    [spof] = res.spofs
    assert spof.node == "R"
    assert spof.reachable_pairs_lost == 6  # the 4-choose-2 companion pairs
    assert spof.reachable_share_after == 0.0
    assert spof.nodes_cut_off == 4
    assert spof.confidence == pytest.approx(0.6)  # weakest link it depends on
    assert not spof.articulation_point  # alone in the relay subgraph, so not an AP
    assert res.bridges == []


def test_bridges_and_articulation_points() -> None:
    # C1 - R1 - R2 - R3 - C3, plus a redundant triangle R3 - R4 - R5 - R3.
    g = relay_graph(
        {"R1": True, "R2": True, "R3": True, "R4": True, "R5": True, "C1": False, "C3": False},
        [
            ("C1", "R1", {}),
            ("R1", "R2", {"confidence": 0.8}),
            ("R2", "R3", {}),
            ("R3", "C3", {}),
            ("R3", "R4", {}),
            ("R4", "R5", {}),
            ("R5", "R3", {}),
        ],
    )
    res = analyse(g)
    assert res.reachable_pairs == 21  # one relay component touches everyone: 7 choose 2
    assert {(b.a, b.b) for b in res.bridges} == {("R1", "R2"), ("R2", "R3")}
    r1r2 = next(b for b in res.bridges if (b.a, b.b) == ("R1", "R2"))
    # Splits into {R1, C1} and {R2..R5, C3}: 1 + 10 pairs remain.
    assert r1r2.reachable_pairs_lost == 21 - 11
    assert r1r2.confidence == pytest.approx(0.8)
    # R1 is a leaf of the relay subgraph (not an AP) but still a SPOF: it alone serves C1.
    assert {s.node for s in res.spofs} == {"R1", "R2", "R3"}
    assert {s.node for s in res.spofs if s.articulation_point} == {"R2", "R3"}
    assert "R4" not in {s.node for s in res.spofs}  # triangle member: redundant


def test_direct_neighbours_stay_reachable_without_relays() -> None:
    g = relay_graph({"A": False, "B": False}, [("A", "B", {})])
    assert reachable_pairs(reach_sets(g)) == 1
    assert analyse(g).spofs == []
