package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// statusQueries are the contract's PromQL strings (docs/api-contract.md,
// GET /api/v1/status). They are constants: nothing from a request reaches
// Prometheus. The two booleans query the bare min() and compare to 1 here:
// PromQL's "min(x) == 1" filters (an ingester down gives an empty result,
// i.e. null), where the contract wants false.
var statusQueries = []struct{ field, promql string }{
	{"messages_per_s", `sum(rate(meshtwin_ingest_messages_total{result="ok"}[5m]))`},
	{"sync_lag_s", `max(meshtwin_ingest_spool_oldest_age_seconds)`},
	{"spool_bytes", `sum(meshtwin_ingest_spool_bytes)`},
	{"spool_max_bytes", `sum(meshtwin_ingest_spool_max_bytes)`},
	{"storage_up", `min(meshtwin_ingest_storage_up)`},
	{"mqtt_connected", `min(meshtwin_ingest_mqtt_connected)`},
}

const (
	statusTTL        = 10 * time.Second // contract: max-age=10
	statusFailureTTL = 2 * time.Second  // an outage is retried soon, but not by every request
	promMaxBody      = 1 << 20
)

// promClient does not follow redirects: Prometheus' query API never
// redirects, so a 3xx means a misconfigured URL or proxy and is a failure (503).
var promClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// statusTimeout bounds all six queries together (a var so tests can shorten it).
var statusTimeout = 2 * time.Second

type ingestStatus struct {
	MessagesPerS  *float64 `json:"messages_per_s"`
	SyncLagS      *float64 `json:"sync_lag_s"`
	SpoolBytes    *int64   `json:"spool_bytes"`
	SpoolMaxBytes *int64   `json:"spool_max_bytes"`
	StorageUp     *bool    `json:"storage_up"`
	MQTTConnected *bool    `json:"mqtt_connected"`
}

// statusCache holds the last assembled /status body. mu is held across a
// refresh, so concurrent requests wait for the one in flight and share it.
type statusCache struct {
	mu   sync.Mutex
	body []byte
	err  error
	at   time.Time
}

// SetPrometheus enables GET /api/v1/status against the Prometheus at raw
// (an absolute http(s) URL without credentials). Empty leaves it off (503).
func (s *Server) SetPrometheus(raw string) error {
	if raw == "" {
		s.prom = nil
		return nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Errorf("prometheus url: %w", err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		return fmt.Errorf("prometheus url %q: want an absolute http or https URL", raw)
	case u.User != nil:
		return errors.New("prometheus url: must not contain credentials")
	case u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("prometheus url %q: must not have a query or fragment", raw)
	}
	s.prom = u
	return nil
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if s.prom == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "ingest status is not configured")
		return
	}
	c := &s.statusCache
	c.mu.Lock()
	ttl := statusTTL
	if c.err != nil {
		ttl = statusFailureTTL
	}
	if c.at.IsZero() || s.now().Sub(c.at) >= ttl {
		// The result is shared, so one client hanging up must not cancel it for the others.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), statusTimeout)
		c.body, c.err = s.queryStatus(ctx)
		cancel()
		c.at = s.now()
		if c.err != nil {
			promFailures.Inc()
			slog.Error("ingest status from prometheus", "err", c.err)
		}
	}
	body, err := c.body, c.err
	c.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "ingest status is unavailable")
		return
	}
	writeBody(w, r, body, "application/json", "max-age=10")
}

// queryStatus runs the six queries concurrently (one 2 s budget for the
// whole set rather than six round trips back to back) and assembles the body.
// Any failed query fails the lot: a strip with half its numbers missing for
// no visible reason is worse than an honest "unavailable".
func (s *Server) queryStatus(ctx context.Context) ([]byte, error) {
	asOf := s.now().UTC().Truncate(time.Second)
	vals := make([]*float64, len(statusQueries))
	errs := make([]error, len(statusQueries))
	var wg sync.WaitGroup
	for i, q := range statusQueries {
		wg.Go(func() { vals[i], errs[i] = s.promQuery(ctx, q.promql) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	st := ingestStatus{
		MessagesPerS:  round1(vals[0]),
		SyncLagS:      round1(vals[1]),
		SpoolBytes:    toInt(vals[2]),
		SpoolMaxBytes: toInt(vals[3]),
		StorageUp:     isOne(vals[4]),
		MQTTConnected: isOne(vals[5]),
	}
	return json.Marshal(struct {
		AsOf   time.Time    `json:"as_of"`
		Ingest ingestStatus `json:"ingest"`
	}{asOf, st})
}

// promQuery runs one instant query and returns its single sample, or nil for
// an empty result or a NaN/Inf value.
func (s *Server) promQuery(ctx context.Context, promql string) (*float64, error) {
	u := s.prom.JoinPath("api/v1/query")
	u.RawQuery = url.Values{"query": {promql}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", promql, err)
	}
	resp, err := promClient.Do(req) // URL: operator config validated at startup + a constant query
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", promql, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, promMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("query %q: read: %w", promql, err)
	}
	if len(body) > promMaxBody {
		return nil, fmt.Errorf("query %q: response over %d bytes", promql, promMaxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query %q: HTTP %d: %.200s", promql, resp.StatusCode, body) // logged only, never sent to clients
	}
	var pr struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value [2]json.RawMessage `json:"value"` // [unix time, "value as string"]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("query %q: decode: %w", promql, err)
	}
	if pr.Status != "success" || pr.Data.ResultType != "vector" || len(pr.Data.Result) > 1 {
		return nil, fmt.Errorf("query %q: status %q, %s with %d series; want one-series vector",
			promql, pr.Status, pr.Data.ResultType, len(pr.Data.Result))
	}
	if len(pr.Data.Result) == 0 {
		return nil, nil
	}
	var str string
	if err := json.Unmarshal(pr.Data.Result[0].Value[1], &str); err != nil {
		return nil, fmt.Errorf("query %q: sample value: %w", promql, err)
	}
	// ParseFloat accepts "NaN", "+Inf", "-Inf", which Prometheus does send.
	v, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return nil, fmt.Errorf("query %q: sample value: %w", promql, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, nil
	}
	return &v, nil
}

func round1(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := math.Round(*v*10) / 10
	return &r
}

func toInt(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(math.Round(*v))
	return &n
}

func isOne(v *float64) *bool {
	if v == nil {
		return nil
	}
	b := *v == 1
	return &b
}
