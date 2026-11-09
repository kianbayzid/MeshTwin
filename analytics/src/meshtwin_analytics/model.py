"""Shared data types and role constants."""

from dataclasses import dataclass

# ADVERT flags bits 0-3 (docs/protocol.md section 4). "chat" is called "companion" in the API.
ROLE_NAMES: dict[int, str] = {1: "companion", 2: "repeater", 3: "room", 4: "sensor"}

# Roles that forward flood packets by default. Firmware (MeshCore 81dbae5): repeaters
# forward; room servers ship with disable_fwd=1, sensors and companions do not forward,
# though an owner can switch forwarding on. Used for (a) the first candidate tier in hop
# resolution and (b) who counts as a relay in resilience. Change here only.
RELAY_ROLES: frozenset[str] = frozenset({"repeater"})

PLACEHOLDER_PREFIX = "unknown:"


@dataclass(frozen=True)
class Node:
    """A node we know about.

    `key` is the full 64-hex public key, or `unknown:<HEX>` for a hop hash that
    matched no known node. lat/lon are raw advert coordinates (private: they must
    go through geo.snap before leaving analytics).
    """

    key: str
    role: str = "unknown"
    name: str | None = None
    lat: float | None = None
    lon: float | None = None

    @property
    def is_placeholder(self) -> bool:
        return self.key.startswith(PLACEHOLDER_PREFIX)
