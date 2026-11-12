// Click picking for overlapping link hit areas. Pure apart from map.project, so the maths can be checked without a map.
import type { Map as MlMap } from 'maplibre-gl';

// Screen distance from p to the nearest segment of each line feature; returns the closest feature.
export function nearest<F extends { geometry: GeoJSON.Geometry }>(map: MlMap, p: { x: number; y: number }, fs: F[]): F | undefined {
  let best: F | undefined;
  let bestD = Infinity;
  for (const f of fs) {
    if (f.geometry.type !== 'LineString') continue;
    const pts = f.geometry.coordinates.map((c) => map.project(c as [number, number]));
    for (let i = 1; i < pts.length; i++) {
      const d = segmentDistance(p, pts[i - 1]!, pts[i]!);
      if (d < bestD) [best, bestD] = [f, d];
    }
  }
  return best;
}

export function segmentDistance(p: { x: number; y: number }, a: { x: number; y: number }, b: { x: number; y: number }): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const len2 = dx * dx + dy * dy;
  const t = len2 ? Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2)) : 0;
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}
