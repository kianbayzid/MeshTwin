// Performance probe, not a pass/fail gate: run with PERF=1 and read the numbers it prints (docs/benchmarks.md).
// Measures time to a drawn network, idle repaint rate (should be ~0: an idle map must not burn the GPU),
// frame times while panning, and JS heap.
import { test } from '@playwright/test';

type Win = Window & { __map?: import('maplibre-gl').Map };

test.skip(!process.env.PERF, 'set PERF=1 to run the performance probe');

test('performance probe', async ({ page }) => {
  const t0 = Date.now();
  await page.goto('/');
  await page.waitForFunction(() => {
    const m = (window as Win).__map;
    return !!m?.getLayer('nodes') && m.areTilesLoaded() && m.queryRenderedFeatures({ layers: ['nodes'] }).length > 0;
  }, undefined, { timeout: 60_000 });
  const firstNetworkMs = Date.now() - t0;
  await page.waitForTimeout(1500); // let terrain and late tiles settle

  const idleRendersPerS = await page.evaluate(async () => {
    const m = (window as Win).__map!;
    let n = 0;
    const on = () => n++;
    m.on('render', on);
    await new Promise((r) => setTimeout(r, 3000));
    m.off('render', on);
    return n / 3;
  });

  const pan = await page.evaluate(async () => {
    const m = (window as Win).__map!;
    const frames: number[] = [];
    let last = performance.now();
    let raf = 0;
    const tick = (t: number) => { frames.push(t - last); last = t; raf = requestAnimationFrame(tick); };
    raf = requestAnimationFrame(tick);
    m.panBy([400, 0], { duration: 1500 });
    await new Promise((r) => setTimeout(r, 1600));
    m.panBy([-400, 0], { duration: 1500 });
    await new Promise((r) => setTimeout(r, 1600));
    cancelAnimationFrame(raf);
    frames.sort((a, b) => a - b);
    const p = (q: number) => Math.round(frames[Math.floor(q * (frames.length - 1))]! * 10) / 10;
    return { frames: frames.length, p50_ms: p(0.5), p95_ms: p(0.95), max_ms: p(1) };
  });

  const counts = await page.evaluate(() => {
    const m = (window as Win).__map!;
    const n = (id: string) => ((m.getSource(id)!.serialize() as { data: { features: unknown[] } }).data.features.length);
    return { nodes: n('nodes'), links: n('links') };
  });
  const heapMB = await page.evaluate(() => {
    const mem = (performance as Performance & { memory?: { usedJSHeapSize: number } }).memory;
    return mem ? Math.round(mem.usedJSHeapSize / 1e6) : null;
  });
  console.log(JSON.stringify({ firstNetworkMs, idleRendersPerS, pan, counts, heapMB }));
});
