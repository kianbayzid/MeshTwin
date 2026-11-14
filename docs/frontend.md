# MeshTwin Console (`/web`)

A browser console for the summaries the API serves. It has to tell one story in 30 seconds:
real terrain, what the radio model predicted, what the mesh actually did, and where to put the next repeater.

## Phase status
| Phase | Scope | State |
|-------|-------|-------|
| 1 | Design tokens, app shell, layout on fixture data | done (2026-11-11) |
| 2 | DEM → terrain-RGB tiles, PMTiles basemap, offline 3D map | done (2026-11-11) |
| 3 | Layers wired to the real API | done (2026-11-11) |
| 4 | Link inspector: terrain profile + Fresnel zone (new endpoint) | done (2026-11-11) |
| 5 | Node inspector, parameter panel, status strip on live metrics | done (2026-11-11) |
| 6 | Performance, Helm, Playwright in CI, README media | done (2026-11-11) |
| 7 | Predicted coverage layer (new endpoint) | done (2026-11-12) |
| 8 | Whole-US map and city picker (no new endpoint) | done (2026-11-13) |

## Run
```sh
cd web && npm install
npm run dev              # http://localhost:5173 (needs the API on 127.0.0.1:8080, see below)
npm run build            # tsc (strict) + vite build into web/dist
npm test                 # self-checks: palette contrast (WCAG AA), profile chart geometry, link picking
npm run build:e2e        # build with the Playwright test hook into web/dist-e2e
npm run e2e              # smoke tests against a running API (E2E_BASE_URL; PW_CHANNEL=chrome to use local Chrome)
```

From the repo root: `make web` (npm ci + test + build) and `make e2e` (builds the API, serves `web/dist-e2e` with
the API's test fixtures and runs the smoke suite; `E2E_LISTEN=127.0.0.1:<port>` if 8080 is taken). On Windows, stop a
running Vite dev server first: `npm ci` can't replace `esbuild.exe` while it's in use. In Helm, the basemap is off
by default; `api.basemap.hostPath` mounts a directory of `*.pmtiles` read-only (k3d shortcut; a ReadOnlyMany PVC on a
real cluster).

With real terrain (dev): build the tiles once, run the API on them; Vite proxies `/api` to it.
```sh
cd analytics && uv run meshtwin-analytics fetch-dem --bbox 47.3,-122.6,47.9,-121.8 --dem-dir .dem
uv run meshtwin-analytics terrain-tiles --region SEA --dem-dir .dem --bbox 47.3,-122.6,47.9,-121.8 --out .out/terrain
cd ../api && go run ./cmd/api -summaries-dir ../analytics/.out/terrain -listen 127.0.0.1:8080
sh web/scripts/fetch-basemap.sh   # optional, once, online: OSM basemap into web/public/basemap/sea.pmtiles
sh web/scripts/fetch-basemap.sh us  # optional, once, online: US overview (z9, ~180 MB) into web/public/basemap/us.pmtiles
```

## Decisions (Phase 1)
- **Vite + React 18 + TypeScript strict, plain CSS.** No UI kit and no Tailwind: the console has about ten
  components, and CSS custom properties are enough for one palette. `noUncheckedIndexedAccess` is on.
- **One palette file:** `web/src/palette.ts`. `applyPalette()` writes it to CSS custom properties at startup, and
  the map's paint expressions import the same object, so CSS and WebGL can't drift apart.
  Meaning: cyan = observed / live, magenta = predicted-only or surprising, lime = healthy / recommended,
  amber = warning / single point of failure. Selection is drawn in ink (white), not a data colour.
- **Never colour alone:** node role is shape (diamond repeater, circle companion, square room, triangle
  sensor or unknown), and observers are filled cyan. Uncertain links (confidence < 0.8) are dashed. SPOFs get a
  ring plus a text callout in the inspector. Warnings in the status strip carry a ▲ and words.
- **Canvas-drawn icons and rank badges** (`web/src/map/icons.ts`) instead of a sprite and glyph server. The map
  needs nothing from the network, which Phase 2's offline requirement depends on.
- **Fonts are self-hosted** through `@fontsource` (Barlow Semi Condensed for labels, JetBrains Mono for values),
  bundled by Vite. No Google Fonts request.
- **Mock data is the API's own test fixtures** (`api/testdata/summaries/region=SEA`, copied to `web/src/mock/`), so
  the UI is typed and laid out against shapes the API really emits. (Phase 3 replaced them with `fetch`.)
- **Map engine:** MapLibre GL JS 5 with native layers (deck.gl decision: see Phase 3).
- **Serving (done in Phase 6):** embed `web/dist` in the Go API with `embed.FS`, replacing the Leaflet page.
  The API already has a CSP, ETags and a static route, so this avoids a second container and a second origin
  (no CORS). Cost: the API image changes when the UI does.

## Decisions (Phase 2)
- **Terrain is the model's terrain.** `meshtwin-analytics terrain-tiles` samples the same GLO-30 mosaic and the
  same bilinear `GridTerrain.elevation_m` the propagation model calls, at every Web Mercator pixel centre, and
  writes Mapbox Terrain-RGB PNGs (z8–12; z12 ≈ 26 m/pixel at 47.6° N, close to GLO-30's 30 m). No GDAL,
  rasterio or rio-rgbify: numpy + `imagecodecs.png_encode`, which the project already had. The job refuses to
  run without DEM tiles rather than publish flat terrain.
- **The API serves the tiles** (`/api/v1/regions/{region}/terrain.json` + `/terrain/{z}/{x}/{y}.png`, see the contract)
  from the same store as the summaries, after checking the PNG header and a 1 MB cap. No tile cache in the API
  (tiles are many; clients cache for 1 h and revalidate by ETag).
- **Basemap:** a Protomaps PMTiles extract of the region, served as a static file next to the app, read by the
  `pmtiles` protocol with HTTP Range requests. Style = `@protomaps/basemaps` "dark" with the ground recoloured
  to our near-black, **without label layers** (labels need a glyph server; the network data is what should be read).
  The file is fetched once by `web/scripts/fetch-basemap.sh` and not committed (size, ODbL).
- **Graceful degradation:** no terrain tiles → flat map and the exaggeration slider is disabled; no basemap → hillshade
  only. The basemap check reads the archive's magic bytes, because dev/SPA servers answer a missing file with
  `index.html` and status 200.
- **2D/3D:** 2D removes terrain (`setTerrain(null)`) and flattens the camera; 3D drapes nodes and links over the DEM.
  Hillshade and 3D terrain use separate sources over the same tiles, as MapLibre's terrain example does.
- **No external terrain fallback.** It wasn't needed, so there is no option to turn on.
- **Offline check:** a manual Playwright session recorded every request during load, 3D, exaggeration and 2D: zero
  left the origin. Since Phase 6 the CI smoke suite asserts it on every run.

## Decisions (Phase 3)
- **Links are draped, not arced; no deck.gl.** MapLibre 5.24's line layer has no elevation property (checked
  against the installed style spec), so arcs need deck.gl's ArcLayer or a custom WebGL layer, plus per-endpoint
  `queryTerrainElevation` to sit on the terrain. That is a second renderer and roughly 250 kB gzip for an
  effect the brief lists as optional ("draped over or arcing above"). Draped lines show the same thing: which
  links exist and their colour. Follow-up if wanted: deck.gl `MapboxOverlay` (interleaved) + `ArcLayer`.
- **Each summary loads independently** (`Promise.allSettled`). Topology is required; a missing resilience,
  residuals, surprising or placement summary disables only its layer, and the toggle shows the API's own error
  message. Checked by answering `placement` with a 503 in Playwright.
- **Region comes from `/api/v1/regions`** (first one with a topology); the selector lists all of them. Switching
  region rebuilds the map (`key={region}`), because the terrain source is per region.
- **Predicted-only links** (`links/surprising` → `predicted_strong_but_never_seen`) are a magenta dotted layer,
  off by default (50 links clutter the first view). Not clickable: there is no observed link to inspect.
- **Placement badges never overlap.** Neighbouring cells often tie (six at 0.95 in SEA); the better rank keeps the
  space and the rest appear as you zoom in. Nodes ignore placement so they never hide a candidate.
- **Fixtures removed from `web/`.** The app only renders what the API returns.
- **Display fixes driven by real data:** `fresnel_clearance` is shown as a ratio of the first Fresnel radius
  (−18.75 × F1), not a percentage; a link inside one grid cell shows "under 1 km" instead of "0.0 km".

## Decisions (Phase 4)
- **Profiles are precomputed by the propagation job, not computed in the API.** The Go API stays a read-only,
  validating server of summaries; the radio math lives in one place (Python). The job writes `profiles.json`
  (237 profiles for SEA, 438 KiB compact, 0.06 s extra per run) and the API returns one entry per request,
  indexed once per cache load.
- **One geometry function.** `FsplKnifeEdge.geometry()` computes ground, Earth bulge, ray and Fresnel radius; the
  model's `predict` and the profile export both call it, and a test checks they agree exactly.
- **Privacy: profiles run between grid-cell centres.** An elevation sequence along the exact path is a fingerprint
  that could be matched against the public DEM to find a node, so exact paths never leave analytics. A test moves
  the exact positions within their cells and asserts the profile doesn't change (the verifier broke the rule on
  purpose and the test failed). The difference is small in practice (link 351FC1F9–FDC42289: −18.66 × F1 on the
  grid path vs −18.75 × F1 exact).
- **The chart does no physics.** `web/src/profile.ts` only adds `ground + bulge`, draws the straight mast-to-mast
  line and divides by the given `r1`; the 60 % Fresnel rule marks obstructions. `npm test` runs a self-check.
- **Hand-rolled SVG, no chart library.** One chart, one y-axis (metres), about 150 lines. Hover crosshair + tooltip,
  arrow keys for keyboard users, a text summary as the accessible name and caption, and a key in which every
  meaning has a line style as well as a colour (amber solid = blocks the ray, amber dotted = inside 60 % of the zone).
- **Known limit:** the chart arrays are capped at 256 points, so on paths over ~7.6 km a very narrow obstacle can be
  thinner than one chart sample; the published clearance and diffraction come from the full-resolution path.

## Decisions (Phase 5)
- **Status strip from Prometheus, through the API.** The browser can't scrape the ingester (another origin, and a
  rate needs two scrapes), so the API runs six fixed PromQL instant queries against `PROMETHEUS_URL` and serves
  `/api/v1/status`, cached 10 s. Nothing from the request reaches Prometheus. Without a URL the endpoint is
  `503 not_ready` and the strip says "metrics unavailable" instead of showing numbers. The sample values are gone.

  | strip item | query |
  |---|---|
  | Ingest | `sum(rate(meshtwin_ingest_messages_total{result="ok"}[5m]))` |
  | Sync lag | `max(meshtwin_ingest_spool_oldest_age_seconds)`; amber above 5 min |
  | Disk buffer | `sum(meshtwin_ingest_spool_bytes)` / `sum(meshtwin_ingest_spool_max_bytes)` |
  | Storage | `min(meshtwin_ingest_storage_up)`, true when 1 (compared in Go: PromQL `==` filters) |
  | Broker | `min(meshtwin_ingest_mqtt_connected)`, true when 1 |
  | Data from | `meta.generated_at` of the topology summary; amber after 2 h |
- **"The DDIL buffer is filling" is one rule:** storage down, or the spool past 50 %. Then the meter turns amber, gets
  a slow glow (off under reduced motion) and the words "▲ filling"; the storage item says "buffering to disk".
- **Polling, not push.** Status every 15 s, regions every 60 s, both paused while the tab is hidden. When a region's
  `updated_at` changes, the summaries are swapped in place: the map keeps its camera and selection (checked in a
  manual Playwright run with a fake clock: no reload while unchanged, one reload on a new version, bearing kept).
- **Filters stay client-side, debounced 150 ms.** The API's `min_confidence` does the same filtering, so a refetch
  per slider tick would only add latency; the debounce stops the map rebuilding 300+ features on every tick.
- **Parameter panel is read-only for model parameters.** The API is read-only: it reports antenna height (placement),
  model and DEM (residuals); frequency and k-factor come with each terrain profile, so the chart caption shows them.
  The time window is the job's; the API has no window parameter.
- **Node packet stats are derived from its links** (receptions on its links, measured links, median last-hop SNR):
  the contract has no per-node packet counter, and adding one isn't worth a schema change.
- **"Can't reach the API"** now also covers a dev proxy or gateway answering 5xx with a non-JSON body (the
  confusing "HTTP 500" seen when the API process was stopped).

## Decisions (Phase 6)
- **Served by the Go API from the binary.** `web/dist` is embedded at image build time (`--build-context web=web`, a
  Node stage that never reaches the distroless image; API image 22.2 → 25.1 MB). `/` is `no-cache`, `/assets/*` are
  content-hashed and `immutable`. The CSP allows only `'self'`, plus `blob:` for MapLibre's workers and images.
  The strict CSP immediately caught Vite inlining small font subsets as `data:` URIs; the build now never inlines
  fonts rather than the CSP allowing `data:` fonts.
- **Idle map = zero frames.** The SPOF pulse was a paint property updated every animation frame, which kept the 3D map
  re-rendering ~45 times a second with nothing moving (found because the smoke tests could never see the map idle).
  It is now a handful of DOM markers animated by CSS on the compositor; the static ring stays a map layer. Measured:
  45 → 0 renders/s idle.
- **Click picks the nearest line, not the topmost.** In dense clusters the 12 px click areas of 3–4 links overlap, and
  the topmost won, so some short links could not be selected at all at the default zoom. `src/map/pick.ts` picks the
  line closest to the cursor (self-checked by `npm test`).
- **Profiles are sharded** (contract change). At 5× the fixture, one `profiles.json` was 9.1 MB, over the API's 8 MB
  cap, so every profile lookup would have failed. 16 shard files by the first hex digit of the smaller id; a bad shard
  only affects its own lookups. The shards are uneven (the smaller of two hex digits leans low): the busiest reaches
  the cap at about 19,000 profiles. An XOR rule would spread them evenly; recorded as a follow-up.
- **Playwright smoke tests in CI** (`web/e2e/smoke.spec.ts`): the real API serving the e2e build and its committed
  fixtures. They fail on any console error (so any CSP violation), any request leaving the origin, and any HTTP
  failure other than the contract's expected ones (terrain tiles outside the tile set, no basemap, no Prometheus).
  The e2e build (`vite --mode e2e`) exposes the map to tests; production builds drop that line.
- **Performance probe** (`web/e2e/perf.spec.ts`, `PERF=1`, not a CI gate): time to a drawn network, idle renders,
  frame times while panning, heap. Numbers in `docs/benchmarks.md`.
- **Helm:** `PROMETHEUS_URL` points the status strip at the chart's Prometheus; the basemap is off by default and
  `api.basemap.hostPath` mounts a `*.pmtiles` directory read-only (k3d shortcut).
- **README media are reproducible:** `web/scripts/capture-media.mjs` takes the screenshots and the GIF (frames as
  screenshots, encoded by the system ffmpeg).

## Decisions (Phase 7)
- **What the layer answers:** "if I stood here with a handheld, would the model expect me to reach a repeater?" Best
  predicted SNR per 0.01° cell from any repeater or room server (handset 2 m, repeater 10 m), computed nightly by the
  placement job on the same grid, DEM and model (`GET /regions/{region}/coverage`, contract in `docs/api-contract.md`).
- **Privacy:** the job places each repeater at its snapped cell centre before predicting. A smooth signal field around a
  raw position would give that position away far below 1 km; from the cell centre it reveals nothing the map doesn't.
- **"Covered" is a margin the reader picks**, 0–20 dB over the decode floor (default 5 dB, the fade margin placement
  asks of a new link). The cut is a layer filter, so the slider never rebuilds the data. Cells under it aren't drawn:
  "not covered" reads as plain terrain, not as one more colour to decode.
- **Colour:** magenta (the palette's "predicted"), one hue from dim at the chosen margin to full at +30 dB, where a
  radio can't tell the difference any more; beside a repeater the model predicts far more than any receiver reports.
  30 % opacity under every network layer: coverage is context, the links are the story. Hovering a cell shows its
  numbers, so the layer never relies on colour alone.
- **One polygon per cell** (`src/map/coverage.ts`, self-checked by `npm test`): ~1.5k cells for SEA, exact cell edges,
  hover via the map's own hit testing. A canvas image would scale to the contract's 250k-cell cap; recorded as a
  shortcut for when a region needs it.
- **Accessibility fix found by the new test:** a `<label>` names only its first labelable child, which in the slider
  component was the `<output>` readout, so every slider was announced as just "slider". Sliders now carry their own
  name, and the smoke test drives the coverage slider by keyboard, by that name.

## Decisions (Phase 8)
- **What it answers:** "where is MeshCore active, and what does MeshTwin know there?" One **Area** picker with two
  groups: *MeshTwin data* (regions the API has; picking one loads it as before) and *MeshCore cities (LetsMesh)*
  (100 US cities; picking one flies the camera there and says plainly that MeshTwin has no data for it yet).
  *Whole US* goes back to the overview. With no region at all, the console now shows the US map instead of an error.
- **No dummy nodes.** Cities are camera targets only. The map draws only what the API serves.
- **Which cities:** the LetsMesh analyzer's own region list (IATA code, city, a count badge), snapshot 2026-11-13,
  US regions with a count of 4+ (100 of 429). LetsMesh regions are IATA codes, like MeshTwin's, so a region with data
  shows under its city name. Coordinates from OurAirports (public domain): the airport, which can be up to ~40 km from
  downtown (Dulles), fine at metro zoom. Sources and terms: `docs/data-sources.md`, Console city list.
- **Built once, committed, never fetched:** `scripts/build-cities.ts` turns a saved analyzer page and `airports.csv`
  into `src/map/cities.ts`; `scripts/check-cities.ts` (in `npm test`) checks unique codes, every city inside the US
  basemap, and that SEA and MCO are present. The list goes stale; re-run by hand.
- **Two basemaps, one style:** `us.pmtiles` (contiguous US, Alaska, Hawaii; max zoom 9, 182 MB) under the region's own
  archive (z14). Layer ids carry the source as a prefix so both fit in one style; the region draws last and covers the
  overzoomed overview inside its box. z9 was the cut: z10 is ~385 MB and z11 ~835 MB for the contiguous US alone.
  Outside a region, streets get blurry past zoom ~11, and there is no terrain (terrain tiles exist only per region).
- **Flying, not reloading:** a city pick moves the camera (`flyTo`, instant under reduced motion) and leaves the
  current region's data loaded; a region pick remounts the map on that region, as before. The map ignores a pick made
  before it mounted, so changing region never replays an old flight.

## Reading the SEA demo data honestly
The SEA summaries come from the synthetic fixture (`testdata/parquet/sea-seed1-2h-hash1`) run through the real
pipeline **with** the GLO-30 DEM (analytics lane, 2026-11-11). The generator's link model ignores terrain: path loss
uses a distance exponent of 7.5 that "stands in for ground clutter and terrain" (`ingest/internal/synth/network.go`).
So the residual RMSE falling from 30.7 dB (flat) to 19.1 dB (GLO-30) is the DEM's diffraction loss soaking up
that exponent, **not** evidence that the terrain model explains reality: residual correlates with diffraction loss at
r = +0.89, and line-of-sight links fit worst. The map shows "model vs synthetic network" until a real capture exists.
Also from the synthetic data: two repeaters sit at ~0 m (likely in Puget Sound; the generator has no land check).

## Known gaps
- Coverage is drawn as cells, not contour lines (optional in the brief; skipped). On the SEA demo data nearly every
  cell is predicted far above the floor, so the layer shows no gaps there (see `docs/benchmarks.md`, Console Phase 7).
- The city list is a 2026-11-13 snapshot; the count badge's meaning (observers) is UNVERIFIED. No in-app search box:
  the picker is a native `<select>` (type-ahead works), enough for 100 entries.
- Edge tiles are 0 m outside `bounds`, so the relief ends in a step at the box edge (visible when zoomed out).
- Links are draped over the terrain, so long links follow the ground (see the Phase 3 deck.gl decision).
- The time window is what the job produced; the API has no window parameter.
- The JS bundle is 1.3 MB minified (361 kB gzip), mostly MapLibre, in one chunk. Not split: MapLibre is needed for the
  first paint, so a split would only help caching across deploys.
- Map features (nodes, links) are reachable by mouse only; a keyboard-accessible list of nodes and links is not built.
- At 5× scale the resilience layer is empty (0 SPOFs: the denser synthetic network has none), so SPOF rendering at
  scale is untested.

## Interview questions (Phase 1)
1. **Why put the palette in TypeScript instead of a CSS file?** The map's paint expressions run in WebGL and
   can't read CSS variables, so one TS object feeds both, and the contrast check runs against the same values.
2. **How does the UI avoid leaking locations?** It only ever has what the API sends: 0.01° cell centres. Nodes
   with `location: null` aren't drawn, and the inspector says "~1 km grid cell" instead of printing coordinates.
3. **Why draw icons on a canvas?** Symbol text needs a glyph server, and a sprite needs a hosted file. Drawing 18
   small images at load keeps the app self-contained offline, and the shapes stay in code with the palette.

## Interview questions (Phase 2)
1. **How do you know the 3D terrain matches what the radio model saw?** Both read the same GLO-30 files through
   the same `GridTerrain` object and the same bilinear interpolation; the tile job just samples it on a Web
   Mercator grid. A test renders a synthetic plane and checks every pixel within 0.05 m, and spot checks on real
   tiles match known heights (Cougar Mountain 469 m against a 486 m summit, at 26 m pixels).
2. **Why serve tiles through the API instead of a tile server?** They live in the same store as the summaries and
   go through the same validation path (PNG header, size cap, strict integer z/x/y, ETag), with no new service to
   run. At 161 tiles / 8 MB per region a tile server would be machinery without a job.
3. **What breaks if the network is down?** Nothing at runtime: basemap, terrain, fonts and icons are same-origin.
   Only two one-time setup steps go online, `fetch-dem` and `fetch-basemap.sh`, and both cache their result.

## Interview questions (Phase 3)
1. **Why not deck.gl for arcs?** MapLibre lines can't leave the ground, so arcs mean a second WebGL renderer kept in
   sync with the terrain, about 250 kB more, and endpoint heights from `queryTerrainElevation`. The brief allowed
   draping, and draped lines carry the same information. I'd add arcs only if a user test showed long links being
   misread.
2. **What happens when one analytics job hasn't run?** Each summary is fetched separately; only topology is
   required. The missing layer is disabled and shows the API's message (e.g. `not_ready`), so the rest of the map
   still works. Tested by forcing a 503 on placement.
3. **The residual RMSE dropped from 31 to 19 dB with the DEM. Is the model good?** Not shown by this data: the
   network is synthetic and its generator ignores terrain, so the DEM is fitting the generator's distance exponent.
   The tell is that line-of-sight links fit worst. It needs a real capture before the number means anything.

## Interview questions (Phase 4)
1. **Why not compute the profile on request in Go?** The API would need the DEM and a second implementation of the
   geometry, which could drift from the Python model. Precomputing all 237 profiles costs 0.06 s per job and
   438 KiB, and keeps the API a validator and server of files.
2. **Why is the chart's path not the exact link path?** Privacy: a ground profile along the exact path identifies the
   endpoints when matched against the public DEM. The grid path changes clearance by about 0.1 Fresnel radius on
   the test link; the inspector shows the exact-path model numbers next to it and says which is which.
3. **How do you know the chart and the model agree?** They share one geometry function, tested to reproduce `predict`
   exactly; the chart's own arithmetic (terrain + bulge, mast-to-mast ray, clearance ÷ r1) has a self-check, and the
   worst clearance it shows (−18.66 × F1) equals the job's published value for the same path.

## Interview questions (Phase 5)
1. **Why does the API query Prometheus instead of the browser scraping the ingester?** A rate needs two samples over
   time and every ingester replica; Prometheus already has both. The API runs fixed queries (no injection surface),
   caches them for 10 s so open consoles can't load Prometheus, and degrades to "metrics unavailable".
2. **How does the UI show the DDIL buffer filling?** One rule (storage down or spool > 50 %) drives an amber meter, a
   ▲ "filling" label and the storage text, so it reads without colour and without motion.
3. **How do new job results reach an open console without a reload?** A 60 s poll of `/regions` compares
   `updated_at`; only a change fetches the summaries, which are swapped into the existing map sources, so the
   camera and the selected link survive. Hidden tabs don't poll.

## Interview questions (Phase 8)
1. **Why not show nodes for all those cities?** MeshTwin only draws what its own pipeline measured; the city list
   is where to look, not data. Pulling nodes from someone else's map needs that source's permission
   (`docs/data-sources.md`), and placeholder nodes would make the map lie.
2. **Why airports for city coordinates?** LetsMesh regions *are* airport codes, so OurAirports joins them exactly,
   is public domain, and needs no geocoding service. The cost (an airport can be 40 km out) doesn't matter at metro zoom.
3. **Why a z9 US basemap instead of full detail?** Size: z9 is 182 MB, z14 for the whole US would be tens of GB. Detail
   matters only where there's data, and those regions keep their own z14 archive drawn on top.

## Interview questions (Phase 7)
1. **Why compute coverage from the snapped position and not the real one?** The output is a continuous field: with a
   raw position, the cells with the strongest signal and the shape of the falloff point back to the exact spot, well
   under the 1 km the project promises. From the cell centre the field reveals only the cell, which the map already shows.
   A test moves a repeater around inside its cell and checks the raster doesn't change.
2. **Why does the SEA map show no coverage gaps, and is that a bug?** It's the data: 30 repeaters in about 40 × 40 km
   on a synthetic network, with a single-knife-edge model that is optimistic over several ridges. The analytics lane
   flagged it before the UI was built; the benchmark records it. Real data or a Deygout model would show gaps.
3. **Why let the user choose the margin instead of fixing a threshold?** "Covered" depends on what you need: a
   messaging user tolerates retries, a sensor network wants fade margin. The model gives an SNR; the threshold is a
   policy, so it sits in the UI as a filter, and the default (5 dB) matches what placement assumes.

## Interview questions (Phase 6)
1. **What did the performance pass find?** Two real problems, both found by tests rather than by looking: an animation
   that kept the 3D map rendering ~45 frames a second while idle (now 0, via CSS on DOM markers), and a summary file
   that outgrew the API's size cap at 5× scale (now sharded). At 5× (3,790 links drawn) panning holds 60 fps.
2. **What do the smoke tests prove?** The real API binary, real CSP, real build: the region loads with terrain, layers
   toggle, 2D drops terrain, a link click opens its terrain profile, a SPOF's what-if answers. Any console error, any
   request to another origin and any unexpected HTTP failure fails the run, which is how the CSP font problem surfaced.
3. **Why embed the console in the API instead of an nginx container?** One origin (no CORS), one CSP, one image to
   scan and sign, ETags already there. The cost is coupling: a UI change rebuilds the API image (+2.9 MB).
