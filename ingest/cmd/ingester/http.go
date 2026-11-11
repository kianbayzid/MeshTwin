package main

import (
	"net/http"
	"sync/atomic"

	"meshtwin/ingest/internal/ingest"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Packets/sec is rate(meshtwin_ingest_messages_total{result="ok"}[1m]) in
// Prometheus: export counters, let the query layer compute rates.
var (
	messages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "meshtwin_ingest_messages_total",
		Help: "Messages received, by result: ok, duplicate, not_packet, bad_topic, decode_error, rate_limited, oversize, topic_mismatch, shutdown.",
	}, []string{"result"})
	payloadTypes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "meshtwin_ingest_packets_by_type_total",
		Help: "Accepted packets by MeshCore payload type number.",
	}, []string{"payload_type"})
	hashMismatches = promauto.NewCounter(prometheus.CounterOpts{
		Name: "meshtwin_ingest_hash_mismatch_total",
		Help: "Observer 'hash' field disagreed with calculatePacketHash (should stay 0).",
	})
	buffered = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "meshtwin_ingest_rows_buffered",
		Help: "Rows held in memory, not yet written to object storage.",
	})
	filesWritten = promauto.NewCounter(prometheus.CounterOpts{
		Name: "meshtwin_ingest_files_written_total", Help: "Parquet files written.",
	})
	bytesWritten = promauto.NewCounter(prometheus.CounterOpts{
		Name: "meshtwin_ingest_bytes_written_total", Help: "Parquet bytes written.",
	})
	flushErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "meshtwin_ingest_flush_errors_total", Help: "Failed flushes (rows are kept and retried).",
	})
	mqttConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "meshtwin_ingest_mqtt_connected", Help: "1 while subscribed to the broker.",
	})
)

// storageUpFunc reports object storage health: the spool's last upload
// result, or without a spool the last flush (the only time storage is called).
func storageUpFunc(spool *ingest.Spool, flushOK *atomic.Bool) func() bool {
	if spool != nil {
		return spool.StorageUp
	}
	return flushOK.Load
}

// registerStorageMetrics exports storage and spool state, read at scrape
// time. Spool metrics read 0 without a spool so dashboards always find them.
func registerStorageMetrics(spool *ingest.Spool, storageUp func() bool) {
	stats := func() ingest.SpoolStats {
		if spool == nil {
			return ingest.SpoolStats{}
		}
		return spool.Stats()
	}
	gauge := func(name, help string, f func() float64) {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, f)
	}
	counter := func(name, help string, f func() float64) {
		promauto.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help}, f)
	}
	gauge("meshtwin_ingest_storage_up", "1 if the last call to object storage succeeded.", func() float64 {
		// Changes only on a storage call: on a quiet link it lags by up to one flush age.
		return map[bool]float64{true: 1}[storageUp()]
	})
	gauge("meshtwin_ingest_spool_bytes", "Bytes waiting in the disk spool.", func() float64 { return float64(stats().Bytes) })
	gauge("meshtwin_ingest_spool_max_bytes", "Configured spool cap (SPOOL_MAX_BYTES; 0 without a spool).", func() float64 { return float64(stats().MaxBytes) })
	gauge("meshtwin_ingest_spool_files", "Files waiting in the disk spool.", func() float64 { return float64(stats().Files) })
	gauge("meshtwin_ingest_spool_oldest_age_seconds", "Age of the oldest spooled file (0 when empty).", func() float64 { return stats().OldestAge.Seconds() })
	counter("meshtwin_ingest_spool_writes_total", "Files written to the spool (storage was down).", func() float64 { return float64(stats().Writes) })
	counter("meshtwin_ingest_spool_uploads_total", "Spooled files uploaded to object storage.", func() float64 { return float64(stats().Uploads) })
	counter("meshtwin_ingest_spool_evicted_files_total", "Spooled files deleted unsent because the spool was full (data lost).", func() float64 { return float64(stats().EvictedFiles) })
	counter("meshtwin_ingest_spool_evicted_bytes_total", "Bytes of spooled files deleted unsent because the spool was full.", func() float64 { return float64(stats().EvictedBytes) })
}

// httpHandler serves /metrics, /healthz (liveness: the process is serving)
// and /readyz (readiness: connected in live mode and the last flush worked).
// With a spool, a flush "works" while storage is down (the files went to
// disk), so that state is ready but reported as degraded: the pod is doing
// its job. Liveness stays simple on purpose: a broker outage should make the
// pod unready, not get it restarted in a loop.
func httpHandler(mode string, mqttUp, flushOK *atomic.Bool, storageUp func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		switch {
		case mode == "live" && !mqttUp.Load():
			http.Error(w, "mqtt not connected", http.StatusServiceUnavailable)
		case !flushOK.Load():
			http.Error(w, "last flush failed (object storage down, and no spool or spool not writable)", http.StatusServiceUnavailable)
		case !storageUp():
			_, _ = w.Write([]byte("ready (degraded: object storage down, spooling to disk)\n"))
		default:
			_, _ = w.Write([]byte("ready\n"))
		}
	})
	return mux
}
