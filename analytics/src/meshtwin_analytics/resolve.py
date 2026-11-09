"""Hop resolution: turn a flood path of hop hashes into node probabilities.

A flood reception gives a chain `[origin?] + path_hops + [observer]`. Each hop is a
1-3 byte key prefix that can match several nodes (docs/protocol.md section 2), so we
treat the chain as a small chain-structured probabilistic model (like an HMM):

- states at each position = candidate nodes (endpoints have exactly one state);
- each state has a unary log prior (candidate tier, below);
- consecutive states (a, b) score log p(distance(a, b)) (geo.log_link_prior).

Forward-backward gives exact posterior marginals for every hop and every consecutive
pair. Viterbi would give only the single best guess; marginals let every inferred link
carry a probability, which is what the confidence numbers are built from.
"""

import itertools
import math
import time
from dataclasses import dataclass, field

import numpy as np
from numpy.typing import NDArray

from .geo import haversine_km, log_link_prior
from .model import PLACEHOLDER_PREFIX, RELAY_ROLES, Node

FloatArray = NDArray[np.float64]

# Candidate tiers. A hop is first matched against known nodes with a relay role
# (model.RELAY_ROLES). Only if none match do we fall back to known nodes of any other
# role (owners can switch forwarding on), with this log prior. Because tiers are never
# mixed, the constant shifts every state at that position equally and cancels in the
# posterior; it is kept explicit so mixing tiers later is a one-line change.
RELAY_TIER_LOG_PRIOR = 0.0
FALLBACK_TIER_LOG_PRIOR = math.log(0.1)

# A node followed by itself (an observer hearing its own relayed copy, protocol.md
# section 2) is not a radio link: it scores log(1) = 0 (no penalty) and yields no link.
SELF_PAIR_LOG = 0.0

# Pair probabilities below this are dropped: they cannot pass any useful confidence
# threshold and would only fill the graph with noise edges.
MIN_PAIR_PROB = 1e-3


def _logsumexp(x: FloatArray, axis: int | None = None) -> FloatArray:
    """log(sum(exp(x))) computed stably by factoring out the maximum."""
    m = np.max(x, axis=axis, keepdims=True)
    out = m + np.log(np.sum(np.exp(x - m), axis=axis, keepdims=True))
    return np.squeeze(out, axis=axis) if axis is not None else out.reshape(())


def forward_backward(
    unary: list[FloatArray], pairwise: list[FloatArray]
) -> tuple[list[FloatArray], list[FloatArray]]:
    """Exact posterior marginals of a chain model, in log space.

    unary[i] has shape (k_i,) and pairwise[i] has shape (k_i, k_{i+1}); both are log
    scores. Returns (node marginals per position, pair marginals per adjacent positions),
    each summing to 1.

    alpha[i](s) = log-sum of all prefixes ending in s; beta[i](s) = of all suffixes after s.
    P(x_i = s) = exp(alpha + beta - logZ); P(x_i = a, x_i+1 = b) adds the a->b score.
    """
    n = len(unary)
    alpha = [unary[0]]
    for i in range(1, n):
        alpha.append(unary[i] + _logsumexp(alpha[i - 1][:, None] + pairwise[i - 1], axis=0))
    beta = [np.zeros_like(u) for u in unary]
    for i in range(n - 2, -1, -1):
        beta[i] = _logsumexp(pairwise[i] + (unary[i + 1] + beta[i + 1])[None, :], axis=1)
    log_z = float(_logsumexp(alpha[-1]))
    nodes = [np.exp(alpha[i] + beta[i] - log_z) for i in range(n)]
    pairs = [
        np.exp(alpha[i][:, None] + pairwise[i] + (unary[i + 1] + beta[i + 1])[None, :] - log_z)
        for i in range(n - 1)
    ]
    return nodes, pairs


@dataclass(frozen=True)
class ChainResult:
    """What one resolved chain contributes. Shared by every reception with the same chain."""

    pair_probs: dict[tuple[str, str], float]  # undirected (sorted) pair -> P(pair in chain)
    last_hop: dict[str, float]  # sender -> P(sender is the last hop into the observer)
    hop_probs: dict[str, float]  # node -> max P(node relayed at some hop position)
    ambiguous_hops: int  # hop entries with more than one candidate
    unresolved_hops: int  # hop entries with no candidate (became placeholders)


@dataclass
class HopResolver:
    """Resolves chains against a node registry, memoising identical chains.

    `nodes` maps key -> Node; placeholders and unseen endpoints are added to it as they
    are met, so after resolving it holds every node the graph can mention.
    """

    nodes: dict[str, Node]
    relay_keys: frozenset[str] = frozenset()  # advert-less observers with self-echo evidence
    hits: int = 0
    misses: int = 0
    solve_seconds: float = 0.0  # time spent solving new chains (for benchmarks)
    _candidates: dict[str, list[tuple[str, float]]] = field(default_factory=dict)
    _memo: dict[tuple[tuple[str, ...], str | None, str], ChainResult] = field(default_factory=dict)

    def candidates(self, hop: str) -> list[tuple[str, float]]:
        """(node key, log prior) for one hop: relay tier, else fallback tier, else placeholder."""
        if hop not in self._candidates:
            matches = [
                n for k, n in self.nodes.items() if k.startswith(hop) and not n.is_placeholder
            ]
            relays = [(n.key, RELAY_TIER_LOG_PRIOR) for n in matches if self._is_relay(n)]
            others = [(n.key, FALLBACK_TIER_LOG_PRIOR) for n in matches if not self._is_relay(n)]
            found = relays or others
            if not found:
                key = PLACEHOLDER_PREFIX + hop
                self.nodes.setdefault(key, Node(key))
                found = [(key, RELAY_TIER_LOG_PRIOR)]
            self._candidates[hop] = sorted(found)
        return self._candidates[hop]

    def _is_relay(self, n: Node) -> bool:
        return n.role in RELAY_ROLES or n.key in self.relay_keys

    def resolve(self, hops: tuple[str, ...], origin: str | None, observer: str) -> ChainResult:
        # Main performance lever: identical (path, origin, observer) chains repeat many
        # times (the same advert heard by the same observer via the same relays), so each
        # distinct chain is solved once.
        key = (hops, origin, observer)
        if key in self._memo:
            self.hits += 1
            return self._memo[key]
        self.misses += 1
        t0 = time.perf_counter()
        result = self._solve(hops, origin, observer)
        self.solve_seconds += time.perf_counter() - t0
        self._memo[key] = result
        return result

    def _endpoint(self, key: str) -> list[tuple[str, float]]:
        self.nodes.setdefault(key, Node(key))
        return [(key, 0.0)]

    def _log_pair(self, a: str, b: str) -> float:
        if a == b:
            return SELF_PAIR_LOG
        na, nb = self.nodes[a], self.nodes[b]
        if na.lat is None or na.lon is None or nb.lat is None or nb.lon is None:
            return log_link_prior(None)
        return log_link_prior(haversine_km(na.lat, na.lon, nb.lat, nb.lon))

    def _solve(self, hops: tuple[str, ...], origin: str | None, observer: str) -> ChainResult:
        hop_states = [self.candidates(h) for h in hops]
        states = (
            ([self._endpoint(origin)] if origin else []) + hop_states + [self._endpoint(observer)]
        )
        first_hop = 1 if origin else 0  # index of the first relay position

        unary = [np.array([lp for _, lp in s], dtype=np.float64) for s in states]
        pairwise = [
            np.array(
                [[self._log_pair(a, b) for b, _ in nxt] for a, _ in cur],
                dtype=np.float64,
            )
            for cur, nxt in itertools.pairwise(states)
        ]
        node_marg, pair_marg = forward_backward(unary, pairwise)

        pair_probs: dict[tuple[str, str], float] = {}
        last_hop: dict[str, float] = {}
        for i, m in enumerate(pair_marg):
            for ai, (a, _) in enumerate(states[i]):
                for bi, (b, _) in enumerate(states[i + 1]):
                    p = float(m[ai, bi])
                    if a == b or p < MIN_PAIR_PROB:
                        continue
                    pk = (a, b) if a < b else (b, a)
                    pair_probs[pk] = max(pair_probs.get(pk, 0.0), p)
                    if i == len(pair_marg) - 1:
                        last_hop[a] = p

        hop_probs: dict[str, float] = {}
        for i in range(first_hop, first_hop + len(hops)):
            for si, (k, _) in enumerate(states[i]):
                hop_probs[k] = max(hop_probs.get(k, 0.0), float(node_marg[i][si]))

        return ChainResult(
            pair_probs=pair_probs,
            last_hop=last_hop,
            hop_probs=hop_probs,
            ambiguous_hops=sum(len(s) > 1 for s in hop_states),
            unresolved_hops=sum(s[0][0].startswith(PLACEHOLDER_PREFIX) for s in hop_states),
        )
