import type { IngestStatus, Meta } from '../api/types';
import { ago, bytes, duration } from '../format';

interface Props {
  status: IngestStatus | null; // null: /api/v1/status unavailable
  statusError: string | null;
  meta: Meta | undefined;
  region: string | null;
  now: number;
}

const STALE_S = 2 * 3600; // topology job is hourly; two missed runs is stale
const LAG_WARN_S = 300; // the ingester flushes every few minutes; beyond that rows are waiting
const FILL_WARN = 0.5;

const dash = <span className="dim">—</span>;

export function StatusStrip({ status, statusError, meta, region, now }: Props) {
  const s = status;
  const fill = s?.spool_bytes != null && s.spool_max_bytes ? s.spool_bytes / s.spool_max_bytes : null;
  // The DDIL buffer is "filling" when storage is down (rows can only go to disk) or the spool is past half.
  const filling = s?.storage_up === false || (fill ?? 0) > FILL_WARN;
  const stale = meta ? (now - Date.parse(meta.generated_at)) / 1000 > STALE_S : false;
  return (
    <header className="strip">
      <div className="brand">
        <span className="brand-name">MeshTwin</span>
        {region && <span className="mono region">{region}</span>}
      </div>
      {s ? (
        <dl className="strip-metrics">
          <div>
            <dt>Ingest</dt>
            <dd className="mono live">{s.messages_per_s != null ? `${s.messages_per_s.toFixed(1)} msg/s` : dash}</dd>
          </div>
          <div>
            <dt>Sync lag</dt>
            <dd className={`mono ${s.sync_lag_s != null && s.sync_lag_s > LAG_WARN_S ? 'warn' : 'live'}`}>
              {s.sync_lag_s != null ? duration(s.sync_lag_s) : dash}
            </dd>
          </div>
          <div className={filling ? 'buffer is-filling' : 'buffer'}>
            <dt>Disk buffer</dt>
            <dd className="mono">
              {fill != null && (
                <span className="meter" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(fill * 100)}
                  aria-label="Disk buffer fill">
                  <span style={{ width: `${Math.min(100, fill * 100)}%` }} />
                </span>
              )}
              {s.spool_bytes != null && s.spool_max_bytes != null ? `${bytes(s.spool_bytes)} / ${bytes(s.spool_max_bytes)}` : dash}
              {filling && <span className="filling">▲ filling</span>}
            </dd>
          </div>
          <div>
            <dt>Storage</dt>
            <dd className={`mono ${s.storage_up === false ? 'warn' : 'ok'}`}>
              {s.storage_up == null ? dash : s.storage_up ? '● connected' : '▲ unreachable, buffering to disk'}
            </dd>
          </div>
          <div>
            <dt>Broker</dt>
            <dd className={`mono ${s.mqtt_connected === false ? 'warn' : 'ok'}`}>
              {s.mqtt_connected == null ? dash : s.mqtt_connected ? '● subscribed' : '▲ disconnected'}
            </dd>
          </div>
          <Freshness meta={meta} stale={stale} now={now} />
        </dl>
      ) : (
        <dl className="strip-metrics">
          <div>
            <dt>Ingest</dt>
            <dd className="mono dim" title={statusError ?? undefined}>{statusError ? 'metrics unavailable' : 'loading…'}</dd>
          </div>
          <Freshness meta={meta} stale={stale} now={now} />
        </dl>
      )}
    </header>
  );
}

function Freshness({ meta, stale, now }: { meta: Meta | undefined; stale: boolean; now: number }) {
  return (
    <div>
      <dt>Data from</dt>
      <dd className={`mono ${stale ? 'warn' : ''}`} title={meta?.generated_at}>
        {meta ? <>{stale && '▲ '}{ago(meta.generated_at, now)}</> : dash}
      </dd>
    </div>
  );
}
