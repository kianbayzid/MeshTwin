import type { ReactNode } from 'react';
import type { Missing, Summaries } from '../api/client';
import type { RegionInfo } from '../api/types';
import { CITIES } from '../map/cities';
import { SNR_FLOOR, type Filters } from '../map/geojson';
import type { Layers } from '../state';

interface Props {
  layers: Layers;
  setLayers: (l: Layers) => void;
  filters: Filters;
  setFilters: (f: Filters) => void;
  regions: RegionInfo[];
  area: string; // region or city code; '' = the whole US
  onPick: (code: string) => void;
  data: Summaries | undefined;
  missing: Missing;
}

function Toggle({ label, icon, checked, disabled, hint, onChange }: {
  label: string; icon: ReactNode; checked: boolean; disabled?: boolean; hint?: string; onChange: (v: boolean) => void;
}) {
  return (
    <label className={`toggle${disabled ? ' is-disabled' : ''}`} title={hint}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle-icon" aria-hidden>{icon}</span>
      <span className="toggle-label">{label}</span>
      {hint && <span className="hint">{hint}</span>}
    </label>
  );
}

function Slider({ label, value, min, max, step, unit, onChange }: {
  label: string; value: number; min: number; max: number; step: number; unit: string; onChange: (v: number) => void;
}) {
  return (
    <label className="slider">
      <span className="slider-head">
        <span>{label}</span>
        <output className="mono live">{value.toFixed(step < 1 ? 2 : 0)}{unit}</output>
      </span>
      {/* The wrapping <label> names its first labelable child, the <output>, so the slider needs its own name. */}
      <input type="range" aria-label={label} min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} />
    </label>
  );
}

const Glyph = ({ d, color }: { d: string; color: string }) => (
  <svg width="16" height="16" viewBox="0 0 16 16"><path d={d} fill={color} /></svg>
);
const DIAMOND = 'M8 1 15 8 8 15 1 8Z';
const LINE = 'M1 7h14v2H1z';
const RING = 'M8 2a6 6 0 1 0 0 12A6 6 0 1 0 8 2zm0 2a4 4 0 1 1 0 8 4 4 0 1 1 0-8z';
const DOTTED = 'M1 7h2v2H1zm4 0h2v2H5zm4 0h2v2H9zm4 0h2v2h-2z';
const SQUARE = 'M2 2h12v12H2z';
const BADGE = 'M8 1a7 7 0 1 0 0 14A7 7 0 1 0 8 1z';

// Region codes are IATA codes, like the city list's, so a region with data gets its city's name.
const placeName = (code: string) => {
  const c = CITIES.find((x) => x.code === code);
  return c ? `${c.name}, ${c.state} (${code})` : code;
};

export function Sidebar({ layers, setLayers, filters, setFilters, regions, area, onPick, data, missing }: Props) {
  // A region still waiting for its first topology run stays pickable as a city, so you can fly there.
  const withData = new Set(regions.filter((r) => r.summaries.includes('topology')).map((r) => r.region));
  const set = <K extends keyof Layers>(k: K, v: Layers[K]) => setLayers({ ...layers, [k]: v });
  const residuals = data?.residuals;
  const placement = data?.placement;
  // A missing summary disables its layer and says why (the API's own message), instead of drawing nothing silently.
  const why = (k: keyof Missing) => (missing[k] ? `Not available: ${missing[k]}` : undefined);
  return (
    <aside className="sidebar">
      <section>
        <h2>Layers</h2>
        <Toggle label="Nodes" icon={<Glyph d={DIAMOND} color="var(--ink)" />} checked={layers.nodes} onChange={(v) => set('nodes', v)} />
        <Toggle label="Links" icon={<Glyph d={LINE} color="var(--cyan)" />} checked={layers.links} onChange={(v) => set('links', v)} />
        <div className="seg" role="radiogroup" aria-label="Color links by">
          {(['snr', 'residual'] as const).map((m) => (
            <button key={m} role="radio" aria-checked={layers.linkColor === m} onClick={() => set('linkColor', m)}
              disabled={m === 'residual' && !!missing.residuals} title={m === 'residual' ? why('residuals') : undefined}>
              {m === 'snr' ? 'Observed SNR' : 'Model residual'}
            </button>
          ))}
        </div>
        <Toggle label="Predicted, never seen" icon={<Glyph d={DOTTED} color="var(--magenta)" />} checked={layers.predicted}
          disabled={!!missing.surprising} hint={why('surprising')} onChange={(v) => set('predicted', v)} />
        <Toggle label="Single points of failure" icon={<Glyph d={RING} color="var(--amber)" />} checked={layers.spofs}
          disabled={!!missing.resilience} hint={why('resilience')} onChange={(v) => set('spofs', v)} />
        <Toggle label="Repeater candidates" icon={<Glyph d={BADGE} color="var(--lime)" />} checked={layers.placement}
          disabled={!!missing.placement} hint={why('placement')} onChange={(v) => set('placement', v)} />
        <Toggle label="Predicted coverage" icon={<Glyph d={SQUARE} color="var(--magenta)" />} checked={layers.coverage}
          disabled={!!missing.coverage} hint={why('coverage')} onChange={(v) => set('coverage', v)} />
        {layers.coverage && !missing.coverage && (
          <Slider label="Covered at" value={layers.coverageMargin} min={0} max={20} step={1} unit=" dB over floor"
            onChange={(v) => set('coverageMargin', v)} />
        )}
      </section>

      <section>
        <h2>Filters</h2>
        <label className="select">
          <span>Area</span>
          <select value={area} onChange={(e) => onPick(e.target.value)}>
            <option value="">Whole US</option>
            {withData.size > 0 && (
              <optgroup label="MeshTwin data">
                {[...withData].map((code) => <option key={code} value={code}>{placeName(code)}</option>)}
              </optgroup>
            )}
            <optgroup label="MeshCore cities (LetsMesh)">
              {CITIES.filter((c) => !withData.has(c.code)).map((c) => (
                <option key={c.code} value={c.code}>{c.name}, {c.state}</option>
              ))}
            </optgroup>
          </select>
        </label>
        <label className="select">
          <span>Time window</span>
          <select defaultValue="job" disabled><option value="job">{data ? windowLabel(data.topology.meta.window) : '—'}</option></select>
        </label>
        <Slider label="Minimum SNR" value={filters.minSnr} min={SNR_FLOOR} max={14} step={1} unit=" dB"
          onChange={(v) => setFilters({ ...filters, minSnr: v })} />
        <Slider label="Minimum link confidence" value={filters.minConfidence} min={0.5} max={1} step={0.05} unit=""
          onChange={(v) => setFilters({ ...filters, minConfidence: v })} />
      </section>

      <section>
        <h2>Model</h2>
        <dl className="kv">
          <dt>Propagation</dt><dd className="mono">{residuals ? `${residuals.model.name} v${residuals.model.version}` : '—'}</dd>
          <dt>Elevation data</dt><dd className="mono">{residuals?.model.dem ?? '—'}</dd>
          <dt>Antenna height</dt><dd className="mono live">{placement ? `${placement.method.antenna_height_m} m` : '—'}</dd>
          <dt>Coverage handset</dt><dd className="mono">{data?.coverage ? `${data.coverage.method.rx_height_m} m, ${data.coverage.method.transmitters} repeaters` : '—'}</dd>
          <dt>Candidates scored</dt><dd className="mono">{placement?.method.candidates_scored.toLocaleString('en-US') ?? '—'}</dd>
          <dt>Fit (RMSE)</dt><dd className="mono">{residuals?.summary.rmse_db != null ? `${residuals.summary.rmse_db.toFixed(1)} dB` : '—'}</dd>
        </dl>
      </section>
    </aside>
  );
}

function windowLabel(w: { from: string; to: string } | null): string {
  if (!w) return '—';
  const h = Math.round((Date.parse(w.to) - Date.parse(w.from)) / 3_600_000);
  return `${h} h ending ${w.to.slice(0, 16).replace('T', ' ')} UTC`;
}
