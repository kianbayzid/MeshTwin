"""Advert-less observers: relay status only from self-echo evidence, never from a collision."""

from meshtwin_analytics.load import Advert, Reception
from meshtwin_analytics.model import Node
from meshtwin_analytics.pipeline import SELF_ECHO_MIN_RECEPTIONS, infer_graph, self_echo_relays

OBS = "35" + "A" * 62  # advert-less observer, 1-byte prefix 35
RPT = "35" + "B" * 62  # a real repeater with the same 1-byte prefix
OTHER = "34" + "C" * 62  # another repeater
LAT, LON = 47.60, -122.30


def _advert(key: str, lat: float) -> Advert:
    return Advert(Node(key, "repeater", None, lat, LON), 0)


def _rx(observer: str, hops: tuple[str, ...]) -> Reception:
    return Reception(1, observer, hops, None, 5.0, -90.0)


def test_colliding_companion_observer_stays_non_relay() -> None:
    adverts = {RPT: _advert(RPT, LAT + 0.01), OTHER: _advert(OTHER, LAT + 0.02)}
    # Hop 35 shows up everywhere, but only as the last hop into the observer (RPT relaying
    # to it) or in another observer's packets: no self-echo.
    receptions = [_rx(OBS, ("34", "35")) for _ in range(10)]
    receptions += [_rx(OTHER, ("35",)) for _ in range(10)]
    graph, _, resolver = infer_graph(adverts, receptions, 0.5)

    assert self_echo_relays(receptions) == set()
    assert resolver.nodes[OBS].role == "unknown"
    assert [k for k, _ in resolver.candidates("35")] == [RPT]  # observer is not a candidate
    assert graph.nodes[OBS]["relay"] is False
    assert graph.nodes[RPT]["relay"] is True


def test_self_echo_makes_advertless_observer_a_relay() -> None:
    adverts = {OTHER: _advert(OTHER, LAT + 0.02)}
    echo = [_rx(OBS, ("35", "34")) for _ in range(SELF_ECHO_MIN_RECEPTIONS)]
    assert self_echo_relays(echo[:-1]) == set()  # below the threshold
    graph, _, resolver = infer_graph(adverts, echo, 0.5)

    assert self_echo_relays(echo) == {OBS}
    assert resolver.nodes[OBS].role == "unknown"  # role is not invented, only relay status
    assert [k for k, _ in resolver.candidates("35")] == [OBS]
    assert graph.nodes[OBS]["relay"] is True


def test_one_wide_hash_echo_is_enough() -> None:
    assert self_echo_relays([_rx(OBS, (OBS[:4], "34CC"))]) == {OBS}


def test_advert_wins_over_inference() -> None:
    companion = Advert(Node(OBS, "companion"), 0)
    echo = [_rx(OBS, ("35", "34")) for _ in range(5)]
    _, _, resolver = infer_graph({OBS: companion}, echo, 0.5)
    assert resolver.nodes[OBS].role == "companion"
    assert OBS not in resolver.relay_keys  # not promoted to the relay candidate tier
