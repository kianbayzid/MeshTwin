package main

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Packets/sec is rate(meshtwin_ingest_messages_total{result="ok"}[1m]) in
// Prometheus: export counters, let the query layer compute rates.
var (
	messages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "meshtwin_ingest_messages_total",
		Help: "Messages received, by result: ok, duplicate, not_packet, bad_topic, decode_error, rate_limited, oversize, shutdown.",
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

// httpHandler serves /metrics, /healthz (liveness: the process is serving)
// and /readyz (readiness: connected in live mode and the last flush worked).
// Liveness stays simple on purpose: a broker outage should make the pod
// unready, not get it restarted in a loop.
func httpHandler(mode string, mqttUp, flushOK *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		switch {
		case mode == "live" && !mqttUp.Load():
			http.Error(w, "mqtt not connected", http.StatusServiceUnavailable)
		case !flushOK.Load():
			http.Error(w, "last flush to object storage failed", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("ready\n"))
		}
	})
	return mux
}
