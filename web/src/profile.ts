// Pure geometry for the link profile chart. Everything here is drawing, not physics: the arrays come from the
// propagation job (docs/api-contract.md, links/{a}/{b}/profile), so the chart can't drift from the model.
import type { Profile } from './api/types';

export interface Sample {
  km: number;
  terrain: number; // ground + Earth-curvature bulge: what the ray has to clear
  los: number; // straight line between the two antennas
  r1: number; // first Fresnel zone radius
  clearance: number; // (los − terrain) / r1; Infinity at the antennas where r1 = 0
  state: 'clear' | 'fresnel' | 'blocked';
}

// The usual radio rule: a link is clear when 60 % of the first Fresnel zone is free of obstacles.
export const FRESNEL_CLEAR = 0.6;

export function samples(p: Profile): Sample[] {
  const n = p.ground_m.length;
  const start = p.ground_m[0]! + p.antenna_a_m;
  const end = p.ground_m[n - 1]! + p.antenna_b_m;
  return p.ground_m.map((g, i) => {
    const t = n > 1 ? i / (n - 1) : 0;
    const terrain = g + p.bulge_m[i]!;
    const los = start + (end - start) * t;
    const r1 = p.fresnel_r1_m[i]!;
    const clearance = r1 > 0 ? (los - terrain) / r1 : Infinity;
    const state = terrain > los ? 'blocked' : clearance < FRESNEL_CLEAR ? 'fresnel' : 'clear';
    return { km: p.distance_km * t, terrain, los, r1, clearance, state };
  });
}

export function extent(s: Sample[]): [number, number] {
  const lo = Math.min(...s.map((x) => x.terrain));
  const hi = Math.max(...s.map((x) => Math.max(x.terrain, x.los + x.r1)));
  const pad = Math.max(5, (hi - lo) * 0.08);
  return [lo - pad, hi + pad];
}

// Index of the sample with the least clearance (the model's knife edge), or −1 if the path has no interior.
export function worst(s: Sample[]): number {
  let best = -1;
  s.forEach((x, i) => {
    if (Number.isFinite(x.clearance) && (best < 0 || x.clearance < s[best]!.clearance)) best = i;
  });
  return best;
}
