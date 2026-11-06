package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Pamawas/pamawas-reporter/fetcher"
	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

func testMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		ReportsGeneratedTotal:  prometheus.NewCounter(prometheus.CounterOpts{Name: "test_reports_total"}),
		DeliveryTotal:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_delivery_total"}, []string{"channel", "status"}),
		TemplateRenderDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_render_seconds"}),
		DBConnectionErrors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_db_errors_total"}),
		LastSentTimestamp:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_last_sent"}),
		ReporterRunning:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_running"}),
	}
}

func newMockReporter(t *testing.T, cfg ReporterConfig) (*Reporter, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("Failed to close database: %v", closeErr)
		}
	})
	return NewReporter(db, cfg, testMetrics()), mock
}

func TestNewReporterAndStop(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{})
	if r.httpClient.Timeout != 10*time.Second || r.StartTime().IsZero() || r.Running() || !r.LastSent().IsZero() {
		t.Fatalf("unexpected reporter state: %+v", r)
	}
	r.Stop()
}

func TestStartWorkerStopsWhenContextCanceled(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{ReportInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx, r.cancelFunc = ctx, cancel
	done := make(chan struct{})
	go func() { r.StartWorker(); close(done) }()
	r.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestGetRecentIncidents(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	started := time.Now().Add(-time.Hour)
	mock.ExpectQuery("SELECT i.id").WithArgs(sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"id", "title", "status", "started_at", "resolved_at", "severity", "affected_services"}).
			AddRow("incident-123", "CPU high", "firing", started, time.Time{}, "high", []byte(`["api","db"]`)),
	)
	incidents, err := r.getRecentIncidents(24 * time.Hour)
	if err != nil || len(incidents) != 1 {
		t.Fatalf("getRecentIncidents() = %#v, %v", incidents, err)
	}
	if got := incidents[0].AffectedServices; len(got) != 2 || got[0] != "api" {
		t.Fatalf("affected services = %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetRecentIncidentsRejectsInvalidServicesJSON(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	mock.ExpectQuery("SELECT i.id").WillReturnRows(
		sqlmock.NewRows([]string{"id", "title", "status", "started_at", "resolved_at", "severity", "affected_services"}).
			AddRow("incident-123", "CPU high", "firing", time.Now(), time.Time{}, "high", []byte(`nope`)),
	)
	if _, err := r.getRecentIncidents(time.Hour); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestGetEvidenceForIncident(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	mock.ExpectQuery("SELECT id, incident_id").WithArgs("incident-123").WillReturnRows(
		sqlmock.NewRows([]string{"id", "incident_id", "type", "content", "source", "confidence"}).
			AddRow("e1", "incident-123", "fact", "CPU saturated", "prometheus", 0.9),
	)
	got, err := r.getEvidenceForIncident("incident-123")
	if err != nil || len(got) != 1 || got[0].Confidence != 0.9 {
		t.Fatalf("getEvidenceForIncident() = %#v, %v", got, err)
	}
}

func TestGenerateDailyReportTemplate(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{ReportTemplate: "total={{TotalIncidents}} firing={{NeedsAttention}} resolved={{Recovered}} evidence={{Evidence}}"})
	mock.ExpectQuery("SELECT id, incident_id").WithArgs("incident-123").WillReturnRows(sqlmock.NewRows([]string{"id", "incident_id", "type", "content", "source", "confidence"}))
	content := r.generateDailyReport([]models.Incident{{ID: "incident-123", Status: "firing"}, {ID: "incident-456", Status: "resolved"}})
	if !strings.Contains(content, "total=2 firing=1 resolved=1") {
		t.Fatalf("content = %q", content)
	}
}

func TestGenerateDailyReportDefaultIncludesEvidence(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	for _, id := range []string{"incident-123", "incident-456"} {
		rows := sqlmock.NewRows([]string{"id", "incident_id", "type", "content", "source", "confidence"})
		if id == "incident-123" {
			rows.AddRow("e1", id, "fact", "CPU saturated", "prometheus", 0.95)
		}
		mock.ExpectQuery("SELECT id, incident_id").WithArgs(id).WillReturnRows(rows)
	}
	content := r.generateDailyReport([]models.Incident{
		{ID: "incident-123", Title: "CPU high", Status: "firing", Severity: "high", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), AffectedServices: []string{"api"}},
		{ID: "incident-456", Title: "Recovered", Status: "resolved", Severity: "warning", StartedAt: time.Now()},
	})
	for _, want := range []string{"2 incidents occurred", "1 needs your attention", "1 recovered", "incident", "CPU saturated", "[FACT] (95%)"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q: %s", want, content)
		}
	}
}

func TestSendToDiscord(t *testing.T) {
	var payload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", req.Method, req.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	r, _ := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	if err := r.sendToDiscord(context.Background(), models.Report{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if payload["content"] != "hello" || payload["username"] != "Pamawas Reporter" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSendToDiscordReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	r, _ := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	if err := r.sendToDiscord(context.Background(), models.Report{}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendToTelegram(t *testing.T) {
	var req *http.Request
	var payload map[string]interface{}
	r, _ := newMockReporter(t, ReporterConfig{TelegramBotToken: "token", TelegramChatID: "chat"})
	r.httpClient.Transport = roundTripperFunc(func(rq *http.Request) (*http.Response, error) {
		req = rq
		if err := json.NewDecoder(rq.Body).Decode(&payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	if err := r.sendToTelegram(context.Background(), models.Report{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://api.telegram.org/bottoken/sendMessage" || payload["chat_id"] != "chat" || payload["parse_mode"] != "Markdown" {
		t.Fatalf("request=%v payload=%#v", req.URL, payload)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendReportReturnsDeliveryError(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{DiscordWebhookURL: "http://discord.invalid"})
	r.httpClient.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("delivery failed") })
	if err := r.sendReport(models.Report{}); err == nil || !strings.Contains(err.Error(), "delivery failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestSaveReport(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	report := models.Report{ID: "r1", IncidentID: "i1", Content: "body", SentAt: time.Now(), Channels: []string{"discord"}}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO reports (id, incident_id, content, sent_at, channels) VALUES ($1,$2,$3,$4,$5)")).
		WithArgs(report.ID, report.IncidentID, report.Content, report.SentAt, []byte(`["discord"]`)).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := r.saveReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateAndSendDailyReportNoIncidents(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	mock.ExpectQuery("SELECT i.id").WillReturnRows(sqlmock.NewRows([]string{"id", "title", "status", "started_at", "resolved_at", "severity", "affected_services"}))
	if err := r.GenerateAndSendDailyReport(); err != nil {
		t.Fatal(err)
	}
	if r.Running() {
		t.Fatal("reporter left running")
	}
}

func TestGenerateAndSendDailyReportAlreadyRunning(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{})
	r.running = true
	if err := r.GenerateAndSendDailyReport(); err != nil {
		t.Fatal(err)
	}
}

// Mock fetchers for testing
type mockMetricsFetcher struct {
	Metrics []fetcher.Metric
	Err     error
}

func (m *mockMetricsFetcher) Fetch(ctx context.Context, incidentID string) ([]fetcher.Metric, error) {
	return m.Metrics, m.Err
}

type mockLogsFetcher struct {
	Logs []fetcher.LogEntry
	Err  error
}

func (l *mockLogsFetcher) Fetch(ctx context.Context, incidentID string) ([]fetcher.LogEntry, error) {
	return l.Logs, l.Err
}

func TestEnrichEvidenceNoEvidence(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	r.SetMetricsFetcher(&mockMetricsFetcher{})
	r.SetLogsFetcher(&mockLogsFetcher{})
	mock.ExpectQuery("SELECT e.id, e.version, e.metrics, e.logs").
		WithArgs("incident-123").
		WillReturnError(sql.ErrNoRows)
	err := r.EnrichEvidence(context.Background(), "incident-123")
	if err != nil {
		t.Fatalf("EnrichEvidence() error = %v", err)
	}
}

func TestEnrichEvidenceWithMetrics(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	// Initialize proper metrics with FetchDuration histogram
	r.metrics = &metrics.Metrics{
		ReportsGeneratedTotal:  prometheus.NewCounter(prometheus.CounterOpts{Name: "test_reports_total"}),
		DeliveryTotal:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_delivery_total"}, []string{"channel", "status"}),
		TemplateRenderDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "test_render_seconds"}),
		DBConnectionErrors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "test_db_errors_total"}),
		LastSentTimestamp:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_last_sent"}),
		ReporterRunning:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_running"}),
		FetchDuration:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "pamawas_fetch_duration_seconds"}, []string{"type"}),
	}

	mockMetrics := &mockMetricsFetcher{
		Metrics: []fetcher.Metric{
			{Name: "http_requests_total", Value: 42.5, Labels: map[string]string{"method": "GET"}, At: time.Now()},
		},
	}
	r.SetMetricsFetcher(mockMetrics)
	r.SetLogsFetcher(&mockLogsFetcher{})

	mock.ExpectQuery("SELECT e.id, e.version, e.metrics, e.logs").
		WithArgs("incident-123").
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "metrics", "logs"}).
			AddRow("evidence-1", 0, "[]", "[]"))
	mock.ExpectExec("UPDATE evidence").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := r.EnrichEvidence(context.Background(), "incident-123")
	if err != nil {
		t.Fatalf("EnrichEvidence() error = %v", err)
	}
}

func TestMergeMetricsDeduplication(t *testing.T) {
	existing := []models.Metric{
		{Name: "http_requests", Value: 1, Labels: map[string]string{"method": "GET"}, At: time.Now().Add(-time.Hour)},
	}
	newMetrics := []models.Metric{
		{Name: "http_requests", Value: 1, Labels: map[string]string{"method": "GET"}, At: time.Now().Add(-time.Hour)}, // duplicate
		{Name: "cpu_usage", Value: 50.0, Labels: map[string]string{}, At: time.Now()},
	}
	merged := mergeMetrics(existing, newMetrics)
	if len(merged) != 2 {
		t.Fatalf("mergeMetrics() returned %d metrics, want 2", len(merged))
	}
}

func TestMergeLogsDeduplication(t *testing.T) {
	existing := []models.LogEntry{
		{Timestamp: time.Now().Add(-time.Hour), Stream: "app", Line: "Request completed"},
	}
	newLogs := []models.LogEntry{
		{Timestamp: time.Now().Add(-time.Hour), Stream: "app", Line: "Request completed"}, // duplicate
		{Timestamp: time.Now(), Stream: "app", Line: "New log"},
	}
	merged := mergeLogs(existing, newLogs)
	if len(merged) != 2 {
		t.Fatalf("mergeLogs() returned %d logs, want 2", len(merged))
	}
}
