package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

func testMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		ReportsGeneratedTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_reports_total"}),
		DeliveryTotal:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_delivery_total"}, []string{"channel", "status"}),
		DBConnectionErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "test_db_errors_total"}),
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
	// Tests construct ReporterConfig{} literals; give the circuit breakers a
	// valid budget so they behave like production defaults in assertions.
	if cfg.CircuitBreakerFailureThreshold == 0 {
		cfg.CircuitBreakerFailureThreshold = 3
	}
	if cfg.CircuitBreakerResetTimeout == 0 {
		cfg.CircuitBreakerResetTimeout = 30 * time.Second
	}
	return NewReporter(db, cfg, testMetrics()), mock
}

func TestNewReporterAndStop(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{})
	if r.httpClient.Timeout != 10*time.Second || r.StartTime().IsZero() {
		t.Fatalf("unexpected reporter state: %+v", r)
	}
	if err := r.ctx.Err(); err != nil {
		t.Fatalf("reporter context is canceled before Stop: %v", err)
	}
	r.Stop()
	if err := r.ctx.Err(); err != context.Canceled {
		t.Fatalf("reporter context error = %v, want context.Canceled", err)
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
	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	// A report with no incidents still produces the summary card.
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("rpt-1").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}))
	mock.ExpectQuery("SELECT incident_id, run_id").
		WithArgs("rpt-1").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "run_id"}))
	if err := r.sendToDiscord(context.Background(), models.Report{ID: "rpt-1"}); err != nil {
		t.Fatal(err)
	}
	embeds, ok := payload["embeds"].([]interface{})
	if !ok || len(embeds) != 1 || payload["username"] != "Pamawas Reporter" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSendToDiscordReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("rpt-1").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}))
	mock.ExpectQuery("SELECT incident_id, run_id").
		WithArgs("rpt-1").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "run_id"}))
	if err := r.sendToDiscord(context.Background(), models.Report{ID: "rpt-1"}); err == nil || !strings.Contains(err.Error(), "502") {
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

// TestLoadIncidentFindingsReturnsCauseAndActions covers the on-demand read.
//
// Investigations run for every incident regardless of severity; this is how a
// reader gets the full record rather than the one-line summary the digest
// carries.
func TestLoadIncidentFindingsReturnsCauseAndActions(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})

	started := time.Date(2026, 11, 16, 1, 37, 20, 0, time.UTC)
	mock.ExpectQuery("SELECT id, title, status, started_at").
		WithArgs("inc_1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "last_event_at", "resolved_at",
			"severity", "environment", "affected_services", "correlation_policy",
			"correlation_version", "created_at", "updated_at",
		}).AddRow("inc_1", "Monitoring Target Down", "open", started, started, nil,
			"critical", "production", "{last-labs}", "pamawas-correlation-v1", 1, started, started))

	mock.ExpectQuery("SELECT id, status, started_at, completed_at FROM investigation_runs").
		WithArgs("inc_1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "started_at", "completed_at"}).
			AddRow("irun_1", "completed", started, started.Add(4*time.Minute)))

	mock.ExpectQuery("FROM evidence e").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "incident_id", "run_id", "type", "content", "source", "source_detail",
			"confidence", "supports_evidence", "contradicts_evidence", "ordinal",
		}).
			AddRow("ev_1", "inc_1", "irun_1", "likely_cause", "netbird tunnel dropped", "llm",
				"prometheus up + loki docker_logs", 0.85, "{}", "{}", 0).
			AddRow("ev_2", "inc_1", "irun_1", "recommendation", "Restart netbird-agent", "llm",
				"", 0.8, "{}", "{}", 1))

	got, err := r.LoadIncidentFindings(context.Background(), "inc_1")
	require.NoError(t, err)

	require.Equal(t, "inc_1", got.Incident.ID)
	require.NotNil(t, got.Run)
	require.Equal(t, "completed", got.Run.Status)
	require.Len(t, got.Findings, 2)
	require.Equal(t, "netbird tunnel dropped", got.Summary)
	require.Equal(t, []string{"Restart netbird-agent"}, got.Actions)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestLoadIncidentFindingsReportsAnUnknownIncident keeps 404 distinct from
// "investigated, found nothing". A 200 with an empty findings list would tell
// the caller the investigation ran and learned nothing.
func TestLoadIncidentFindingsReportsAnUnknownIncident(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})

	mock.ExpectQuery("SELECT id, title, status, started_at").
		WithArgs("inc_missing").
		WillReturnError(sql.ErrNoRows)

	_, err := r.LoadIncidentFindings(context.Background(), "inc_missing")
	require.ErrorIs(t, err, ErrIncidentNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestLoadIncidentFindingsSurvivesAMissingInvestigation returns the incident
// with no run rather than an error: "the investigation never ran" is a fact
// the caller can act on.
func TestLoadIncidentFindingsSurvivesAMissingInvestigation(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})

	started := time.Date(2026, 11, 16, 1, 37, 20, 0, time.UTC)
	mock.ExpectQuery("SELECT id, title, status, started_at").
		WithArgs("inc_2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "last_event_at", "resolved_at",
			"severity", "environment", "affected_services", "correlation_policy",
			"correlation_version", "created_at", "updated_at",
		}).AddRow("inc_2", "CPU", "open", started, started, nil,
			"warning", "production", "{kube-2}", "pamawas-correlation-v1", 1, started, started))

	mock.ExpectQuery("FROM investigation_runs").
		WithArgs("inc_2").
		WillReturnError(sql.ErrNoRows)

	got, err := r.LoadIncidentFindings(context.Background(), "inc_2")
	require.NoError(t, err)
	require.Nil(t, got.Run)
	require.Empty(t, got.Findings)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLookupFallbackAffected(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})

	mock.ExpectQuery("SELECT DISTINCT val FROM incident_events").
		WithArgs("inc_crash").
		WillReturnRows(sqlmock.NewRows([]string{"val"}).AddRow("pamawas-ingest-pod-1"))

	result := r.lookupFallbackAffected(context.Background(), "inc_crash")
	require.Equal(t, []string{"pamawas-ingest-pod-1"}, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSelectInvestigationRunsPinsOnlyCompletedFullRuns(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	snapshot := time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)
	mock.ExpectQuery("SELECT DISTINCT ON \\(incident_id\\)").
		WithArgs(sqlmock.AnyArg(), snapshot).
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "id"}).
			AddRow("inc_1", "irun_investigation"))

	selected, err := r.selectInvestigationRuns(
		context.Background(), []string{"inc_1"}, snapshot,
	)

	require.NoError(t, err)
	require.Equal(t, map[string]string{"inc_1": "irun_investigation"}, selected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadEvidenceForRunsNeverMixesAnotherRun(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	mock.ExpectQuery("WHERE e.run_id = ANY").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "incident_id", "run_id", "type", "content", "source", "source_detail",
			"confidence", "supports_evidence", "contradicts_evidence", "ordinal",
		}).AddRow(
			"ev_1", "inc_1", "irun_investigation", "hypothesis",
			"A dependency timeout is plausible.", "llm", "synthesis", 0.5,
			"{ev_fact}", "{}", 0,
		))

	evidence, err := r.loadEvidenceForRuns(
		context.Background(), map[string]string{"inc_1": "irun_investigation"},
	)

	require.NoError(t, err)
	require.Len(t, evidence["inc_1"], 1)
	require.Equal(t, "irun_investigation", evidence["inc_1"][0].RunID)
	require.Equal(t, []string{"ev_fact"}, evidence["inc_1"][0].Supports)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPersistReportStoresPinnedRunID(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{})
	now := time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)
	report := models.Report{
		ID: "rpt_1", RequestID: "rreq_1", ReportType: "daily",
		PeriodStart: now.Add(-24 * time.Hour), PeriodEnd: now,
		Timezone: "Asia/Jakarta", TemplateVersion: "pamawas-report-v2",
		Content: "report", GeneratedAt: now, Status: models.ReportStatusGenerated,
		CreatedAt: now,
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO reports").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO report_incidents").
		WithArgs("rpt_1", "inc_1", models.InclusionReasonOngoing, "irun_1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := r.persistReportWithIncidents(
		context.Background(), report, []string{"inc_1"},
		map[string]string{"inc_1": models.InclusionReasonOngoing},
		map[string]string{"inc_1": "irun_1"},
	)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPromptEchoUsesDeterministicSummaryFallback(t *testing.T) {
	incidents := []models.Incident{
		{Title: "Database Down", Status: "open", Severity: "critical", AffectedServices: []string{"db-a"}},
		{Title: "CPU", Status: "resolved", Severity: "warning"},
	}
	require.True(t, looksLikeSummaryPromptEcho(
		"We need to summarize the user's request. Use only the facts given and no headings.",
	))
	fallback := deterministicDailySummary(incidents)
	require.Contains(t, fallback, "2 incidents were recorded")
	require.Contains(t, fallback, "Database Down affecting db-a")
	require.NotContains(t, strings.ToLower(fallback), "user")
}

func TestReportHeaderUsesConfiguredTimezone(t *testing.T) {
	var sb strings.Builder
	writeReportHeader(
		&sb,
		"daily",
		time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 24, 17, 0, 0, 0, time.UTC),
		"Asia/Jakarta",
	)
	require.Contains(t, sb.String(), "2026-11-24 00:00:00 WIB")
	require.Contains(t, sb.String(), "(Asia/Jakarta)")
	require.NotContains(t, sb.String(), "17:00:00 UTC (Asia/Jakarta)")
}

func TestImmediateReportHeaderUsesIncidentName(t *testing.T) {
	var sb strings.Builder
	writeReportHeader(
		&sb,
		"high_severity",
		time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC),
		"UTC",
	)
	require.Contains(t, sb.String(), "🚨 Incident Report")
	require.NotContains(t, sb.String(), "High Severity")
}

func TestIncidentSectionsDoNotRenderEmptyHeadings(t *testing.T) {
	resolved := []models.Incident{{
		ID: "inc_1", Title: "CPU", Status: "resolved", Severity: "warning",
		StartedAt: time.Now().Add(-time.Minute), ResolvedAt: time.Now(),
	}}
	var sb strings.Builder
	writeIncidentDetails(&sb, nil, resolved, nil, nil)

	require.NotContains(t, sb.String(), "Unresolved")
	require.Contains(t, sb.String(), "Solved")
}

func TestPlainIncidentCardUsesSeverityBadgeWithoutHardcodedRed(t *testing.T) {
	inc := models.Incident{
		ID: "inc_warning", Title: "CPU", Status: "open", Severity: "warning",
		StartedAt: time.Now(),
	}
	var sb strings.Builder
	writeIncidentCardV2(&sb, inc, nil, "", "UTC")

	require.Contains(t, sb.String(), "Severity: 🟡 warning")
	require.NotContains(t, sb.String(), "🔴 Severity:")
}

func TestImmediatePlainReportSkipsPeriodSummaryProviders(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	r, _ := newMockReporter(t, ReporterConfig{
		PrometheusURL: server.URL, AvailabilityMetric: "up",
		InvestigatorURL: server.URL, SummaryTimeout: time.Second,
		ReportTemplate: "v2",
	})
	content, err := r.generateReportContent(
		context.Background(), "high_severity",
		time.Now().Add(-time.Hour), time.Now(), "UTC",
		nil, nil, nil, time.Now(),
	)

	require.NoError(t, err)
	require.Zero(t, atomic.LoadInt32(&calls))
	require.NotContains(t, content, "Availability")
	require.NotContains(t, content, "**Summary**")
}
