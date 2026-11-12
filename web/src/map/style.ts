// Base style: our background, a self-hosted Protomaps basemap, and hillshade + 3D terrain from the API's
// Terrain-RGB tiles (the same GLO-30 elevation the propagation model uses). Everything is same-origin, so the
// map works with no internet access. Missing pieces degrade to a flat, dark map instead of failing.
import { layers as basemapLayers, namedFlavor } from '@protomaps/basemaps';
import maplibregl, { type LayerSpecification, type SourceSpecification, type StyleSpecification } from 'maplibre-gl';
import { Protocol } from 'pmtiles';
import { palette } from '../palette';

export interface TerrainMeta {
  encoding: 'mapbox';
  tile_size: number;
  minzoom: number;
  maxzoom: number;
  bounds: [number, number, number, number];
  dem: string;
}

const COPERNICUS =
  'Elevation: Copernicus WorldDEM-30 © DLR e.V. 2010-2014 and © Airbus Defence and Space GmbH 2014-2018, ' +
  'provided under COPERNICUS by the European Union and ESA';
const OSM = 'Basemap: © <a href="https://openstreetmap.org/copyright">OpenStreetMap</a> contributors, Protomaps';

let protocolAdded = false;

// Checks the archive's magic bytes, not just the status: SPA servers answer unknown paths with index.html (200).
async function isPmtiles(url: string): Promise<boolean> {
  try {
    const r = await fetch(url, { headers: { Range: 'bytes=0-6' } });
    return r.ok && (await r.text()).startsWith('PMTiles');
  } catch {
    return false;
  }
}

async function terrainMeta(region: string): Promise<TerrainMeta | null> {
  try {
    const r = await fetch(`/api/v1/regions/${encodeURIComponent(region)}/terrain.json`);
    return r.ok ? ((await r.json()) as TerrainMeta) : null;
  } catch {
    return null;
  }
}

// Protomaps "dark" with its ground colours pulled toward our near-black, so data stays the brightest thing.
// No symbol layers: labels would need a glyph server, and the network data is what should be read.
function basemap(): LayerSpecification[] {
  const flavor = { ...namedFlavor('dark'), background: palette.void, earth: '#0a0e13', water: '#03111a' };
  return basemapLayers('basemap', flavor).filter((l) => l.type !== 'symbol' && l.id !== 'background');
}

export interface BaseStyle {
  style: StyleSpecification;
  terrain: TerrainMeta | null;
  attribution: string[];
}

export async function baseStyle(region: string): Promise<BaseStyle> {
  const origin = window.location.origin;
  const basemapUrl = `/basemap/${encodeURIComponent(region.toLowerCase())}.pmtiles`;
  const [hasBasemap, terrain] = await Promise.all([isPmtiles(basemapUrl), terrainMeta(region)]);

  const sources: Record<string, SourceSpecification> = {};
  const layers: LayerSpecification[] = [{ id: 'bg', type: 'background', paint: { 'background-color': palette.void } }];
  const attribution: string[] = [];

  if (hasBasemap) {
    if (!protocolAdded) {
      maplibregl.addProtocol('pmtiles', new Protocol().tile);
      protocolAdded = true;
    }
    sources.basemap = { type: 'vector', url: `pmtiles://${origin}${basemapUrl}` };
    layers.push(...basemap());
    attribution.push(OSM);
  }

  if (terrain) {
    const dem = {
      type: 'raster-dem' as const,
      tiles: [`${origin}/api/v1/regions/${encodeURIComponent(region)}/terrain/{z}/{x}/{y}.png`],
      tileSize: terrain.tile_size,
      minzoom: terrain.minzoom,
      maxzoom: terrain.maxzoom,
      bounds: terrain.bounds,
      encoding: terrain.encoding,
    };
    // Separate sources for 3D terrain and hillshade, as MapLibre's terrain example does: each keeps its own tile cache.
    sources['terrain-3d'] = dem;
    sources['terrain-shade'] = dem;
    layers.push({
      id: 'hillshade',
      type: 'hillshade',
      source: 'terrain-shade',
      paint: {
        'hillshade-shadow-color': '#000000',
        'hillshade-highlight-color': '#4a5868',
        'hillshade-accent-color': '#0b1016',
        'hillshade-exaggeration': 0.75,
        'hillshade-illumination-direction': 315,
      },
    });
    attribution.push(COPERNICUS);
  }

  return { style: { version: 8, sources, layers }, terrain, attribution };
}
