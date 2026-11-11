import type { IngestStatus } from '../api/mock';
import type { Meta } from '../api/types';
import { ago, bytes, duration } from '../format';

interface Props {
  status: IngestStatus;
  meta: Meta | undefined;
  region: string | null;
}

const STALE_S = 2 * 3600; // topology job is hourly; two missed runs is stale

export function StatusStrip({ status, meta, region }: Props) {
  const fill = status.spool_bytes / status.spool_max_bytes;
  const filling = fill > 0.5 || !status.storage_up;
  const stale = meta ? (Date.now() - Date.parse(meta.generated_at)) / 1000 > STALE_S : false;
  return (
    <header className="strip">
      <div className="brand">
        <span className="brand-name">MeshTwin</span>
        {region && <span className="mono region">{region}</span>}
      </div>
      <dl className="strip-metrics">
        <div>
          <dt>Ingest</dt>
          <dd className="mono live">{status.rate_per_s.toFixed(1)} msg/s</dd>
        </div>
        <div>
          <dt>Sync lag</dt>
          <dd className={`mono ${status.sync_lag_s > 300 ? 'warn' : 'live'}`}>{duration(status.sync_lag_s)}</dd>
        </div>
        <div className={filling ? 'buffer is-filling' : 'buffer'}>
          <dt>Disk buffer</dt>
          <dd className="mono">
            <span className="meter" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(fill * 100)}
              aria-label="Disk buffer fill">
              <span style={{ width: `${Math.min(100, fill * 100)}%` }} />
            </span>
            {bytes(status.spool_bytes)} / {bytes(status.spool_max_bytes)}
          </dd>
        </div>
        <div>
          <dt>Storage</dt>
          <dd className={`mono ${status.storage_up ? 'ok' : 'warn'}`}>
            {status.storage_up ? '● connected' : '▲ unreachable, buffering to disk'}
          </dd>
        </div>
        <div>
          <dt>Data from</dt>
          <dd className={`mono ${stale ? 'warn' : ''}`} title={meta?.generated_at}>
            {meta ? <>{stale && '▲ '}{ago(meta.generated_at)}</> : '—'}
          </dd>
        </div>
      </dl>
      {/* Ingest metrics are wired in Phase 5; until then say so on screen. */}
      <span className="tag">Ingest metrics: sample</span>
    </header>
  );
}
