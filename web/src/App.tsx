import { useCallback, useEffect, useState } from 'react';
import { api, loadSummaries, type Missing, type Summaries } from './api/client';
import { mockStatus } from './api/mock';
import type { RegionInfo } from './api/types';
import { Inspector } from './components/Inspector';
import { Legend, ViewControls } from './components/MapOverlays';
import { MapView } from './components/MapView';
import { Sidebar } from './components/Sidebar';
import { StatusStrip } from './components/StatusStrip';
import { SNR_FLOOR, type Filters } from './map/geojson';
import { baseStyle, type BaseStyle } from './map/style';
import { initialLayers, type Layers, type Selection, type View } from './state';

interface Loaded {
  data: Summaries;
  missing: Missing;
  base: BaseStyle;
}

const describe = (e: unknown): string =>
  e instanceof TypeError ? "Can't reach the API. Check that it is running." : e instanceof Error ? e.message : String(e);

export function App() {
  const [regions, setRegions] = useState<RegionInfo[]>([]);
  const [region, setRegion] = useState<string | null>(null);
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [layers, setLayers] = useState<Layers>(initialLayers);
  const [filters, setFilters] = useState<Filters>({ minSnr: SNR_FLOOR, minConfidence: 0.5 });
  const [view, setView] = useState<View>({ is3D: true, exaggeration: 1.5 });
  const [selection, setSelectionState] = useState<Selection>(null);
  const [resetSignal, setResetSignal] = useState(0);
  const setSelection = useCallback((s: Selection) => setSelectionState(s), []);

  useEffect(() => {
    const c = new AbortController();
    api.regions(c.signal)
      .then(({ regions }) => {
        setRegions(regions);
        const first = regions.find((r) => r.summaries.includes('topology'));
        if (first) setRegion(first.region);
        else setError('No region has a topology summary yet. Run the topology job, then reload.');
      })
      .catch((e: unknown) => c.signal.aborted || setError(describe(e)));
    return () => c.abort();
  }, []);

  useEffect(() => {
    if (!region) return;
    const c = new AbortController();
    setLoaded(null);
    setSelectionState(null);
    setError(null);
    Promise.all([loadSummaries(region, c.signal), baseStyle(region)])
      .then(([{ data, missing }, base]) => setLoaded({ data, missing, base }))
      .catch((e: unknown) => c.signal.aborted || setError(describe(e)));
    return () => c.abort();
  }, [region]);

  const data = loaded?.data;
  return (
    <div className="app">
      <StatusStrip status={mockStatus} meta={data?.topology.meta} region={region} />
      <Sidebar layers={layers} setLayers={setLayers} filters={filters} setFilters={setFilters}
        regions={regions} region={region} setRegion={setRegion} data={data} missing={loaded?.missing ?? {}} />
      <main className="stage">
        {loaded && data && (
          <MapView key={region} base={loaded.base} data={data} layers={layers} filters={filters} view={view}
            selection={selection} resetSignal={resetSignal} onSelect={setSelection} />
        )}
        {!loaded && (
          <p className={`stage-msg${error ? ' warn' : ''}`} role={error ? 'alert' : 'status'}>
            {error ?? `Loading ${region ?? 'regions'}…`}
          </p>
        )}
        <ViewControls view={view} setView={setView} onReset={() => setResetSignal((n) => n + 1)} terrain={!!loaded?.base.terrain} />
        <Legend linkColor={layers.linkColor} predicted={layers.predicted} />
        {selection && data && <Inspector data={data} selection={selection} onSelect={setSelection} />}
      </main>
    </div>
  );
}
