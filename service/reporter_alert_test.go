package service

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Pamawas/pamawas-reporter/models"
)

// newAlertTestReporter wires a reporter against a fake Discord webhook so the
// delivery path is exercised end to end without real credentials.
func newAlertTestReporter(t *testing.T, threshold string) (*Reporter, sqlmock.Sqlmock, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Logf("failed to drain body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	r, mock := newMockReporter(t, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		HighSeverityThreshold: threshold,
	})
	return r, mock, &calls
}

func TestSendAlertSkipsBelowThreshold(t *testing.T) {
	r, mock, calls := newAlertTestReporter(t, "high")

	mock.ExpectQuery("SELECT title, severity, environment, started_at FROM incidents").
		WithArgs("inc_warn").
		WillReturnRows(sqlmock.NewRows([]string{"title", "severity", "environment", "started_at"}).
			AddRow("Warning alert", "warning", "production", time.Now().Add(-time.Hour)))

	resp, err := r.SendAlert(context.Background(), models.AlertPayload{IncidentID: "inc_warn"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "severity_below_threshold" {
		t.Fatalf("expected below-threshold skip, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("delivery attempted %d times for below-threshold severity", got)
	}
}

func TestSendAlertOneShotClaim(t *testing.T) {
	r, mock, calls := newAlertTestReporter(t, "high")

	incidentRow := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"title", "severity", "environment", "started_at"}).
			AddRow("Critical alert", "critical", "production", time.Now().Add(-time.Hour))
	}

	mock.ExpectQuery("SELECT title, severity, environment, started_at FROM incidents").
		WithArgs("inc_crit").WillReturnRows(incidentRow())
	// Claim succeeds the first time.
	mock.ExpectQuery("UPDATE incidents").
		WithArgs("inc_crit").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("inc_crit"))

	resp, err := r.SendAlert(context.Background(), models.AlertPayload{IncidentID: "inc_crit"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Sent {
		t.Fatalf("expected alert sent, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected exactly 1 delivery, got %d", got)
	}

	// A second forward (Grafana re-fire) finds the claim already taken.
	mock.ExpectQuery("SELECT title, severity, environment, started_at FROM incidents").
		WithArgs("inc_crit").WillReturnRows(incidentRow())
	mock.ExpectQuery("UPDATE incidents").
		WithArgs("inc_crit").
		WillReturnError(sql.ErrNoRows)

	resp, err = r.SendAlert(context.Background(), models.AlertPayload{IncidentID: "inc_crit"})
	if err != nil {
		t.Fatalf("unexpected error on re-fire: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "already_notified" {
		t.Fatalf("expected already_notified skip, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("re-fire produced a duplicate delivery (calls=%d)", got)
	}
}

func TestSendResolveAlertGatesOnSameThreshold(t *testing.T) {
	r, mock, calls := newAlertTestReporter(t, "high")

	mock.ExpectQuery("SELECT title, severity, environment, resolved_at, started_at FROM incidents").
		WithArgs("inc_warn").
		WillReturnRows(sqlmock.NewRows([]string{"title", "severity", "environment", "resolved_at", "started_at"}).
			AddRow("Warning alert", "warning", "production", time.Now(), time.Now().Add(-time.Hour)))

	resp, err := r.SendResolveAlert(context.Background(), models.AlertPayload{IncidentID: "inc_warn"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "severity_not_high_critical" {
		t.Fatalf("expected resolve skip on low severity, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("resolve delivery attempted %d times for below-threshold severity", got)
	}
}
