package telemetry

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns MailWisp's bounded-cardinality Prometheus registry.
type Metrics struct {
	registry            *prometheus.Registry
	httpRequests        *prometheus.CounterVec
	httpDuration        *prometheus.HistogramVec
	lmtpActive          prometheus.Gauge
	lmtpRejected        prometheus.Counter
	lmtpDeliveries      *prometheus.CounterVec
	lmtpQuotaRejected   *prometheus.CounterVec
	lmtpStorageRejected *prometheus.CounterVec
	parserRuns          *prometheus.CounterVec
	parserDuration      *prometheus.HistogramVec
	retentionSweeps     *prometheus.CounterVec
	retentionDeleted    *prometheus.CounterVec
}

// NewMetrics creates an isolated registry without global mutable collectors.
func NewMetrics(pool *pgxpool.Pool) *Metrics {
	metrics := &Metrics{
		registry:            prometheus.NewRegistry(),
		httpRequests:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_http_requests_total", Help: "Completed HTTP requests."}, []string{"method", "route", "status"}),
		httpDuration:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "mailwisp_http_request_duration_seconds", Help: "HTTP request duration.", Buckets: prometheus.DefBuckets}, []string{"method", "route"}),
		lmtpActive:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "mailwisp_lmtp_sessions_active", Help: "Currently active LMTP sessions."}),
		lmtpRejected:        prometheus.NewCounter(prometheus.CounterOpts{Name: "mailwisp_lmtp_sessions_rejected_total", Help: "LMTP sessions rejected by the concurrency limit."}),
		lmtpDeliveries:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_lmtp_deliveries_total", Help: "LMTP delivery outcomes by SMTP status class."}, []string{"result"}),
		lmtpQuotaRejected:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_lmtp_quota_rejections_total", Help: "LMTP recipient and delivery rejections by bounded quota reason."}, []string{"reason"}),
		lmtpStorageRejected: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_lmtp_storage_rejections_total", Help: "LMTP storage admission rejections by bounded reason."}, []string{"reason"}),
		parserRuns:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_parser_runs_total", Help: "MIME parser work outcomes."}, []string{"result"}),
		parserDuration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "mailwisp_parser_duration_seconds", Help: "MIME parser work duration.", Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}}, []string{"result"}),
		retentionSweeps:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_retention_sweeps_total", Help: "Retention sweep outcomes."}, []string{"result"}),
		retentionDeleted:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "mailwisp_retention_deleted_total", Help: "Objects deleted by retention."}, []string{"kind"}),
	}
	metrics.registry.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.httpRequests, metrics.httpDuration, metrics.lmtpActive, metrics.lmtpRejected,
		metrics.lmtpDeliveries, metrics.lmtpQuotaRejected, metrics.lmtpStorageRejected, metrics.parserRuns, metrics.parserDuration, metrics.retentionSweeps, metrics.retentionDeleted,
	)
	if pool != nil {
		metrics.registerPostgresPool(pool)
	}
	return metrics
}

// Handler exposes this isolated registry in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// ObserveHTTPRequest records one bounded route pattern, never a raw URL.
func (m *Metrics) ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

// LMTPSessionOpened records one accepted active session.
func (m *Metrics) LMTPSessionOpened() { m.lmtpActive.Inc() }

// LMTPSessionClosed records one completed active session.
func (m *Metrics) LMTPSessionClosed() { m.lmtpActive.Dec() }

// LMTPSessionRejected records admission-control overload.
func (m *Metrics) LMTPSessionRejected() { m.lmtpRejected.Inc() }

// ObserveLMTPDelivery records one stable SMTP status class.
func (m *Metrics) ObserveLMTPDelivery(status int) {
	result := "temporary_failure"
	if status >= 200 && status < 300 {
		result = "success"
	} else if status >= 500 {
		result = "permanent_failure"
	}
	m.lmtpDeliveries.WithLabelValues(result).Inc()
}

// ObserveLMTPQuotaRejected records one fixed Inbox quota reason.
func (m *Metrics) ObserveLMTPQuotaRejected(reason string) {
	if reason != "messages" && reason != "storage_bytes" {
		return
	}
	m.lmtpQuotaRejected.WithLabelValues(reason).Inc()
}

// ObserveLMTPStorageRejected records one fixed storage admission reason.
func (m *Metrics) ObserveLMTPStorageRejected(reason string) {
	if reason != "capacity" && reason != "check_error" {
		return
	}
	m.lmtpStorageRejected.WithLabelValues(reason).Inc()
}

// ObserveParser records one fixed parser result label.
func (m *Metrics) ObserveParser(result string, duration time.Duration) {
	m.parserRuns.WithLabelValues(result).Inc()
	m.parserDuration.WithLabelValues(result).Observe(duration.Seconds())
}

// ObserveRetention records one sweep and its bounded deletion counts.
func (m *Metrics) ObserveRetention(result string, inboxes, content int) {
	m.retentionSweeps.WithLabelValues(result).Inc()
	if inboxes > 0 {
		m.retentionDeleted.WithLabelValues("inbox").Add(float64(inboxes))
	}
	if content > 0 {
		m.retentionDeleted.WithLabelValues("content").Add(float64(content))
	}
}

func (m *Metrics) registerPostgresPool(pool *pgxpool.Pool) {
	gauges := []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "mailwisp_postgres_pool_max_connections", Help: "Configured PostgreSQL pool connection limit."}, func() float64 { return float64(pool.Config().MaxConns) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "mailwisp_postgres_pool_connections", Help: "Current PostgreSQL pool connections."}, func() float64 { return float64(pool.Stat().TotalConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "mailwisp_postgres_pool_acquired", Help: "Currently acquired PostgreSQL connections."}, func() float64 { return float64(pool.Stat().AcquiredConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "mailwisp_postgres_pool_idle", Help: "Currently idle PostgreSQL connections."}, func() float64 { return float64(pool.Stat().IdleConns()) }),
	}
	m.registry.MustRegister(gauges...)
}
