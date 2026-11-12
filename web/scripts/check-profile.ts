// Self-check for src/profile.ts. Run: node --experimental-strip-types scripts/check-profile.ts
import { samples, worst } from '../src/profile.ts';

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`check-profile: ${msg}`);
}

// Flat ground, 10 m masts, a 30 m hill in the middle.
//   i=1: terrain 0.5, los 10, r1 20 -> clearance 0.475 (< 0.6: inside the Fresnel zone)
//   i=2: terrain 30.7 > los 10 -> blocked, clearance (10 − 30.7)/14 = −1.479
//   i=3: terrain 0.5, r1 12 -> clearance 0.79 (clear)
const s = samples({
  a: 'AA', b: 'BB', distance_km: 4, antenna_a_m: 10, antenna_b_m: 10,
  ground_m: [0, 0, 30, 0, 0],
  bulge_m: [0, 0.5, 0.7, 0.5, 0],
  fresnel_r1_m: [0, 20, 14, 12, 0],
  line_of_sight: false, fresnel_clearance: -1.479, diffraction_loss_db: 15,
});

assert(s.length === 5, 'one sample per array entry');
assert(s[2]!.km === 2, 'samples are evenly spaced over distance_km');
assert(s[0]!.los === 10 && s[4]!.los === 10, 'line of sight runs antenna top to antenna top');
assert(Math.abs(s[2]!.terrain - 30.7) < 1e-9, 'terrain includes the Earth bulge');
assert(s[1]!.state === 'fresnel', 'clearance 0.475 is inside the 60 % Fresnel rule');
assert(s[2]!.state === 'blocked', 'terrain above the ray is blocked');
assert(s[3]!.state === 'clear', 'clearance 0.79 is clear');
assert(s[0]!.state === 'clear' && s[0]!.clearance === Infinity, 'antennas themselves have no clearance');
assert(worst(s) === 2, 'worst point is the hill');
assert(Math.abs(s[2]!.clearance - -1.4786) < 1e-3, 'chart clearance matches the model definition');
console.log('check-profile: ok');
