// Smoke tests: the console against the Go API serving its own test fixtures (api/testdata/summaries/region=SEA).
// They check the 30-second story still works end to end: map + terrain, layers, link profile, node what-if,
// and that nothing leaves the origin or breaks the CSP.
import { expect, test, type Page } from '@playwright/test';

type Win = Window & { __map?: import('maplibre-gl').Map };

const errors: string[] = [];
const external: string[] = [];
const failed: string[] = [];

// Failures the contract defines as normal: terrain tiles outside the region's tile set (404), no basemap
// configured (404), and no Prometheus in the test setup (/status 503).
const EXPECTED_FAILURES = [/\/api\/v1\/regions\/[A-Z]+\/terrain\/\d+\/\d+\/\d+\.png$/, /\/basemap\/[a-z0-9-]+\.pmtiles$/, /\/api\/v1\/status$/];

test.beforeEach(async ({ page, baseURL }) => {
  errors.length = 0;
  external.length = 0;
  failed.length = 0;
  const origin = new URL(baseURL!).origin;
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('response', (r) => {
    if (r.status() >= 400 && !EXPECTED_FAILURES.some((re) => re.test(new URL(r.url()).pathname))) failed.push(`${r.status()} ${r.url()}`);
  });
  page.on('request', (r) => {
    const u = r.url();
    if (/^https?:/.test(u) && new URL(u).origin !== origin) external.push(u);
  });
  await page.goto('/');
  await page.waitForFunction(() => {
    const m = (window as Win).__map;
    // Camera still too: the initial 3D tilt animates, and a point measured mid-animation misses on click.
    return !!m?.getLayer('nodes') && !m.isMoving() && m.areTilesLoaded() && m.queryRenderedFeatures({ layers: ['nodes'] }).length > 0;
  });
});

test.afterEach(() => {
  // CSP violations and script errors show up as console errors. "Failed to load resource" lines carry no URL, so
  // HTTP failures are checked by URL in `failed` instead, against the contract's expected ones.
  expect(errors.filter((e) => !e.startsWith('Failed to load resource')), 'console errors').toEqual([]);
  expect(failed, 'unexpected HTTP failures').toEqual([]);
  expect(external, 'requests leaving the origin').toEqual([]);
});

// Screen point on a feature, chosen so a click there selects it, so a test clicks like a user.
async function pointOf(page: Page, layer: string, prop: string, value: string | boolean) {
  await page.waitForFunction(() => !(window as Win).__map!.isMoving());
  const p = await page.evaluate(([layer, prop, value]) => {
    const m = (window as Win).__map!;
    // Endpoints from the source data: rendered lines come back cut into tile-sized pieces.
    const nodes = (m.getSource('nodes')!.serialize() as { data: GeoJSON.FeatureCollection<GeoJSON.Point> }).data.features;
    const at = (id: string) => nodes.find((n) => n.properties?.id === id)?.geometry.coordinates;
    const ends = layer === 'nodes'
      ? nodes.filter((n) => n.properties?.[prop] === value).map((n) => n.geometry.coordinates).slice(0, 1)
      : String(value).split('|').map(at);
    const [a, b] = [ends[0], ends.at(-1)];
    if (!a || !b) return null;
    const r = m.getCanvas().getBoundingClientRect();
    for (const t of [0.5, 0.4, 0.6, 0.3, 0.7, 0.2, 0.8]) {
      const q = m.project([a[0]! + (b[0]! - a[0]!) * t, a[1]! + (b[1]! - a[1]!) * t]);
      const hits = m.queryRenderedFeatures(q, { layers: ['nodes', 'links-hit'] });
      // Nodes win a click (as in the app), so for a link pick a point on it with no node icon on top.
      const node = hits.find((h) => h.layer.id === 'nodes');
      const ok = layer === 'nodes' ? node?.properties[prop] === value
        : !node && hits.some((h) => h.layer.id === layer && h.properties[prop] === value);
      if (ok) {
        return { x: q.x + r.left, y: q.y + r.top };
      }
    }
    return null;
  }, [layer, prop, value] as const);
  expect(p, `${layer} ${String(value)}: an unobstructed point on screen`).not.toBeNull();
  return p!;
}

test('loads the region, terrain and the status strip', async ({ page }) => {
  await expect(page.locator('.strip .region')).toHaveText('SEA');
  // No Prometheus in the test setup: the strip must say so instead of showing numbers.
  await expect(page.locator('.strip')).toContainText('metrics unavailable');
  const state = await page.evaluate(() => {
    const m = (window as Win).__map!;
    return { terrain: m.getTerrain()?.source, pitch: Math.round(m.getPitch()) };
  });
  expect(state).toEqual({ terrain: 'terrain-3d', pitch: 55 });
  await expect(page.getByText('Copernicus WorldDEM-30', { exact: false })).toBeVisible();
});

test('layer toggles hide and show map layers', async ({ page }) => {
  const vis = () => page.evaluate(() => (window as Win).__map!.getLayoutProperty('placement', 'visibility'));
  await page.getByLabel('Repeater candidates').uncheck();
  await expect.poll(vis).toBe('none');
  await page.getByLabel('Repeater candidates').check();
  await expect.poll(vis).toBe('visible');
});

test('predicted coverage draws cells and the margin slider hides weak ones', async ({ page }) => {
  const shown = () => page.evaluate(() => {
    const m = (window as Win).__map!;
    return { vis: m.getLayoutProperty('coverage', 'visibility'), cells: m.queryRenderedFeatures({ layers: ['coverage'] }).length };
  });
  await page.getByLabel('Predicted coverage').check();
  await expect.poll(async () => (await shown()).vis).toBe('visible');
  await expect.poll(async () => (await shown()).cells).toBeGreaterThan(0);
  await expect(page.locator('.legend')).toContainText('Predicted coverage');
  // At the slider's top every cell must clear +20 dB over the floor; the filter is applied to the layer itself.
  await page.getByRole('slider', { name: /Covered at/ }).press('End'); // keyboard: jump to the slider's maximum (20 dB)
  await expect.poll(() => page.evaluate(() => JSON.stringify((window as Win).__map!.getFilter('coverage')))).toContain('20');
  // Rendered features lag a filter change by one frame, so poll until the map has redrawn.
  await expect.poll(() => page.evaluate(() =>
    (window as Win).__map!.queryRenderedFeatures({ layers: ['coverage'] }).every((f) => (f.properties.margin as number) >= 20))).toBe(true);
});

test('2D view drops terrain and flattens the camera', async ({ page }) => {
  await page.getByRole('radio', { name: '2D' }).click();
  await expect.poll(() => page.evaluate(() => {
    const m = (window as Win).__map!;
    return [m.getTerrain(), Math.round(m.getPitch())];
  })).toEqual([null, 0]);
});

test('link inspector shows the terrain profile', async ({ page }) => {
  const p = await pointOf(page, 'links-hit', 'key', '04080AC9|3491927B');
  await page.mouse.click(p.x, p.y);
  const inspector = page.getByRole('complementary', { name: 'Link inspector' });
  await expect(inspector).toContainText('SEA-RPT-09');
  const chart = inspector.getByRole('img', { name: /Terrain profile/ });
  await expect(chart).toBeVisible();
  await expect(inspector).toContainText('Observed SNR');
  await page.keyboard.press('Escape');
  await expect(inspector).toBeHidden();
});

test('node inspector answers "what if this node fails"', async ({ page }) => {
  const p = await pointOf(page, 'nodes', 'spof', true);
  await page.mouse.click(p.x, p.y);
  const inspector = page.getByRole('complementary', { name: 'Node inspector' });
  await expect(inspector).toContainText('Single point of failure');
  await inspector.getByRole('button', { name: 'What if this node fails?' }).click();
  await expect(inspector).toContainText('Reachable pairs lost');
});
