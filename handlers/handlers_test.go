package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
		ReportsGeneratedTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "handler_reports_total"}),
		DeliveryTotal:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "handler_delivery_total"}, []string{"channel", "status"}),
		DBConnectionErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "handler_db_errors_total"}),
		FetchDuration:         prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "pamawas_fetch_duration_seconds"}, []string{"type"}),
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
	return NewHandler(db, config.Config{}, handlerMetrics()), mock
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

func TestReportHandlerInvalidBody(t *testing.T) {
	h, _ := newTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/reports", nil)
	h.ReportHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestReportHandlerDatabaseError(t *testing.T) {
	h, mock := newTestHandler(t)
	// claimReportRequest issues UPDATE report_requests ... RETURNING id (no row),
	// then falls back to SELECT status FROM report_requests (also no row) and errors.
	mock.ExpectQuery("UPDATE report_requests").WithArgs("rreq_x").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT status FROM report_requests").WithArgs("rreq_x").WillReturnRows(sqlmock.NewRows([]string{"status"}))
	rr := httptest.NewRecorder()
	body := `{"contract_version":1,"request_id":"rreq_x","report_type":"daily","period_start":"2026-08-16T00:00:00Z","period_end":"2026-08-17T00:00:00Z","timezone":"Asia/Jakarta","incident_ids":[]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/reports", strings.NewReader(body))
	h.ReportHandler(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
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

// TestIncidentFindingsHandlerServesTheRoute exercises the real mux pattern, so
// a broken route registration fails here rather than in production.
func TestIncidentFindingsHandlerServesTheRoute(t *testing.T) {
	h, mock := newTestHandler(t)

	started := time.Date(2026, 11, 16, 1, 37, 20, 0, time.UTC)
	mock.ExpectQuery("SELECT id, title, status, started_at").
		WithArgs("inc_route").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "last_event_at", "resolved_at",
			"severity", "environment", "affected_services", "correlation_policy",
			"correlation_version", "created_at", "updated_at",
		}).AddRow("inc_route", "Monitoring Target Down", "open", started, started, nil,
			"critical", "production", "{last-labs}", "pamawas-correlation-v1", 1, started, started))
	mock.ExpectQuery("FROM investigation_runs").
		WithArgs("inc_route").
		WillReturnError(sql.ErrNoRows)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/incidents/{id}/findings", h.IncidentFindingsHandler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/incidents/inc_route/findings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Incident struct {
			ID string `json:"id"`
		} `json:"incident"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Incident.ID != "inc_route" {
		t.Errorf("incident id = %q", body.Incident.ID)
	}
	if body.Findings == nil {
		t.Error("findings must be an empty array, not null")
	}
}

func TestIncidentFindingsHandlerReturns404ForUnknownIncident(t *testing.T) {
	h, mock := newTestHandler(t)
	mock.ExpectQuery("SELECT id, title, status, started_at").
		WithArgs("inc_missing").
		WillReturnError(sql.ErrNoRows)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/incidents/{id}/findings", h.IncidentFindingsHandler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/incidents/inc_missing/findings", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
