// Pure conversion from API summaries to GeoJSON. Only grid-cell locations exist here; nodes without one are skipped.
import type { FeatureCollection, LineString, Point, Position } from 'geojson';
import { TOP_CANDIDATES } from '../api/client';
import {
  linkKey, type Placement, type Residuals, type Resilience, type Surprising, type Topology,
} from '../api/types';

export interface Filters {
  minSnr: number;
  minConfidence: number;
}

export const SNR_FLOOR = -20;

type Positions = Map<string, Position>;

export const positions = (t: Topology): Positions =>
  new Map(t.nodes.flatMap((n) => (n.location ? [[n.id, [n.location.lon, n.location.lat]] as const] : [])));

export function nodesGeo(t: Topology, r: Resilience | null): FeatureCollection<Point> {
  const spofs = new Set(r?.single_points_of_failure.map((s) => s.id));
  return {
    type: 'FeatureCollection',
    features: t.nodes.flatMap((n) =>
      n.location
        ? [{
            type: 'Feature' as const,
            geometry: { type: 'Point' as const, coordinates: [n.location.lon, n.location.lat] },
            properties: { id: n.id, icon: `node-${n.role}${n.observer ? '-obs' : ''}`, spof: spofs.has(n.id) },
          }]
        : [],
    ),
  };
}

export function linksGeo(t: Topology, res: Residuals | null, f: Filters): FeatureCollection<LineString> {
  const pos = positions(t);
  const resid = new Map(res?.links.map((l) => [linkKey(l.a, l.b), l.residual_db]));
  return {
    type: 'FeatureCollection',
    features: t.links.flatMap((l) => {
      const a = pos.get(l.a);
      const b = pos.get(l.b);
      if (!a || !b || l.confidence < f.minConfidence) return [];
      // Unmeasured links have no SNR; keep them unless the user asks for a floor above the scale minimum.
      if (l.snr_db ? l.snr_db.median < f.minSnr : f.minSnr > SNR_FLOOR) return [];
      const key = linkKey(l.a, l.b);
      const r = resid.get(key);
      return [{
        type: 'Feature' as const,
        geometry: { type: 'LineString' as const, coordinates: [a, b] },
        // Absent (not null) properties so ['has', …] works in style expressions.
        properties: { key, confidence: l.confidence, ...(l.snr_db && { snr: l.snr_db.median }), ...(r !== undefined && { residual: r }) },
      }];
    }),
  };
}

// Links the model expects to be strong that the mesh never used: the "predicted only" half of the story.
export function predictedOnlyGeo(t: Topology, s: Surprising | null): FeatureCollection<LineString> {
  const pos = positions(t);
  return {
    type: 'FeatureCollection',
    features: (s?.predicted_strong_but_never_seen ?? []).flatMap((l) => {
      const a = pos.get(l.a);
      const b = pos.get(l.b);
      return a && b
        ? [{ type: 'Feature' as const, geometry: { type: 'LineString' as const, coordinates: [a, b] }, properties: { predicted: l.predicted_snr_db } }]
        : [];
    }),
  };
}

export function placementGeo(p: Placement | null): FeatureCollection<Point> {
  return {
    type: 'FeatureCollection',
    // Badges are drawn for ranks 1..TOP_CANDIDATES only; never trust the response length for that.
    features: (p?.candidates ?? []).slice(0, TOP_CANDIDATES).map((c) => ({
      type: 'Feature' as const,
      geometry: { type: 'Point' as const, coordinates: [c.location.lon, c.location.lat] },
      properties: { rank: c.rank },
    })),
  };
}

export function bounds(t: Topology): [[number, number], [number, number]] | null {
  const pts = t.nodes.flatMap((n) => (n.location ? [n.location] : []));
  if (pts.length === 0) return null;
  const lons = pts.map((p) => p.lon);
  const lats = pts.map((p) => p.lat);
  return [[Math.min(...lons), Math.min(...lats)], [Math.max(...lons), Math.max(...lats)]];
}
