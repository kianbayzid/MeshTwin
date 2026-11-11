// Types for docs/api-contract.md (v1). Locations are 0.01° grid cells; the API never sends raw points.

export interface Meta {
  schema_version: number;
  region: string;
  generated_at: string;
  window: { from: string; to: string } | null; // null only for terrain.json
  source: string;
}

export interface GridLocation {
  lat: number;
  lon: number;
  grid_deg: number;
}

export type Role = 'repeater' | 'companion' | 'room' | 'sensor' | 'unknown';

export interface Node {
  id: string;
  role: Role;
  name: string | null;
  location: GridLocation | null;
  observer: boolean;
  degree: number;
  last_heard: string;
}

export interface Link {
  a: string;
  b: string;
  observations: number;
  confidence: number;
  snr_db: { median: number; p10: number; p90: number; samples: number } | null;
  rssi_dbm: { median: number; samples: number } | null;
  first_seen: string;
  last_seen: string;
}

export interface Topology {
  meta: Meta;
  stats: {
    receptions: number;
    flood_receptions: number;
    observers: number;
    nodes: number;
    links: number;
    ambiguous_hop_share: number;
    unresolved_hops: number;
  };
  nodes: Node[];
  links: Link[];
}

export interface Spof {
  id: string;
  name: string | null;
  role: Role;
  location: GridLocation | null;
  articulation_point: boolean;
  reachable_pairs_lost: number;
  reachable_share_after: number;
  nodes_cut_off: number;
  confidence: number;
}

export interface Resilience {
  meta: Meta;
  baseline: { nodes: number; reachable_pairs: number; reachable_share: number };
  single_points_of_failure: Spof[];
  bridges: { a: string; b: string; reachable_pairs_lost: number; confidence: number }[];
}

export interface ResidualLink {
  a: string;
  b: string;
  distance_km: number;
  predicted_snr_db: number;
  observed_snr_db: number;
  residual_db: number;
  samples: number;
  line_of_sight: boolean;
  fresnel_clearance: number;
  diffraction_loss_db: number;
  confidence: number;
}

export interface Residuals {
  meta: Meta;
  model: { name: string; version: string; dem: string };
  summary: {
    links: number;
    median_abs_residual_db: number | null;
    rmse_db: number | null;
    bias_db: number | null;
  };
  links: ResidualLink[];
}

export interface PlacementCandidate {
  rank: number;
  location: GridLocation;
  score: number;
  coverage_gain_nodes: number;
  spofs_removed: string[];
  reachable_pairs_gained: number;
  new_links: { to: string; predicted_snr_db: number }[];
}

export interface Placement {
  meta: Meta;
  method: {
    grid_deg: number;
    candidates_scored: number;
    antenna_height_m: number;
    model: string;
    dem: string;
    spofs_checked_top: number;
  };
  candidates: PlacementCandidate[];
}

export const linkKey = (a: string, b: string): string => (a < b ? `${a}|${b}` : `${b}|${a}`);

export interface Surprising {
  meta: Meta;
  observed_but_predicted_impossible: {
    a: string;
    b: string;
    distance_km: number;
    predicted_snr_db: number;
    observed_snr_db: number;
    samples: number;
    confidence: number;
    possible_reasons: string[];
  }[];
  predicted_strong_but_never_seen: {
    a: string;
    b: string;
    distance_km: number;
    predicted_snr_db: number;
    both_active: boolean;
    possible_reasons: string[];
  }[];
}

export interface RegionInfo {
  region: string;
  updated_at: string | null;
  nodes: number | null; // null until the topology job has run
  links: number | null;
  summaries: string[];
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}

export interface Profile {
  a: string;
  b: string;
  distance_km: number;
  antenna_a_m: number;
  antenna_b_m: number;
  ground_m: number[];
  bulge_m: number[];
  fresnel_r1_m: number[];
  line_of_sight: boolean;
  fresnel_clearance: number;
  diffraction_loss_db: number;
}

export interface ProfileResponse {
  meta: Meta;
  model: Residuals['model'];
  params: { freq_mhz: number; k_factor: number; grid_deg: number };
  profile: Profile;
}
