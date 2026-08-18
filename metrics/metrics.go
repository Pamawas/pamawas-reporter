package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the reporter service
type Metrics struct {
	ReportsGeneratedTotal  prometheus.Counter
	DeliveryTotal          *prometheus.CounterVec
	TemplateRenderDuration prometheus.Histogram
	DBConnectionErrors     prometheus.Counter
	LastSentTimestamp      prometheus.Gauge
	ReporterRunning        prometheus.Gauge
}

// NewMetrics creates and registers all metrics
func NewMetrics() *Metrics {
	return &Metrics{
		ReportsGeneratedTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "reporter_reports_generated_total",
				Help: "Total number of reports generated",
			},
		),
		DeliveryTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "reporter_delivery_total",
				Help: "Total number of report deliveries",
			},
			[]string{"channel", "status"},
		),
		TemplateRenderDuration: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "reporter_template_render_duration_seconds",
				Help:    "Template render duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
		),
		DBConnectionErrors: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "reporter_db_connection_errors_total",
				Help: "Total number of database connection errors",
			},
		),
		LastSentTimestamp: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "reporter_last_sent_timestamp_seconds",
				Help: "Timestamp of last report sent",
			},
		),
		ReporterRunning: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "reporter_running",
				Help: "Whether reporter is currently running (1) or not (0)",
			},
		),
	}
}
