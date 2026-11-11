import { useEffect, useState } from 'react';
import { ApiError, api, type Summaries } from '../api/client';
import { linkKey, type Node, type ProfileResponse } from '../api/types';
import { ago, db, int, pct } from '../format';
import type { Selection } from '../state';
import { ProfileChart } from './ProfileChart';

interface Props {
  data: Summaries;
  selection: NonNullable<Selection>;
  onSelect: (s: Selection) => void;
}

const label = (n: Node | undefined, id: string): string => n?.name ?? id;

export function Inspector({ data, selection, onSelect }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onSelect(null);
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onSelect]);

  return (
    <aside className="inspector" aria-label={selection.kind === 'link' ? 'Link inspector' : 'Node inspector'}>
      <button className="close" onClick={() => onSelect(null)} aria-label="Close inspector">×</button>
      {selection.kind === 'link'
        ? <LinkInspector data={data} a={selection.a} b={selection.b} />
        : <NodeInspector data={data} id={selection.id} onSelect={onSelect} />}
    </aside>
  );
}

function LinkInspector({ data, a, b }: { data: Summaries; a: string; b: string }) {
  const key = linkKey(a, b);
  const nodes = new Map(data.topology.nodes.map((n) => [n.id, n]));
  const link = data.topology.links.find((l) => linkKey(l.a, l.b) === key);
  const r = data.residuals?.links.find((l) => linkKey(l.a, l.b) === key);
  if (!link) return <p className="empty">This link is not in the current window.</p>;
  return (
    <>
      <h2 className="inspector-title">
        <span>{label(nodes.get(a), a)}</span>
        <span className="arrow" aria-hidden>⟷</span>
        <span>{label(nodes.get(b), b)}</span>
      </h2>
      <LinkProfile region={data.topology.meta.region} a={a} b={b} names={(id) => label(nodes.get(id), id)} />
      <dl className="metrics">
        <dt>Observed SNR</dt>
        <dd className="mono live">{db(link.snr_db?.median)}{link.snr_db && <small>p10 {link.snr_db.p10.toFixed(1)}, p90 {link.snr_db.p90.toFixed(1)}</small>}</dd>
        <dt>Predicted SNR</dt><dd className="mono predicted">{db(r?.predicted_snr_db)}</dd>
        <dt>Residual</dt>
        <dd className={`mono ${r && r.residual_db < 0 ? 'predicted' : 'live'}`}>
          {db(r?.residual_db)}{r && <small> {r.residual_db < 0 ? 'worse than model' : 'better than model'}</small>}
        </dd>
        <dt>Distance</dt><dd className="mono">{r ? (r.distance_km < 0.05 ? <>under 1 km<small>both ends in one ~1 km grid cell</small></> : `${r.distance_km.toFixed(1)} km`) : '—'}</dd>
        <dt>Observations</dt><dd className="mono">{int(link.observations)}</dd>
        <dt>Link confidence</dt>
        <dd className={`mono ${link.confidence < 0.8 ? 'warn' : ''}`}>
          {pct(link.confidence)}<small> hop-hash ambiguity</small>
        </dd>
        {r && (<>
          <dt>Line of sight</dt><dd className="mono">{r.line_of_sight ? 'clear' : 'obstructed'}</dd>
          <dt>Fresnel clearance</dt>
          <dd className={`mono ${r.fresnel_clearance < 0 ? 'warn' : ''}`}>{r.fresnel_clearance.toFixed(2)} × F1<small>{r.fresnel_clearance < 0 ? 'ground or canopy blocks the path' : r.fresnel_clearance < 0.6 ? 'partly inside the Fresnel zone' : 'clear of the Fresnel zone'}</small></dd>
          <dt>Diffraction loss</dt><dd className="mono">{r.diffraction_loss_db.toFixed(1)} dB</dd>
        </>)}
        <dt>Last seen</dt><dd className="mono">{ago(link.last_seen)}</dd>
      </dl>
    </>
  );
}

function NodeInspector({ data, id, onSelect }: { data: Summaries; id: string; onSelect: (s: Selection) => void }) {
  const [whatIf, setWhatIf] = useState(false);
  useEffect(() => setWhatIf(false), [id]);
  const node = data.topology.nodes.find((n) => n.id === id);
  if (!node) return <p className="empty">This node is not in the current window.</p>;
  const nodes = new Map(data.topology.nodes.map((n) => [n.id, n]));
  const links = data.topology.links
    .filter((l) => l.a === id || l.b === id)
    .sort((x, y) => y.observations - x.observations);
  const spof = data.resilience?.single_points_of_failure.find((s) => s.id === id);
  const base = data.resilience?.baseline;
  return (
    <>
      <h2 className="inspector-title"><span>{label(node, id)}</span></h2>
      <dl className="metrics">
        <dt>Id</dt><dd className="mono">{node.id}</dd>
        <dt>Role</dt><dd>{node.role}{node.observer && <span className="chip live">observer</span>}</dd>
        <dt>Links</dt><dd className="mono">{node.degree}</dd>
        <dt>Last heard</dt><dd className="mono">{ago(node.last_heard)}</dd>
        <dt>Location</dt><dd className="mono">{node.location ? '~1 km grid cell' : 'not advertised'}</dd>
      </dl>
      {spof && <p className="callout warn">▲ Single point of failure</p>}
      <button className="action" aria-expanded={whatIf} disabled={!base} onClick={() => setWhatIf(!whatIf)}
        title={base ? undefined : 'Resilience analysis has not run for this region yet'}>What if this node fails?</button>
      {whatIf && base && (
        <dl className="metrics whatif">
          <dt>Reachable pairs lost</dt>
          <dd className={`mono ${spof ? 'warn' : 'ok'}`}>{int(spof?.reachable_pairs_lost ?? 0)} of {int(base.reachable_pairs)}</dd>
          <dt>Reachability after</dt>
          <dd className="mono">{pct(base.reachable_share)} → {pct(spof?.reachable_share_after ?? base.reachable_share)}</dd>
          <dt>Nodes cut off</dt><dd className="mono">{spof?.nodes_cut_off ?? 0}</dd>
          {spof && (<><dt>Confidence</dt><dd className="mono">{pct(spof.confidence)}</dd></>)}
        </dl>
      )}
      <h3>Strongest links</h3>
      <ul className="linklist">
        {links.slice(0, 8).map((l) => {
          const other = l.a === id ? l.b : l.a;
          const [a, b] = linkKey(l.a, l.b).split('|') as [string, string];
          return (
            <li key={other}>
              <button onClick={() => onSelect({ kind: 'link', a, b })}>
                <span>{label(nodes.get(other), other)}</span>
                <span className="mono live">{db(l.snr_db?.median)}</span>
                <span className="mono dim">{int(l.observations)}</span>
              </button>
            </li>
          );
        })}
      </ul>
    </>
  );
}

function LinkProfile({ region, a, b, names }: { region: string; a: string; b: string; names: (id: string) => string }) {
  const [state, setState] = useState<{ key: string; res?: ProfileResponse; error?: string }>({ key: '' });
  const key = `${region}/${a}/${b}`;
  useEffect(() => {
    const c = new AbortController();
    api.profile(region, a, b, c.signal)
      .then((res) => setState({ key, res }))
      .catch((e: unknown) => {
        if (c.signal.aborted) return;
        const error = e instanceof ApiError && e.status === 404
          ? 'No terrain profile for this link: both ends may share one ~1 km grid cell, or a node has no location.'
          : `Terrain profile unavailable: ${e instanceof Error ? e.message : String(e)}`;
        setState({ key, error });
      });
    return () => c.abort();
  }, [key, region, a, b]);

  if (state.key !== key) return <p className="profile-state" role="status">Loading terrain profile…</p>;
  if (!state.res) return <p className="profile-state">{state.error}</p>;
  const p = state.res.profile;
  return <ProfileChart profile={p} nameA={names(p.a)} nameB={names(p.b)} />;
}
