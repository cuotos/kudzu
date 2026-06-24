// Package observability provides Prometheus metrics for Kudzu, including a
// collector that reports each gate's live state at scrape time.
package observability

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bloomandwild/kudzu/internal/gate"
)

// GateLister is the subset of gate.Service the collector needs.
type GateLister interface {
	List(ctx context.Context) ([]gate.Gate, error)
}

// Metrics holds the registry and HTTP instruments.
type Metrics struct {
	reg  *prometheus.Registry
	reqs *prometheus.CounterVec
	dur  *prometheus.HistogramVec
}

// New builds the metrics registry, registering Go/process collectors, the HTTP
// instruments, and a live gate-state collector backed by lister.
func New(lister GateLister, log *slog.Logger) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		reg: reg,
		reqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kudzu_http_requests_total",
			Help: "Total HTTP requests by route and status.",
		}, []string{"method", "route", "status"}),
		dur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kudzu_http_request_duration_seconds",
			Help:    "HTTP request duration by route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.reqs, m.dur, newGateCollector(lister, log))
	return m
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Observe records a completed HTTP request.
func (m *Metrics) Observe(method, route string, status int, d time.Duration) {
	m.reqs.WithLabelValues(method, route, statusClass(status)).Inc()
	m.dur.WithLabelValues(method, route).Observe(d.Seconds())
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// gateCollector reports kudzu_gate_allowed{service,env} (1 open / 0 blocked)
// and kudzu_gate_state{service,env,state} (1 for the current state) on scrape.
type gateCollector struct {
	lister      GateLister
	log         *slog.Logger
	allowedDesc *prometheus.Desc
	stateDesc   *prometheus.Desc
}

func newGateCollector(lister GateLister, log *slog.Logger) *gateCollector {
	if log == nil {
		log = slog.Default()
	}
	return &gateCollector{
		lister: lister,
		log:    log,
		allowedDesc: prometheus.NewDesc("kudzu_gate_allowed",
			"1 if the gate is open (deploys allowed), 0 otherwise.",
			[]string{"service", "env"}, nil),
		stateDesc: prometheus.NewDesc("kudzu_gate_state",
			"Current gate state, 1 for the active state.",
			[]string{"service", "env", "state"}, nil),
	}
}

func (c *gateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.allowedDesc
	ch <- c.stateDesc
}

func (c *gateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gates, err := c.lister.List(ctx)
	if err != nil {
		c.log.Warn("gate metrics collection failed", "err", err)
		return
	}
	for _, g := range gates {
		allowed := 0.0
		if g.Allowed {
			allowed = 1
		}
		ch <- prometheus.MustNewConstMetric(c.allowedDesc, prometheus.GaugeValue, allowed, g.Service, g.Env)
		ch <- prometheus.MustNewConstMetric(c.stateDesc, prometheus.GaugeValue, 1, g.Service, g.Env, string(g.State))
	}
}
