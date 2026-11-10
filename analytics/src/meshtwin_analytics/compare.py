"""Predicted vs observed: residuals per measured link, and where model and reality disagree.

Rows keep full keys and use raw positions (internal); the *_json functions apply the
same public id / snapping rules as summaries.py. A published distance is measured
between the SNAPPED cell centres, so it reveals nothing finer than the published grid.
"""

import math
from dataclasses import dataclass
from itertools import combinations
from typing import Any

import numpy as np

from .aggregate import LinkEvidence, percentile
from .geo import haversine_km, snap
from .model import Node
from .pipeline import TopologyRun
from .propagation import LinkPrediction, PropagationModel, Radio, Site
from .summaries import meta, public_id

SOURCE = "propagation-job"
STRONG_MARGIN_DB = 10.0  # "predicted strong" = this far above the demodulation floor
MAX_UNSEEN_DISTANCE_KM = 30.0
MAX_UNSEEN = 50
IMPOSSIBLE_REASONS = ["hop-hash misattribution", "ducting", "DEM misses a tall mast"]
UNSEEN_REASONS = ["building clutter", "antenna/feedline fault", "node misconfigured"]


@dataclass(frozen=True)
class Residual:
    a: str  # full keys, a < b
    b: str
    prediction: LinkPrediction
    observed_snr_db: float  # median of last-hop samples
    samples: int
    confidence: float

    @property
    def residual_db(self) -> float:
        return self.observed_snr_db - self.prediction.predicted_snr_db


@dataclass(frozen=True)
class Unseen:
    a: str
    b: str
    prediction: LinkPrediction


def site(node: Node, radio: Radio) -> Site | None:
    if node.lat is None or node.lon is None:
        return None
    return Site(node.lat, node.lon, radio.antenna_height_m(node.role))


def residuals(run: TopologyRun, model: PropagationModel, radio: Radio) -> list[Residual]:
    """Every exported link with measured SNR and both ends located, worst |residual| first."""
    rows = []
    for u, v in run.graph.edges:
        a, b = min(u, v), max(u, v)
        le: LinkEvidence = run.graph.edges[a, b]["evidence"]
        sa, sb = site(run.nodes[a], radio), site(run.nodes[b], radio)
        if not le.snr or sa is None or sb is None:
            continue
        rows.append(
            Residual(
                a, b, model.predict(sa, sb), percentile(le.snr, 50), len(le.snr), le.confidence
            )
        )
    return sorted(rows, key=lambda r: (-abs(r.residual_db), r.a, r.b))


def unseen_strong(run: TopologyRun, model: PropagationModel, radio: Radio) -> list[Unseen]:
    """Relay pairs, both heard in the window and located, at most 30 km apart, with no
    inferred link at any confidence, that the model says should be strong."""
    g = run.graph
    relays = sorted(
        k
        for k in g.nodes
        if g.nodes[k]["relay"]
        and (g.nodes[k]["last_heard_us"] or 0) >= run.from_us
        and site(run.nodes[k], radio) is not None
    )
    found = []
    for a, b in combinations(relays, 2):
        if (a, b) in run.evidence.links:
            continue
        sa, sb = site(run.nodes[a], radio), site(run.nodes[b], radio)
        assert sa is not None and sb is not None
        if haversine_km(sa.lat, sa.lon, sb.lat, sb.lon) > MAX_UNSEEN_DISTANCE_KM:
            continue
        p = model.predict(sa, sb)
        if p.predicted_snr_db >= radio.snr_floor_db + STRONG_MARGIN_DB:
            found.append(Unseen(a, b, p))
    found.sort(key=lambda u: (-u.prediction.predicted_snr_db, u.a, u.b))
    return found[:MAX_UNSEEN]


# --- JSON (docs/api-contract.md) ----------------------------------------------------------


def public_distance_km(a: Node, b: Node) -> float:
    """Distance between the snapped cells of a and b, to 0.1 km (both must be located)."""
    ca, cb = snap(a.lat, a.lon), snap(b.lat, b.lon)
    assert ca is not None and cb is not None
    return round(haversine_km(ca["lat"], ca["lon"], cb["lat"], cb["lon"]), 1)


def _link(run: TopologyRun, a: str, b: str, p: LinkPrediction) -> dict[str, Any]:
    return {
        "a": public_id(a),
        "b": public_id(b),
        "distance_km": public_distance_km(run.nodes[a], run.nodes[b]),
        "predicted_snr_db": round(p.predicted_snr_db, 1),
    }


def residuals_json(run: TopologyRun, rows: list[Residual], model: dict[str, str]) -> dict[str, Any]:
    res = np.array([r.residual_db for r in rows])
    summary: dict[str, Any] = {
        "links": len(rows),
        # Undefined without links: null rather than a misleading 0.
        "median_abs_residual_db": round(float(np.median(np.abs(res))), 1) if rows else None,
        "rmse_db": round(math.sqrt(float(np.mean(res**2))), 1) if rows else None,
        "bias_db": round(float(np.mean(res)), 1) if rows else None,
    }
    links = [
        _link(run, r.a, r.b, r.prediction)
        | {
            "observed_snr_db": round(r.observed_snr_db, 1),
            "residual_db": round(r.residual_db, 1),
            "samples": r.samples,
            "line_of_sight": r.prediction.line_of_sight,
            "fresnel_clearance": round(r.prediction.fresnel_clearance, 2),
            "diffraction_loss_db": round(r.prediction.diffraction_loss_db, 1),
            "confidence": round(r.confidence, 3),
        }
        for r in rows
    ]
    return {"meta": meta(run, SOURCE), "model": model, "summary": summary, "links": links}


def surprising_json(
    run: TopologyRun, rows: list[Residual], unseen: list[Unseen], radio: Radio
) -> dict[str, Any]:
    impossible = [
        _link(run, r.a, r.b, r.prediction)
        | {
            "observed_snr_db": round(r.observed_snr_db, 1),
            "samples": r.samples,
            "confidence": round(r.confidence, 3),
            "possible_reasons": IMPOSSIBLE_REASONS,
        }
        for r in rows  # already worst |residual| first
        if r.prediction.predicted_snr_db < radio.snr_floor_db
    ]
    never_seen = [
        _link(run, u.a, u.b, u.prediction)
        | {"both_active": True, "possible_reasons": UNSEEN_REASONS}
        for u in unseen
    ]
    return {
        "meta": meta(run, SOURCE),
        "observed_but_predicted_impossible": impossible,
        "predicted_strong_but_never_seen": never_seen,
    }
