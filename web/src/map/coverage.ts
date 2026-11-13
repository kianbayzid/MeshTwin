// Predicted coverage grid (docs/api-contract.md, /coverage) to one square polygon per cell.
import type { FeatureCollection, Polygon } from 'geojson';
import type { Coverage } from '../api/types';

// Colours run from the decode floor up to this margin; beyond it a radio can't tell the difference
// (and right beside a repeater the model predicts tens of dB more than any receiver reports).
export const COVERAGE_MAX_MARGIN_DB = 30;

// shortcut: one GeoJSON polygon per cell is fine for city-sized grids (~1.5k cells for SEA); for grids near the
// contract's 250k-cell cap, draw the grid into a canvas image source instead.
export function coverageGeo(c: Coverage | null): FeatureCollection<Polygon> {
  if (!c) return { type: 'FeatureCollection', features: [] };
  const { north, west, rows, cols } = c.grid;
  const d = c.method.grid_deg;
  const h = d / 2;
  const features: FeatureCollection<Polygon>['features'] = [];
  for (let r = 0; r < rows; r++) {
    for (let col = 0; col < cols; col++) {
      const snr = c.snr_db[r * cols + col];
      if (snr == null) continue; // no repeater within range: nothing to draw
      const lat = north - r * d;
      const lon = west + col * d;
      features.push({
        type: 'Feature',
        geometry: {
          type: 'Polygon',
          coordinates: [[[lon - h, lat - h], [lon + h, lat - h], [lon + h, lat + h], [lon - h, lat + h], [lon - h, lat - h]]],
        },
        properties: { snr, margin: snr - c.method.snr_floor_db },
      });
    }
  }
  return { type: 'FeatureCollection', features };
}
