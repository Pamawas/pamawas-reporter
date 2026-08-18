package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Pamawas/pamawas-reporter/config"
	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

func handlerMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		ReportsGeneratedTotal:  prometheus.NewCounter(prometheus.CounterOpts{Name: "handler_reports_total"}),
		DeliveryTotal:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "handler_delivery_total"}, []string{"channel", "status"}),
		TemplateRenderDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "handler_render_seconds"}),
		DBConnectionErrors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "handler_db_errors_total"}),
		LastSentTimestamp:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "handler_last_sent"}),
		ReporterRunning:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "handler_running"}),
	}
}

func newTestHandler(t *testing.T) (*Handler, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("Failed to close database: %v", closeErr)
		}
	})
	return NewHandler(db, config.Config{ReportInterval: time.Hour}, handlerMetrics()), mock
}

func TestNewHandlerExposesReporter(t *testing.T) {
	h, _ := newTestHandler(t)
	if h.Reporter() == nil {
		t.Fatal("Reporter returned nil")
	}
}

func TestHandlersRejectWrongMethods(t *testing.T) {
	h, _ := newTestHandler(t)
	cases := []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"health", h.HealthHandler}, {"ready", h.ReadyHandler}, {"report", h.ReportHandler}, {"status", h.StatusHandler},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			method := http.MethodPost
			if tc.name == "report" {
				method = http.MethodGet
			}
			req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
			tc.fn(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d", rr.Code)
			}
		})
	}
}

func TestHealthHandlerHealthy(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectPing()
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	h.HealthHandler(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q", rr.Code, rr.Header().Get("Content-Type"))
	}
	var response models.HealthResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "healthy" || response.Version != "1.0.0" || response.Timestamp.IsZero() {
		t.Fatalf("response = %+v", response)
	}
}

func TestHealthHandlerUnhealthyIncrementsMetric(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectPing().WillReturnError(errors.New("db down"))
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	h.HealthHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable || testutil.ToFloat64(h.metrics.DBConnectionErrors) != 1 {
		t.Fatalf("status=%d metric=%v", rr.Code, testutil.ToFloat64(h.metrics.DBConnectionErrors))
	}
	var response models.HealthResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "unhealthy" || response.Error == "" {
		t.Fatalf("response = %+v", response)
	}
}

func TestReadyHandler(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		h, mock := newTestHandler(t)
		mock.ExpectPing()
		rr := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil)
		h.ReadyHandler(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "{\"status\":\"ready\"}\n" {
			t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
		}
	})
	t.Run("not ready", func(t *testing.T) {
		h, mock := newTestHandler(t)
		mock.ExpectPing().WillReturnError(errors.New("offline"))
		rr := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ready", nil)
		h.ReadyHandler(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", rr.Code)
		}
	})
}

func TestReportHandlerNoIncidents(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery("SELECT i.id").WillReturnRows(sqlmock.NewRows([]string{"id", "title", "status", "started_at", "resolved_at", "severity", "affected_services"}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", nil)
	h.ReportHandler(rr, req)
	if rr.Code != http.StatusAccepted || rr.Body.String() != "{\"message\":\"Daily report triggered successfully\"}\n" {
		t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
	}
}

func TestReportHandlerDatabaseError(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery("SELECT i.id").WillReturnError(errors.New("query failed"))
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/report", nil)
	h.ReportHandler(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestStatusAndMetricsHandlers(t *testing.T) {
	h, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status", nil)
	h.StatusHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var status map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["version"] != "1.0.0" || status["uptime"] == "" {
		t.Fatalf("status = %#v", status)
	}
	if h.MetricsHandler() == nil {
		t.Fatal("MetricsHandler returned nil")
	}
}
