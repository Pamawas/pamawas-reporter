package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the reporter service
type Metrics struct {
	ReportsGeneratedTotal prometheus.Counter
	DeliveryTotal         *prometheus.CounterVec
	DBConnectionErrors    prometheus.Counter
	FetchDuration         *prometheus.HistogramVec
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
		DBConnectionErrors: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "reporter_db_connection_errors_total",
				Help: "Total number of database connection errors",
			},
		),
		FetchDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "pamawas_fetch_duration_seconds",
				Help:    "Duration of Prometheus/Loki fetch operations in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"type"},
		),
	}
}
