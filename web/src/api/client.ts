// Typed GETs against docs/api-contract.md. Same origin: Vite proxies /api in dev, the Go API serves both in prod.
import type { ApiErrorBody, Coverage, Placement, ProfileResponse, RegionInfo, StatusResponse, Residuals, Resilience, Surprising, Topology } from './types';

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message);
  }
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(`/api/v1${path}`, { signal, headers: { Accept: 'application/json' } });
  if (!r.ok) {
    let code = 'http_error';
    let message = `HTTP ${r.status}`;
    try {
      const e = ((await r.json()) as ApiErrorBody).error;
      code = e.code || code;
      message = e.message || message;
    } catch {
      // Not the contract's error shape: a dev proxy or gateway answered because the API itself is down.
      if (r.status >= 500) message = `Can't reach the API (HTTP ${r.status} from the proxy). Check that it is running.`;
    }
    throw new ApiError(r.status, code, message);
  }
  return (await r.json()) as T;
}

export const TOP_CANDIDATES = 10;

const at = (region: string) => `/regions/${encodeURIComponent(region)}`;

export const api = {
  regions: (s?: AbortSignal) => get<{ regions: RegionInfo[] }>('/regions', s),
  status: (s?: AbortSignal) => get<StatusResponse>('/status', s),
  topology: (r: string, s?: AbortSignal) => get<Topology>(`${at(r)}/topology`, s),
  resilience: (r: string, s?: AbortSignal) => get<Resilience>(`${at(r)}/resilience`, s),
  residuals: (r: string, s?: AbortSignal) => get<Residuals>(`${at(r)}/links/residuals?limit=1000`, s),
  surprising: (r: string, s?: AbortSignal) => get<Surprising>(`${at(r)}/links/surprising`, s),
  placement: (r: string, s?: AbortSignal) => get<Placement>(`${at(r)}/placement?top=${TOP_CANDIDATES}`, s),
  coverage: (r: string, s?: AbortSignal) => get<Coverage>(`${at(r)}/coverage`, s),
  profile: (r: string, a: string, b: string, s?: AbortSignal) =>
    get<ProfileResponse>(`${at(r)}/links/${encodeURIComponent(a)}/${encodeURIComponent(b)}/profile`, s),
};

export interface Summaries {
  topology: Topology;
  resilience: Resilience | null;
  residuals: Residuals | null;
  surprising: Surprising | null;
  placement: Placement | null;
  coverage: Coverage | null;
}

export type Missing = Partial<Record<Exclude<keyof Summaries, 'topology'>, string>>;

// Topology is required to draw anything; the others are optional layers, so one missing job never blanks the map.
export async function loadSummaries(region: string, signal?: AbortSignal): Promise<{ data: Summaries; missing: Missing }> {
  const [topology, resilience, residuals, surprising, placement, coverage] = await Promise.allSettled([
    api.topology(region, signal),
    api.resilience(region, signal),
    api.residuals(region, signal),
    api.surprising(region, signal),
    api.placement(region, signal),
    api.coverage(region, signal),
  ]);
  if (topology.status === 'rejected') throw topology.reason;
  const missing: Missing = {};
  const opt = <T,>(name: keyof Missing, res: PromiseSettledResult<T>): T | null => {
    if (res.status === 'fulfilled') return res.value;
    missing[name] = res.reason instanceof Error ? res.reason.message : String(res.reason);
    return null;
  };
  return {
    data: {
      topology: topology.value,
      resilience: opt('resilience', resilience),
      residuals: opt('residuals', residuals),
      surprising: opt('surprising', surprising),
      placement: opt('placement', placement),
      coverage: opt('coverage', coverage),
    },
    missing,
  };
}
