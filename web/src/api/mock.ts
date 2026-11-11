// Ingest health is not in the API yet. Phase 5 maps these to the ingester's Prometheus metrics:
// rate(meshtwin_ingest_messages_total), meshtwin_ingest_spool_oldest_age_seconds,
// meshtwin_ingest_spool_bytes / meshtwin_ingest_spool_max_bytes, meshtwin_ingest_storage_up.
export interface IngestStatus {
  rate_per_s: number;
  sync_lag_s: number;
  spool_bytes: number;
  spool_max_bytes: number;
  storage_up: boolean;
}

export const mockStatus: IngestStatus = {
  rate_per_s: 10.4,
  sync_lag_s: 412,
  spool_bytes: 1_610_000_000,
  spool_max_bytes: 2_147_483_648,
  storage_up: false,
};
