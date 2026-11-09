"""Geography helpers: distance, the link-plausibility prior, and the privacy snap.

`snap` is the ONE function every coordinate passes through before it leaves the
analytics layer (CLAUDE.md, docs/api-contract.md privacy rule 1). Do not round
coordinates anywhere else.
"""

import math

EARTH_RADIUS_KM = 6371.0

# Link-plausibility prior p(d) = 1 / (1 + exp((d - D50) / S)).
# D50 is the distance at which we think a link is a coin flip; S sets how fast
# belief falls off around it. 15 km / 4 km: LoRa repeaters on good sites reach
# 10-20 km, companions much less, and a 40 km "link" is almost always a hash
# collision. p(2 km) ~ 0.96, p(15 km) = 0.5, p(30 km) ~ 0.02, p(80 km) ~ 1e-7.
LINK_D50_KM = 15.0
LINK_SCALE_KM = 4.0

# Used when either end has no known position. 0.5 = "as plausible as a 15 km
# link": no evidence for or against, so geography neither helps nor hurts.
NEUTRAL_LINK_PRIOR = 0.5

GRID_DEG = 0.01


def haversine_km(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Great-circle distance in km. Accurate enough at regional scale."""
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dlat, dlon = p2 - p1, math.radians(lon2 - lon1)
    h = math.sin(dlat / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dlon / 2) ** 2
    return 2 * EARTH_RADIUS_KM * math.asin(math.sqrt(h))


def log_link_prior(distance_km: float | None) -> float:
    """log p(d) of the logistic prior above; neutral when the distance is unknown.

    Computed as -log(1 + exp(x)) via logaddexp so a 500 km distance cannot overflow.
    """
    if distance_km is None:
        return math.log(NEUTRAL_LINK_PRIOR)
    x = (distance_km - LINK_D50_KM) / LINK_SCALE_KM
    return -_logaddexp0(x)


def _logaddexp0(x: float) -> float:
    """log(1 + exp(x)) without overflow."""
    return x + math.log1p(math.exp(-x)) if x > 0 else math.log1p(math.exp(x))


def snap(lat: float | None, lon: float | None) -> dict[str, float] | None:
    """Snap a point to the centre-labelled 0.01 degree grid cell, or None if unknown.

    Rounding to 2 decimals maps every point within +-0.005 deg of a grid line to it,
    so the output is a ~1 km cell label, never the raw point.
    """
    if lat is None or lon is None:
        return None
    return {"lat": round(lat, 2), "lon": round(lon, 2), "grid_deg": GRID_DEG}
