import { COVERAGE_MAX_MARGIN_DB } from '../map/coverage';
import type { Layers, View } from '../state';

export function Legend({ linkColor, predicted, coverageMargin }: {
  linkColor: Layers['linkColor']; predicted: boolean; coverageMargin: number | null;
}) {
  return (
    <div className="legend" aria-label="Map legend">
      {coverageMargin != null && (
        <>
          <span className="legend-title">Predicted coverage, handset at 2 m</span>
          <span className="ramp ramp-cov" />
          <span className="ramp-labels mono"><span>+{coverageMargin} dB</span><span>+{COVERAGE_MAX_MARGIN_DB} dB over floor</span></span>
          <span className="ramp-labels"><span>blank: not covered</span></span>
        </>
      )}
      {linkColor === 'snr' ? (
        <>
          <span className="legend-title">Observed SNR (last hop)</span>
          <span className="ramp ramp-snr" />
          <span className="ramp-labels mono"><span>−20 dB</span><span>+14 dB</span></span>
        </>
      ) : (
        <>
          <span className="legend-title">Observed − predicted SNR</span>
          <span className="ramp ramp-resid" />
          <span className="ramp-labels mono"><span>−20 dB</span><span>0</span><span>+20 dB</span></span>
          <span className="ramp-labels"><span>worse than model</span><span>better</span></span>
        </>
      )}
      <ul className="legend-keys">
        <li><svg viewBox="0 0 16 16"><path d="M8 2 14 8 8 14 2 8Z" fill="var(--ink)" /></svg>Repeater</li>
        <li><svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="4" fill="var(--ink-dim)" /></svg>Companion</li>
        <li><svg viewBox="0 0 16 16"><path d="M8 2 14 8 8 14 2 8Z" fill="var(--cyan)" /></svg>Observer</li>
        <li><svg viewBox="0 0 16 16"><path d="M1 8h14" stroke="var(--ink-dim)" strokeWidth="2" strokeDasharray="3 2" /></svg>Uncertain link</li>
        {predicted && <li className="wide"><svg viewBox="0 0 16 16"><path d="M1 8h14" stroke="var(--magenta)" strokeWidth="2" strokeDasharray="1 3" /></svg>Predicted, never seen</li>}
      </ul>
    </div>
  );
}

interface ControlsProps {
  view: View;
  setView: (v: View) => void;
  onReset: () => void;
  terrain: boolean;
}

export function ViewControls({ view, setView, onReset, terrain }: ControlsProps) {
  const canExaggerate = terrain && view.is3D;
  return (
    <div className="view-controls">
      <div className="seg" role="radiogroup" aria-label="View mode">
        {[false, true].map((is3D) => (
          <button key={String(is3D)} role="radio" aria-checked={view.is3D === is3D} onClick={() => setView({ ...view, is3D })}>
            {is3D ? '3D' : '2D'}
          </button>
        ))}
      </div>
      <label className={`slider compact${canExaggerate ? '' : ' is-disabled'}`}
        title={terrain ? (view.is3D ? undefined : 'Switch to 3D to raise the terrain') : 'No terrain tiles for this region'}>
        <span className="slider-head">
          <span>Terrain ×</span>
          <output className="mono live">{view.exaggeration.toFixed(1)}</output>
        </span>
        <input type="range" aria-label="Terrain exaggeration" min={1} max={4} step={0.1} value={view.exaggeration} disabled={!canExaggerate}
          onChange={(e) => setView({ ...view, exaggeration: Number(e.target.value) })} />
      </label>
      <button className="action small" onClick={onReset}>Reset view</button>
    </div>
  );
}
