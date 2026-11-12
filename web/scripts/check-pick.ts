// Self-check for src/map/pick.ts. Run: node --experimental-strip-types scripts/check-pick.ts
import { segmentDistance } from '../src/map/pick.ts';

function near(a: number, b: number, msg: string): void {
  if (Math.abs(a - b) > 1e-9) throw new Error(`check-pick: ${msg}: ${a} != ${b}`);
}

const A = { x: 0, y: 0 };
const B = { x: 10, y: 0 };
near(segmentDistance({ x: 5, y: 3 }, A, B), 3, 'perpendicular distance inside the segment');
near(segmentDistance({ x: -4, y: 3 }, A, B), 5, 'before the start: distance to the start point');
near(segmentDistance({ x: 13, y: 4 }, A, B), 5, 'past the end: distance to the end point');
near(segmentDistance({ x: 2, y: 2 }, A, A), Math.hypot(2, 2), 'zero-length segment is a point');
console.log('check-pick: ok');
