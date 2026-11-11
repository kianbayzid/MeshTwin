import { useEffect, useRef } from 'react';
import maplibregl, { type ExpressionSpecification, type GeoJSONSource, type Map as MlMap } from 'maplibre-gl';
import 'maplibre-gl/dist/maplibre-gl.css';
import { TOP_CANDIDATES, type Summaries } from '../api/client';
import { bounds, linksGeo, nodesGeo, placementGeo, predictedOnlyGeo, type Filters } from '../map/geojson';
import { addIcons } from '../map/icons';
import type { BaseStyle } from '../map/style';
import { palette } from '../palette';
import type { Layers, Selection, View } from '../state';

interface Props {
  base: BaseStyle;
  data: Summaries;
  layers: Layers;
  filters: Filters;
  view: View;
  selection: Selection;
  resetSignal: number;
  onSelect: (s: Selection) => void;
}

const PITCH_3D = 55;
const INSPECTOR_PX = 380;

// Observed SNR: dim to bright cyan across the SX1262 range we actually see (−20…+14 dB).
const snrColor: ExpressionSpecification = ['case', ['has', 'snr'],
  ['interpolate', ['linear'], ['get', 'snr'], -20, palette.cyanDim, 14, palette.cyan], palette.neutral];
// Residual = observed − predicted. Positive (better than the model) is cyan, negative is magenta.
const residualColor: ExpressionSpecification = ['case', ['has', 'residual'],
  ['interpolate', ['linear'], ['get', 'residual'], -20, palette.magenta, 0, palette.neutral, 20, palette.cyan], palette.line];

// Whole network, or the terrain box when no node has a location yet.
function home(data: Summaries, base: BaseStyle): [[number, number], [number, number]] | undefined {
  const t = base.terrain?.bounds;
  return bounds(data.topology) ?? (t && [[t[0], t[1]], [t[2], t[3]]]);
}

const reducedMotion = (): boolean => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

export function MapView({ base, data, layers, filters, view, selection, resetSignal, onSelect }: Props) {
  const el = useRef<HTMLDivElement>(null);
  const mapRef = useRef<MlMap | null>(null);
  const ready = useRef(false);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  useEffect(() => {
    const map = new maplibregl.Map({
      container: el.current!,
      style: base.style,
      bounds: home(data, base),
      fitBoundsOptions: { padding: 60 },
      attributionControl: { compact: true, customAttribution: base.attribution },
      maxPitch: 75,
    });
    mapRef.current = map;
    if (import.meta.env.DEV) (window as unknown as { __map: MlMap }).__map = map; // for Playwright checks
    map.addControl(new maplibregl.NavigationControl({ visualizePitch: true }), 'top-left');

    let removed = false;
    map.on('load', async () => {
      await addIcons(map, TOP_CANDIDATES);
      if (removed) return; // unmounted while the icon font loaded
      map.addSource('links', { type: 'geojson', data: linksGeo(data.topology, data.residuals, filters) });
      map.addSource('nodes', { type: 'geojson', data: nodesGeo(data.topology, data.resilience) });
      map.addSource('placement', { type: 'geojson', data: placementGeo(data.placement) });
      map.addSource('predicted', { type: 'geojson', data: predictedOnlyGeo(data.topology, data.surprising) });

      // Predicted strong but never observed: magenta, dashed and thin, under the observed links.
      map.addLayer({ id: 'predicted', type: 'line', source: 'predicted',
        paint: { 'line-color': palette.magenta, 'line-width': 1.2, 'line-opacity': 0.7, 'line-dasharray': [1, 2] } });

      // Uncertain links (hop-hash ambiguity) are dashed so confidence never depends on colour alone.
      map.addLayer({ id: 'links', type: 'line', source: 'links', filter: ['>=', ['get', 'confidence'], 0.8],
        layout: { 'line-cap': 'round' }, paint: { 'line-color': snrColor, 'line-width': 1.6, 'line-opacity': 0.85 } });
      map.addLayer({ id: 'links-uncertain', type: 'line', source: 'links', filter: ['<', ['get', 'confidence'], 0.8],
        paint: { 'line-color': snrColor, 'line-width': 1.4, 'line-opacity': 0.85, 'line-dasharray': [2, 2] } });
      // Wide invisible line: makes thin links easy to click.
      map.addLayer({ id: 'links-hit', type: 'line', source: 'links', paint: { 'line-color': '#000', 'line-width': 12, 'line-opacity': 0 } });
      // Selection is drawn in ink (not a data colour) on top of the links, with a soft glow.
      map.addLayer({ id: 'links-selected', type: 'line', source: 'links', filter: ['==', ['get', 'key'], ''],
        layout: { 'line-cap': 'round' },
        paint: { 'line-color': palette.ink, 'line-width': 3 } });
      map.addLayer({ id: 'links-selected-glow', type: 'line', source: 'links', filter: ['==', ['get', 'key'], ''],
        paint: { 'line-color': palette.ink, 'line-width': 10, 'line-blur': 8, 'line-opacity': 0.35 } });

      map.addLayer({ id: 'spof-pulse', type: 'circle', source: 'nodes', filter: ['get', 'spof'],
        paint: { 'circle-radius': 14, 'circle-color': 'transparent', 'circle-stroke-color': palette.amber, 'circle-stroke-width': 1.5, 'circle-stroke-opacity': 0.5 } });
      map.addLayer({ id: 'spof-ring', type: 'circle', source: 'nodes', filter: ['get', 'spof'],
        paint: { 'circle-radius': 10, 'circle-color': 'transparent', 'circle-stroke-color': palette.amber, 'circle-stroke-width': 2 } });
      map.addLayer({ id: 'node-selected', type: 'circle', source: 'nodes', filter: ['==', ['get', 'id'], ''],
        paint: { 'circle-radius': 17, 'circle-color': 'transparent', 'circle-stroke-color': palette.ink, 'circle-stroke-width': 2 } });
      map.addLayer({ id: 'nodes', type: 'symbol', source: 'nodes',
        layout: { 'icon-image': ['get', 'icon'], 'icon-allow-overlap': true, 'icon-ignore-placement': true, 'icon-pitch-alignment': 'viewport' } });
      // Neighbouring cells often tie; badges don't overlap, and the better rank wins the space (shown first on zoom-out).
      map.addLayer({ id: 'placement', type: 'symbol', source: 'placement',
        layout: { 'icon-image': ['concat', 'rank-', ['to-string', ['get', 'rank']]], 'icon-allow-overlap': false, 'symbol-sort-key': ['get', 'rank'] } });

      for (const id of ['links-hit', 'nodes', 'placement']) {
        map.on('mouseenter', id, () => (map.getCanvas().style.cursor = 'pointer'));
        map.on('mouseleave', id, () => (map.getCanvas().style.cursor = ''));
      }
      map.on('click', (e) => {
        const hit = map.queryRenderedFeatures(e.point, { layers: ['nodes', 'links-hit'] });
        const node = hit.find((f) => f.layer.id === 'nodes');
        const link = hit.find((f) => f.layer.id === 'links-hit');
        if (node) onSelectRef.current({ kind: 'node', id: String(node.properties.id) });
        else if (link) {
          const [a, b] = String(link.properties.key).split('|') as [string, string];
          onSelectRef.current({ kind: 'link', a, b });
        } else onSelectRef.current(null);
      });
      ready.current = true;
      map.fire('meshtwin:ready');
    });

    // Slow amber pulse for single points of failure; static ring when the user prefers reduced motion.
    let raf = 0;
    const pulse = (t: number) => {
      if (ready.current && !reducedMotion() && map.getLayoutProperty('spof-pulse', 'visibility') !== 'none') {
        const k = (t % 2400) / 2400;
        map.setPaintProperty('spof-pulse', 'circle-radius', 10 + 14 * k);
        map.setPaintProperty('spof-pulse', 'circle-stroke-opacity', 0.7 * (1 - k));
      }
      raf = requestAnimationFrame(pulse);
    };
    raf = requestAnimationFrame(pulse);

    return () => {
      cancelAnimationFrame(raf);
      removed = true;
      ready.current = false;
      map.remove();
    };
    // Map is created once; data/filters/layers are applied by the effects below.
  }, []);

  // Apply state to the map once it's loaded, and again whenever it changes.
  const whenReady = (fn: (m: MlMap) => void) => {
    const m = mapRef.current;
    if (!m) return;
    if (ready.current) fn(m);
    else m.once('meshtwin:ready', () => fn(m));
  };

  useEffect(() => whenReady((m) => {
    (m.getSource('links') as GeoJSONSource).setData(linksGeo(data.topology, data.residuals, filters));
  }), [data, filters]);

  useEffect(() => whenReady((m) => {
    (m.getSource('nodes') as GeoJSONSource).setData(nodesGeo(data.topology, data.resilience));
    (m.getSource('placement') as GeoJSONSource).setData(placementGeo(data.placement));
    (m.getSource('predicted') as GeoJSONSource).setData(predictedOnlyGeo(data.topology, data.surprising));
  }), [data]);

  useEffect(() => whenReady((m) => {
    const vis = (id: string, on: boolean) => m.setLayoutProperty(id, 'visibility', on ? 'visible' : 'none');
    for (const id of ['links', 'links-uncertain', 'links-hit', 'links-selected', 'links-selected-glow']) vis(id, layers.links);
    for (const id of ['nodes', 'node-selected']) vis(id, layers.nodes);
    for (const id of ['spof-pulse', 'spof-ring']) vis(id, layers.spofs);
    vis('placement', layers.placement);
    vis('predicted', layers.predicted);
    const color = layers.linkColor === 'residual' ? residualColor : snrColor;
    m.setPaintProperty('links', 'line-color', color);
    m.setPaintProperty('links-uncertain', 'line-color', color);
  }), [layers]);

  useEffect(() => whenReady((m) => {
    const key = selection?.kind === 'link' ? `${selection.a}|${selection.b}` : '';
    for (const id of ['links-selected', 'links-selected-glow']) m.setFilter(id, ['==', ['get', 'key'], key]);
    m.setFilter('node-selected', ['==', ['get', 'id'], selection?.kind === 'node' ? selection.id : '']);
    // Keep the selection visible beside the inspector panel (same width as --inspector in styles.css).
    m.easeTo({ padding: { top: 0, bottom: 0, left: 0, right: selection ? INSPECTOR_PX : 0 }, duration: reducedMotion() ? 0 : 400 });
  }), [selection]);

  // 2D is a flat, north-up map; 3D drapes everything over the DEM (when the API has terrain tiles).
  useEffect(() => whenReady((m) => {
    m.setTerrain(view.is3D && base.terrain ? { source: 'terrain-3d', exaggeration: view.exaggeration } : null);
    m.easeTo({ pitch: view.is3D ? PITCH_3D : 0, bearing: view.is3D ? m.getBearing() : 0, duration: reducedMotion() ? 0 : 600 });
  }), [view.is3D]);

  useEffect(() => whenReady((m) => {
    if (view.is3D && base.terrain) m.setTerrain({ source: 'terrain-3d', exaggeration: view.exaggeration });
  }), [view.exaggeration]);

  useEffect(() => {
    if (resetSignal === 0) return;
    const b = home(data, base);
    if (b) whenReady((m) => m.fitBounds(b, { padding: 60, pitch: view.is3D ? PITCH_3D : 0, bearing: 0, duration: reducedMotion() ? 0 : 800 }));
  }, [resetSignal]);

  return <div ref={el} className="map" role="region" aria-label="Network map" />;
}
