// Regenerates the README media: two screenshots and a short GIF of the 3D map and the link inspector.
// Needs: an API serving the e2e build (npm run build:e2e) and real summaries, a local Chrome, and ffmpeg on PATH.
//   api -static-dir web/dist-e2e -summaries-dir analytics/.out/terrain -listen 127.0.0.1:18080
//   node scripts/capture-media.mjs http://127.0.0.1:18080
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from '@playwright/test';

const base = process.argv[2] ?? 'http://127.0.0.1:18080';
const LINK = '351FC1F9|FDC42289'; // the 8.9 km link blocked by a ridge: the clearest profile in the SEA data
const out = new URL('../../docs/media/', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1');
const size = { width: 1280, height: 800 };
mkdirSync(out, { recursive: true });
const frameDir = mkdtempSync(join(tmpdir(), 'meshtwin-media-'));
let frame = 0;
let recordedMs = 0;

// Frames are screenshots taken back to back while an action runs (no Playwright video, which needs its own ffmpeg).
async function record(page, ms, action) {
  const t0 = Date.now();
  const done = action?.();
  while (Date.now() - t0 < ms) {
    const png = await page.screenshot({ type: 'png' });
    writeFileSync(join(frameDir, `f${String(frame++).padStart(4, '0')}.png`), png);
  }
  await done;
  recordedMs += Date.now() - t0;
}

const browser = await chromium.launch({ channel: process.env.PW_CHANNEL ?? 'chrome' });
const context = await browser.newContext({ viewport: size });
const page = await context.newPage();
try {
  await page.goto(base);
  await page.waitForFunction(() => {
    const m = window.__map;
    return m?.getLayer('nodes') && m.areTilesLoaded() && m.queryRenderedFeatures({ layers: ['nodes'] }).length > 0;
  }, undefined, { timeout: 60_000 });
  await page.waitForTimeout(1500);
  await page.screenshot({ path: join(out, 'console-overview.png') });

  // A slow orbit shows the terrain is real 3D, then the link inspector opens on a blocked link.
  await record(page, 900);
  await record(page, 3800, () => page.evaluate(() =>
    window.__map.easeTo({ bearing: -35, pitch: 62, zoom: window.__map.getZoom() + 0.4, duration: 3500 })));
  const p = await page.evaluate((key) => {
    const m = window.__map;
    const nodes = m.getSource('nodes').serialize().data.features;
    const at = (id) => nodes.find((n) => n.properties.id === id)?.geometry.coordinates;
    const [a, b] = key.split('|').map(at);
    const r = m.getCanvas().getBoundingClientRect();
    for (const t of [0.5, 0.4, 0.6, 0.3, 0.7]) {
      const q = m.project([a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t]);
      if (!m.queryRenderedFeatures(q, { layers: ['nodes'] }).length) return { x: q.x + r.left, y: q.y + r.top };
    }
    return null;
  }, LINK);
  if (!p) throw new Error(`link ${LINK} not clickable on screen`);
  await record(page, 1600, async () => {
    await page.mouse.click(p.x, p.y);
    await page.waitForSelector('.profile svg[role="img"]');
  });
  const chart = await page.locator('.profile svg[role="img"]').boundingBox();
  for (const f of [0.3, 0.45, 0.55, 0.65]) {
    await record(page, 450, () => page.mouse.move(chart.x + chart.width * f, chart.y + chart.height * 0.5, { steps: 6 }));
  }
  await page.screenshot({ path: join(out, 'console-link-profile.png') });
  await record(page, 1200);
} finally {
  await browser.close();
}

// Frames arrived at whatever rate screenshots allowed; play them back at that measured rate.
const rate = (frame / (recordedMs / 1000)).toFixed(2);
const input = ['-framerate', rate, '-i', join(frameDir, 'f%04d.png')];
// Two-pass palette GIF, 900 px wide: small enough for a README.
const filters = 'fps=10,scale=900:-1:flags=lanczos';
const palette = join(frameDir, 'palette.png');
execFileSync('ffmpeg', ['-y', '-loglevel', 'error', ...input, '-vf', `${filters},palettegen=stats_mode=diff`, palette]);
execFileSync('ffmpeg', ['-y', '-loglevel', 'error', ...input, '-i', palette, '-lavfi',
  `${filters} [x]; [x][1:v] paletteuse=dither=bayer:bayer_scale=5`, join(out, 'console.gif')]);
rmSync(frameDir, { recursive: true, force: true });
console.log(`${frame} frames over ${(recordedMs / 1000).toFixed(1)} s (${rate} fps)`);
console.log(`wrote ${out}console-overview.png, console-link-profile.png, console.gif`);
