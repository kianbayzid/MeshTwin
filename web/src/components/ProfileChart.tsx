import { useState, type KeyboardEvent, type MouseEvent } from 'react';
import type { Profile, ProfileResponse } from '../api/types';
import { extent, samples, worst, type Sample } from '../profile';

interface Props {
  profile: Profile;
  nameA: string;
  nameB: string;
  params: ProfileResponse['params'];
}

const W = 332;
const H = 190;
const M = { l: 40, r: 8, t: 10, b: 22 };

const path = (pts: [number, number][]): string => pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join('');
const clearanceText = (c: number): string => (Number.isFinite(c) ? `${c.toFixed(2)} × F1` : 'antenna');

// Consecutive samples in the same non-clear state, so obstructions draw as runs along the terrain line.
function runs(s: Sample[], state: Sample['state']): number[][] {
  const out: number[][] = [];
  s.forEach((x, i) => {
    if (x.state !== state) return;
    const last = out[out.length - 1];
    if (last && last[last.length - 1] === i - 1) last.push(i);
    else out.push([i]);
  });
  return out;
}

export function ProfileChart({ profile, nameA, nameB, params }: Props) {
  const s = samples(profile);
  const [lo, hi] = extent(s);
  const d = Math.max(profile.distance_km, 1e-6);
  const x = (km: number) => M.l + (km / d) * (W - M.l - M.r);
  const y = (m: number) => M.t + ((hi - m) / (hi - lo)) * (H - M.t - M.b);
  const [hover, setHover] = useState<number | null>(null);
  const w = worst(s);
  const last = s.length - 1;

  const terrain = path([[x(0), y(lo)], ...s.map((p): [number, number] => [x(p.km), y(p.terrain)]), [x(d), y(lo)]]) + 'Z';
  const band = path([
    ...s.map((p): [number, number] => [x(p.km), y(p.los + p.r1)]),
    ...[...s].reverse().map((p): [number, number] => [x(p.km), y(p.los - p.r1)]),
  ]) + 'Z';
  const along = (idx: number[]) => path(idx.map((i) => [x(s[i]!.km), y(s[i]!.terrain)]));

  const pick = (e: MouseEvent<SVGRectElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    const km = ((e.clientX - box.left) / box.width) * d;
    setHover(Math.max(0, Math.min(last, Math.round((km / d) * last))));
  };
  const keys = (e: KeyboardEvent<SVGSVGElement>) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const step = e.key === 'ArrowRight' ? 1 : -1;
    setHover((h) => Math.max(0, Math.min(last, (h ?? (step > 0 ? -1 : last + 1)) + step)));
  };

  const blocked = s.filter((p) => p.state === 'blocked');
  const summary = blocked.length
    ? `Terrain blocks the line of sight over ${blocked.length} of ${s.length} samples` +
      (w >= 0 ? `; worst clearance ${clearanceText(s[w]!.clearance)} at ${s[w]!.km.toFixed(1)} km.` : '.')
    : w >= 0 && s[w]!.clearance < 0.6
      ? `Line of sight is clear but terrain enters the Fresnel zone; worst clearance ${clearanceText(s[w]!.clearance)}.`
      : 'Line of sight and 60 % of the first Fresnel zone are clear.';
  const h = hover === null ? null : s[hover]!;

  return (
    <figure className="profile">
      <div className="profile-plot">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Terrain profile, ${profile.distance_km} km. ${summary}`}
          tabIndex={0} onKeyDown={keys} onBlur={() => setHover(null)}>
          <line className="grid" x1={M.l} x2={W - M.r} y1={y(lo)} y2={y(lo)} />
          <text className="tick" x={M.l - 6} y={y(hi) + 4} textAnchor="end">{Math.round(hi)} m</text>
          <text className="tick" x={M.l - 6} y={y(lo)} textAnchor="end">{Math.round(lo)} m</text>
          <text className="tick" x={x(0)} y={H - 6}>0</text>
          <text className="tick" x={x(d)} y={H - 6} textAnchor="end">{profile.distance_km.toFixed(1)} km</text>

          <path className="fresnel" d={band} />
          <path className="terrain" d={terrain} />
          {runs(s, 'fresnel').map((r) => <path key={`f${r[0]}`} className="obstruct intrude" d={along(r)} />)}
          {runs(s, 'blocked').map((r) => <path key={`b${r[0]}`} className="obstruct" d={along(r)} />)}
          <line className="los" x1={x(0)} y1={y(s[0]!.los)} x2={x(d)} y2={y(s[last]!.los)} />
          <line className="mast" x1={x(0)} x2={x(0)} y1={y(profile.ground_m[0]!)} y2={y(s[0]!.los)} />
          <line className="mast" x1={x(d)} x2={x(d)} y1={y(profile.ground_m[last]!)} y2={y(s[last]!.los)} />
          {w >= 0 && s[w]!.clearance < 0.6 && (
            <text className="worst" x={x(s[w]!.km)} y={y(s[w]!.terrain) - 6} textAnchor="middle">▲</text>
          )}
          {h && (
            <g className="cross" aria-hidden>
              <line x1={x(h.km)} x2={x(h.km)} y1={M.t} y2={y(lo)} />
              <circle cx={x(h.km)} cy={y(h.terrain)} r={3.5} />
            </g>
          )}
          <rect x={M.l} y={M.t} width={W - M.l - M.r} height={H - M.t - M.b} fill="transparent"
            onMouseMove={pick} onMouseLeave={() => setHover(null)} />
        </svg>
        {h && (
          <div className="profile-tip mono" style={{ left: `${(x(h.km) / W) * 100}%` }} role="status">
            <span>{h.km.toFixed(2)} km</span>
            <span>terrain {h.terrain.toFixed(0)} m</span>
            <span>ray {h.los.toFixed(0)} m</span>
            <span className={h.state === 'clear' ? '' : 'warn'}>{clearanceText(h.clearance)}</span>
          </div>
        )}
      </div>
      <div className="profile-ends"><span>{nameA}</span><span>{nameB}</span></div>
      <ul className="profile-key">
        <li><svg viewBox="0 0 16 10" aria-hidden><path d="M1 5h14" className="los" /></svg>Line of sight</li>
        <li><svg viewBox="0 0 16 10" aria-hidden><rect x="1" y="1" width="14" height="8" className="fresnel" /></svg>1st Fresnel zone</li>
        <li><svg viewBox="0 0 16 10" aria-hidden><path d="M1 5h14" className="obstruct" /></svg>Blocks the ray</li>
        <li><svg viewBox="0 0 16 10" aria-hidden><path d="M1 5h14" className="obstruct intrude" /></svg>Inside 60 % of the zone</li>
      </ul>
      <figcaption>
        {summary} Path between the two ~1 km grid cells; the model's numbers below use the exact positions.
        Fresnel zone at {params.freq_mhz} MHz, Earth curvature with k = {params.k_factor}.
      </figcaption>
    </figure>
  );
}
