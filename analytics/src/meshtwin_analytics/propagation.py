"""Point-to-point link prediction: free-space loss plus one knife edge over a terrain profile.

The model sits behind `PropagationModel` so it can be swapped (e.g. Longley-Rice, or
Deygout for several ridges) without touching the comparison code.
"""

import math
from dataclasses import dataclass
from typing import Protocol

import numpy as np

from .geo import EARTH_RADIUS_KM, haversine_km
from .terrain import Array, Terrain

# Demodulation SNR floor per spreading factor: Semtech SX1262 datasheet Rev 1.2, Table 6-1.
DEMOD_SNR_FLOOR_DB: dict[int, float] = {
    7: -7.5,
    8: -10.0,
    9: -12.5,
    10: -15.0,
    11: -17.5,
    12: -20.0,
}


@dataclass(frozen=True)
class Radio:
    """Every radio assumption in one place.

    US preset freq/BW/SF/CR: MeshCore preset API api.meshcore.nz/api/v1/config and
    MeshCore docs/faq.md section 2.3. TX power: LORA_TX_POWER in the heltec_v3 and
    rak4631 variants' platformio.ini. Noise figure: not stated in the SX1262 datasheet;
    derived from its Table 3-8 boosted-gain sensitivity (6.0-6.5 dB).
    """

    freq_mhz: float = 910.525
    bw_khz: float = 62.5
    sf: int = 7
    cr: int = 5  # coding rate 4/5; recorded for completeness, the link budget doesn't use it
    tx_dbm: float = 22.0
    noise_figure_db: float = 6.0
    # UNVERIFIED site assumptions (no source can know real installs): antenna gains and
    # heights. Errors here show up as a bias in the residuals.
    antenna_gain_dbi: float = 0.0  # each end
    infrastructure_height_m: float = 10.0  # repeaters and room servers: masts, roofs
    client_height_m: float = 2.0  # companions, sensors, unknown: hand or window height

    @property
    def noise_floor_dbm(self) -> float:
        """Thermal noise kTB (-174 dBm/Hz at 290 K) over the bandwidth, plus the receiver's NF."""
        return -174.0 + 10 * math.log10(self.bw_khz * 1000) + self.noise_figure_db

    @property
    def snr_floor_db(self) -> float:
        return DEMOD_SNR_FLOOR_DB[self.sf]

    def antenna_height_m(self, role: str) -> float:
        return (
            self.infrastructure_height_m if role in ("repeater", "room") else self.client_height_m
        )


# Effective Earth radius factor: standard atmosphere bends radio slightly downwards,
# which looks like a flatter Earth with radius 4/3 of the real one (~8500 km,
# ITU-R P.526-16 section 1).
K_FACTOR = 4 / 3
SPEED_OF_LIGHT_M_S = 299_792_458.0
PROFILE_STEP_M = 30.0  # the DEM's resolution: finer sampling adds nothing
MAX_PROFILE_POINTS = 2000  # 60 km at 30 m; longer paths are sampled more coarsely
MIN_DISTANCE_KM = 0.01  # two nodes at one spot: avoid log10(0)


@dataclass(frozen=True)
class Site:
    lat: float
    lon: float
    height_m: float  # antenna height above ground


@dataclass(frozen=True)
class LinkPrediction:
    distance_km: float
    path_loss_db: float
    predicted_snr_db: float
    line_of_sight: bool  # no terrain point above the straight ray
    fresnel_clearance: float  # min clearance / first-Fresnel radius; < 0 when blocked
    diffraction_loss_db: float


@dataclass(frozen=True)
class PathGeometry:
    """One path's terrain profile (n samples, a at index 0) and its knife-edge result."""

    distance_km: float
    ground_m: Array
    bulge_m: Array  # effective Earth-curvature rise; 0 at both ends
    fresnel_r1_m: Array  # first Fresnel zone radius; 0 at both ends
    line_of_sight: bool
    fresnel_clearance: float
    diffraction_loss_db: float


class PropagationModel(Protocol):
    name: str
    version: str

    def predict(self, a: Site, b: Site) -> LinkPrediction: ...


def fspl_db(distance_km: float, freq_mhz: float) -> float:
    """Free-space path loss: 20 log10(d_km) + 20 log10(f_MHz) + 32.44."""
    return 20 * math.log10(distance_km) + 20 * math.log10(freq_mhz) + 32.44


def knife_edge_db(v: float) -> float:
    """Single knife-edge loss J(v), ITU-R P.526-16 section 4.1 Eq. (31); 0 for v <= -0.78."""
    if v <= -0.78:
        return 0.0
    return 6.9 + 20 * math.log10(math.sqrt((v - 0.1) ** 2 + 1) + v - 0.1)


class FsplKnifeEdge:
    """Free-space loss plus the single worst knife edge on the terrain profile.

    shortcut: only the dominant edge counts, so a path over several ridges is predicted
    too strong; Deygout (recurse on each side of the main edge) is the upgrade.
    """

    name = "fspl+knife-edge"
    version = "1"

    def __init__(self, terrain: Terrain, radio: Radio) -> None:
        self.terrain = terrain
        self.radio = radio
        self.profiles = 0  # predictions made, for the profiles/second benchmark

    def geometry(self, a: Site, b: Site, max_points: int = MAX_PROFILE_POINTS) -> PathGeometry:
        """The terrain profile from a to b and its knife-edge result.

        The one source of path geometry: `predict` and the published profiles
        (compare.profile) both call it, so the chart and the model cannot drift apart.
        """
        d_km = max(haversine_km(a.lat, a.lon, b.lat, b.lon), MIN_DISTANCE_KM)
        d = d_km * 1000
        n = min(max_points, max(3, math.ceil(d / PROFILE_STEP_M) + 1))
        # A straight line in lat/lon is within metres of the great circle below ~50 km.
        t = np.linspace(0.0, 1.0, n)
        ground = self.terrain.elevation_m(a.lat + t * (b.lat - a.lat), a.lon + t * (b.lon - a.lon))

        # d1 or d2 is exactly 0 at the ends, so bulge and r1 are 0 there.
        d1, d2 = t * d, (1 - t) * d
        start, end = ground[0] + a.height_m, ground[-1] + b.height_m
        ray = start + (end - start) * t
        bulge = d1 * d2 / (2 * K_FACTOR * EARTH_RADIUS_KM * 1000)
        wavelength = SPEED_OF_LIGHT_M_S / (self.radio.freq_mhz * 1e6)
        fresnel_r1 = np.sqrt(wavelength * d1 * d2 / d)

        # Interior points only: the endpoints are the antennas themselves.
        h = (ground + bulge - ray)[1:-1]  # obstacle height above the ray (> 0 blocks)
        worst = float(np.min(-h / fresnel_r1[1:-1]))
        # ITU-R P.526-16 Eq. (26): v = h sqrt(2 (d1 + d2) / (lambda d1 d2)) = sqrt(2) h / r1,
        # so the point with the largest v is the one with the smallest Fresnel clearance.
        return PathGeometry(
            distance_km=d_km,
            ground_m=ground,
            bulge_m=bulge,
            fresnel_r1_m=fresnel_r1,
            line_of_sight=bool(np.all(h <= 0)),
            fresnel_clearance=worst,
            diffraction_loss_db=knife_edge_db(-math.sqrt(2) * worst),
        )

    def predict(self, a: Site, b: Site) -> LinkPrediction:
        self.profiles += 1
        g = self.geometry(a, b)
        r = self.radio
        loss = fspl_db(g.distance_km, r.freq_mhz) + g.diffraction_loss_db
        snr = r.tx_dbm + 2 * r.antenna_gain_dbi - loss - r.noise_floor_dbm
        return LinkPrediction(
            distance_km=g.distance_km,
            path_loss_db=loss,
            predicted_snr_db=snr,
            line_of_sight=g.line_of_sight,
            fresnel_clearance=g.fresnel_clearance,
            diffraction_loss_db=g.diffraction_loss_db,
        )
