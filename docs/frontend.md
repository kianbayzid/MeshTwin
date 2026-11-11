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
| 5 | Node inspector, parameter panel, status strip on live metrics | — |
| 6 | Performance, Helm, Playwright in CI, README media | — |

## Run
```sh
cd web && npm install
npm run dev              # http://localhost:5173, fixture data
npm run build            # tsc (strict) + vite build into web/dist
npm run check:contrast   # fails if any palette text pairing is below WCAG AA 4.5:1
```

With real terrain (dev): build the tiles once, run the API on them; Vite proxies `/api` to it.
```sh
cd analytics && uv run meshtwin-analytics fetch-dem --bbox 47.3,-122.6,47.9,-121.8 --dem-dir .dem
uv run meshtwin-analytics terrain-tiles --region SEA --dem-dir .dem --bbox 47.3,-122.6,47.9,-121.8 --out .out/terrain
cd ../api && go run ./cmd/api -summaries-dir ../analytics/.out/terrain -listen 127.0.0.1:8080
sh web/scripts/fetch-basemap.sh   # optional, once, online: OSM basemap into web/public/basemap/sea.pmtiles
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
  the UI is typed and laid out against shapes the API really emits. Phase 3 replaces `api/mock.ts` with `fetch`.
  The status strip shows "Sample data" while this is the case.
- **Map engine:** MapLibre GL JS 5 with native layers (deck.gl decision: see Phase 3).
- **Serving (planned for Phase 6):** embed `web/dist` in the Go API with `embed.FS`, replacing the Leaflet page.
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
- **Offline check:** Playwright records every request during load, 3D, exaggeration and 2D: zero leave the origin.


The strip is designed around metrics that already exist (wired in Phase 5):

| Strip item | Source |
|------------|--------|
| Ingest | `rate(meshtwin_ingest_messages_total)` |
| Sync lag | `meshtwin_ingest_spool_oldest_age_seconds` |
| Disk buffer | `meshtwin_ingest_spool_bytes` / `meshtwin_ingest_spool_max_bytes`; amber above 50 % or when storage is down |
| Storage | `meshtwin_ingest_storage_up` |
| Data from | `meta.generated_at` of the topology summary; amber after 2 h (two missed hourly runs) |

## Known gaps
- The coverage layer stays disabled until a coverage raster exists. No contour lines (optional in the brief; skipped).
- Edge tiles are 0 m outside `bounds`, so the relief ends in a step at the box edge (visible when zoomed out).
- Links are draped over the terrain, so long links follow the ground (see the Phase 3 deck.gl decision).
- The time window is what the job produced; the API has no window parameter. Summaries aren't re-fetched
  while the page is open (Phase 5).
- Model parameters are read-only: the API exposes antenna height, model and DEM, but not frequency.
- The JS bundle is 1.5 MB minified (367 kB gzip), mostly MapLibre. Phase 6 measures it and splits it if needed.

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
