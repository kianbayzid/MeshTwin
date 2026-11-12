// Throws (non-zero exit) if any text pairing in the palette is below WCAG AA 4.5:1.
// Run: node --experimental-strip-types scripts/check-contrast.ts
import { palette, textPairs } from '../src/palette.ts';

function luminance(hex: string): number {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const [r, g, b] = c.map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function ratio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

if (Math.abs(ratio('#ffffff', '#000000') - 21) > 0.01) throw new Error('contrast formula broken');

let failed = false;
for (const [fg, bg] of textPairs) {
  const r = ratio(palette[fg], palette[bg]);
  const ok = r >= 4.5;
  failed ||= !ok;
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${fg} on ${bg}: ${r.toFixed(2)}:1`);
}
if (failed) throw new Error('contrast check failed');
