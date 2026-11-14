import { useCallback, useEffect, useRef, useState } from 'react';
import { api, loadSummaries, NO_DATA, type Missing, type Summaries } from './api/client';
import type { IngestStatus, RegionInfo } from './api/types';
import { Inspector } from './components/Inspector';
import { Legend, ViewControls } from './components/MapOverlays';
import { MapView } from './components/MapView';
import { Sidebar } from './components/Sidebar';
import { StatusStrip } from './components/StatusStrip';
import { SNR_FLOOR, type Filters } from './map/geojson';
import { CITIES } from './map/cities';
import { baseStyle, type BaseStyle } from './map/style';
import { initialLayers, US_VIEW, type FlyTo, type Layers, type Selection, type View } from './state';

interface Loaded {
  data: Summaries | null; // null: no region has data yet, the map shows the US
  missing: Missing;
  base: BaseStyle;
}

const STATUS_MS = 15_000; // ingest metrics: the API caches them for 10 s
const REGIONS_MS = 60_000; // summaries change hourly at most
const FILTER_DEBOUNCE_MS = 150; // rebuilding 300+ link features on every slider tick stutters the map

const describe = (e: unknown): string =>
  e instanceof TypeError ? "Can't reach the API. Check that it is running." : e instanceof Error ? e.message : String(e);

// Runs fn now and every `ms`, skipping ticks while the tab is hidden.
function usePoll(fn: () => void, ms: number, deps: unknown[]): void {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    ref.current();
    const id = setInterval(() => document.hidden || ref.current(), ms);
    return () => clearInterval(id);
  }, deps);
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

export function App() {
  const [regions, setRegions] = useState<RegionInfo[]>([]);
  const [region, setRegion] = useState<string | null | undefined>(undefined); // undefined until /regions answers
  const [area, setArea] = useState(''); // the picker's value: a region or city code, '' = the whole US
  const [fly, setFly] = useState<FlyTo | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<{ ingest: IngestStatus | null; error: string | null }>({ ingest: null, error: null });
  const [now, setNow] = useState(Date.now());
  const [layers, setLayers] = useState<Layers>(initialLayers);
  const [filters, setFilters] = useState<Filters>({ minSnr: SNR_FLOOR, minConfidence: 0.5 });
  const mapFilters = useDebounced(filters, FILTER_DEBOUNCE_MS);
  const [view, setView] = useState<View>({ is3D: true, exaggeration: 1.5 });
  const [selection, setSelectionState] = useState<Selection>(null);
  const [resetSignal, setResetSignal] = useState(0);
  const setSelection = useCallback((s: Selection) => setSelectionState(s), []);
  const version = useRef<string | null>(null); // the region's updated_at when its summaries were loaded
  const current = useRef<string | null>(null); // region on screen; a late refresh for another region is dropped

  useEffect(() => {
    const c = new AbortController();
    api.regions(c.signal)
      .then(({ regions }) => {
        setRegions(regions);
        const first = regions.find((r) => r.summaries.includes('topology'));
        setRegion(first?.region ?? null);
        setArea(first?.region ?? '');
        if (!first) setNotice('No region has MeshTwin data yet. Pick a city, or connect an observer and wait for the topology job.');
      })
      .catch((e: unknown) => c.signal.aborted || setError(describe(e)));
    return () => c.abort();
  }, []);

  useEffect(() => {
    if (region === undefined) return;
    current.current = region;
    const c = new AbortController();
    setLoaded(null);
    setSelectionState(null);
    setError(null);
    version.current = regions.find((r) => r.region === region)?.updated_at ?? null;
    Promise.all([region ? loadSummaries(region, c.signal) : { data: null, missing: {} }, baseStyle(region)])
      .then(([{ data, missing }, base]) => setLoaded({ data, missing, base }))
      .catch((e: unknown) => c.signal.aborted || setError(describe(e)));
    return () => c.abort();
    // regions is read only to note the version being loaded; a new list must not reload the map.
  }, [region]);

  usePoll(() => {
    api.status()
      .then(({ ingest }) => setStatus({ ingest, error: null }))
      .catch((e: unknown) => setStatus({ ingest: null, error: describe(e) }));
  }, STATUS_MS, []);

  // When a job publishes new summaries for this region, swap them in without rebuilding the map or the camera.
  usePoll(() => {
    setNow(Date.now());
    if (region === undefined) return;
    api.regions()
      .then(async ({ regions }) => {
        setRegions(regions); // with no region yet, new ones still appear in the picker
        if (!region) return;
        const updated = regions.find((r) => r.region === region)?.updated_at ?? null;
        if (updated === version.current) return;
        const { data, missing } = await loadSummaries(region);
        if (current.current !== region) return;
        version.current = updated;
        setLoaded((l) => l && { ...l, data, missing });
      })
      .catch(() => undefined); // keep showing the last good data; the strip's freshness turns amber when it ages
  }, REGIONS_MS, [region]);

  // A region with data loads it (the map fits to its network); any other city only moves the camera.
  const pick = (code: string) => {
    setArea(code);
    setNotice(null);
    const city = CITIES.find((c) => c.code === code);
    if (regions.some((r) => r.region === code && r.summaries.includes('topology'))) {
      if (code === region) setResetSignal((n) => n + 1);
      else setRegion(code);
    } else if (city) {
      setFly({ lon: city.lon, lat: city.lat, zoom: 9, n: (fly?.n ?? 0) + 1 });
      setNotice(`No MeshTwin data for ${city.name}, ${city.state} yet. LetsMesh shows MeshCore activity there (${city.count} observers).`);
    } else setFly({ ...US_VIEW, n: (fly?.n ?? 0) + 1 });
  };

  const data = loaded?.data ?? undefined;
  return (
    <div className="app">
      <StatusStrip status={status.ingest} statusError={status.error} meta={data?.topology.meta} region={region ?? null} now={now} />
      <Sidebar layers={layers} setLayers={setLayers} filters={filters} setFilters={setFilters}
        regions={regions} area={area} onPick={pick} data={data} missing={loaded?.missing ?? {}} />
      <main className="stage">
        {loaded && (
          <MapView key={region ?? 'us'} base={loaded.base} data={data ?? NO_DATA} layers={layers} filters={mapFilters} view={view}
            selection={selection} resetSignal={resetSignal} fly={fly} onSelect={setSelection} />
        )}
        {loaded && notice && <p className="stage-note" role="status">{notice}</p>}
        {!loaded && (
          <p className={`stage-msg${error ? ' warn' : ''}`} role={error ? 'alert' : 'status'}>
            {error ?? `Loading ${region ?? 'map'}…`}
          </p>
        )}
        <ViewControls view={view} setView={setView} onReset={() => { setResetSignal((n) => n + 1); setArea(region ?? ''); setNotice(null); }} terrain={!!loaded?.base.terrain} />
        <Legend linkColor={layers.linkColor} predicted={layers.predicted}
          coverageMargin={layers.coverage && data?.coverage ? layers.coverageMargin : null} />
        {selection && data && <Inspector data={data} selection={selection} onSelect={setSelection} />}
      </main>
    </div>
  );
}
