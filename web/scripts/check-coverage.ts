// Self-check for src/map/coverage.ts. Run: node --experimental-strip-types scripts/check-coverage.ts
import type { Coverage } from '../src/api/types.ts';
import { coverageGeo } from '../src/map/coverage.ts';

function eq(a: unknown, b: unknown, msg: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`check-coverage: ${msg}: ${JSON.stringify(a)} != ${JSON.stringify(b)}`);
}
const near = (a: number, b: number) => Math.abs(a - b) < 1e-9;

// 2 rows x 3 cols, row 0 north. The null cell has no repeater in range.
const c = {
  method: { grid_deg: 0.01, snr_floor_db: -7.5 },
  grid: { north: 47.61, west: -122.35, rows: 2, cols: 3 },
  snr_db: [1, 2, null, 4, 5, 6],
} as unknown as Coverage;

const f = coverageGeo(c).features;
eq(f.length, 5, 'null cells are skipped');
eq(f.map((x) => x.properties?.snr), [1, 2, 4, 5, 6], 'row-major order');
eq(f[0]!.properties?.margin, 8.5, 'margin is SNR above the floor');

// Cell (1, 2) (snr 6) is centred one row south and two columns east of the north-west centre.
const ring = f[4]!.geometry.coordinates[0]!;
const sw = ring[0]!;
if (!near(sw[0]!, -122.35 + 0.02 - 0.005) || !near(sw[1]!, 47.61 - 0.01 - 0.005)) throw new Error(`check-coverage: south-west corner ${sw}`);
if (!near(ring[2]![0]! - ring[0]![0]!, 0.01) || !near(ring[2]![1]! - ring[0]![1]!, 0.01)) throw new Error('check-coverage: cell size');
eq(coverageGeo(null).features, [], 'no coverage summary draws nothing');
console.log('check-coverage: ok');
