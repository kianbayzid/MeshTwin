package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Labels use the mux route pattern (r.Pattern), never the raw path, so a
// scan of random URLs cannot create unbounded label values.
var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "meshtwin_api_requests_total",
		Help: "HTTP requests by route pattern and status code.",
	}, []string{"route", "code"})
	latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "meshtwin_api_request_duration_seconds",
		Help:    "Request latency by route pattern.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 4, 8), // 0.5 ms .. 8 s
	}, []string{"route"})
	validationFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "meshtwin_api_summary_validation_failures_total",
		Help: "Summary files rejected by the privacy/contract checks (not served), by summary name.",
	}, []string{"summary"})
	promFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "meshtwin_api_status_query_failures_total",
		Help: "Refreshes of GET /api/v1/status that failed because Prometheus errored or timed out.",
	})
	// Only updated when a summary is (re)loaded, i.e. on a request after the
	// cache TTL; GET /api/v1/regions loads every listed summary, so polling it refreshes all.
	summaryGenerated = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "meshtwin_api_summary_generated_timestamp_seconds",
		Help: "meta.generated_at of the last summary that passed validation, as Unix seconds.",
	}, []string{"region", "summary"})
)

// recordGenerated sets the freshness gauge from a summary that passed
// validate. Labels stay bounded: region passed store.ValidRegion and names a
// stored file, and name is in the allow-list (validate rejects anything else).
func recordGenerated(region, name string, validated []byte) {
	var head struct {
		Meta struct {
			GeneratedAt *time.Time `json:"generated_at"`
		} `json:"meta"`
	}
	if json.Unmarshal(validated, &head) == nil && head.Meta.GeneratedAt != nil {
		summaryGenerated.WithLabelValues(region, name).Set(float64(head.Meta.GeneratedAt.Unix()))
	}
}

// cspHeader is the contract's policy: everything from this origin only.
// MapLibre runs its workers from blob: URLs and decodes images to blob:.
const cspHeader = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"font-src 'self'; connect-src 'self'; worker-src blob:; child-src blob:; frame-ancestors 'none'; " +
	"base-uri 'none'; form-action 'none'"

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// instrument sets the security headers on every response and records
// request count and latency.
func instrument(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", cspHeader)
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		mux.ServeHTTP(sw, r)
		route := r.Pattern // set by the mux on this request (Go 1.23+)
		if route == "" {
			route = "unmatched"
		}
		requests.WithLabelValues(route, strconv.Itoa(sw.code)).Inc()
		latency.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
