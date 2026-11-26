package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// UnmatchedRoute labels requests that matched no route (404s for arbitrary paths), so
// scanners cannot create one time series per probed URL.
const UnmatchedRoute = "unmatched"

// PoolStats is a snapshot of the database connection pool.
type PoolStats struct {
	Acquired int32 // connections checked out by a request
	Idle     int32 // open connections waiting in the pool
	Total    int32 // open connections (acquired + idle + constructing)
	Max      int32 // configured maximum
}

// PoolStatsFunc returns the current pool snapshot (pgxpool.Pool.Stat adapted in main).
type PoolStatsFunc func() PoolStats

// Metrics owns the Prometheus registry served on /metrics. It uses its own registry,
// not the global default, so tests and multiple routers never collide.
type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the HTTP collectors plus the Go runtime and process collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iacp", Subsystem: "http", Name: "requests_total",
			Help: "HTTP requests by method, chi route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "iacp", Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP request latency by method and chi route pattern.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route"}),
	}
	m.registry.MustRegister(m.requests, m.duration,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// ObserveRequest records one finished request. route must be a route pattern
// (RouteLabel), never the raw path; method is bounded here with MethodLabel.
func (m *Metrics) ObserveRequest(method, route string, status int, elapsed time.Duration) {
	method = MethodLabel(method)
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

// RegisterPool exposes the connection pool as gauges, read at scrape time.
func (m *Metrics) RegisterPool(stats PoolStatsFunc) {
	m.registry.MustRegister(newPoolCollector(stats))
}

// Handler serves the registry in the Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// RouteLabel maps a chi route pattern to a bounded label value. Pure.
func RouteLabel(pattern string) string {
	if pattern == "" {
		return UnmatchedRoute
	}
	return pattern
}

// OtherMethod labels every request method outside the standard set.
const OtherMethod = "OTHER"

// MethodLabel maps a request method to a bounded label value: net/http accepts any
// token as a method, so anything but the standard API methods becomes OTHER. Pure.
func MethodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return OtherMethod
}

// poolCollector reports PoolStats as iacp_db_pool_connections{state=...}.
type poolCollector struct {
	stats PoolStatsFunc
	desc  *prometheus.Desc
	max   *prometheus.Desc
}

func newPoolCollector(stats PoolStatsFunc) *poolCollector {
	return &poolCollector{
		stats: stats,
		desc: prometheus.NewDesc("iacp_db_pool_connections",
			"Database pool connections by state (acquired, idle, total).", []string{"state"}, nil),
		max: prometheus.NewDesc("iacp_db_pool_max_connections",
			"Configured maximum size of the database pool.", nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
	ch <- c.max
}

// Collect implements prometheus.Collector.
func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	for state, v := range map[string]int32{"acquired": s.Acquired, "idle": s.Idle, "total": s.Total} {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(v), state)
	}
	ch <- prometheus.MustNewConstMetric(c.max, prometheus.GaugeValue, float64(s.Max))
}
