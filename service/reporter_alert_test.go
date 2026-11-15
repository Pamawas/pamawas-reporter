package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	r, _, calls := newAlertTestReporter(t, "high")

	resp, err := r.SendAlert(context.Background(), models.AlertPayload{
		Title:       "Warning alert",
		Severity:    "warning",
		Environment: "production",
		DedupKey:    "grafana:warn1",
	})
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

	// Alert details come from the event, so no incident lookup is needed.
	payload := models.AlertPayload{
		Title:       "Critical alert",
		Severity:    "critical",
		Environment: "production",
		StartedAt:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		DedupKey:    "grafana:abc123",
	}

	// First fire claims the condition.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))

	resp, err := r.SendAlert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Sent {
		t.Fatalf("expected alert sent, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected exactly 1 delivery, got %d", got)
	}

	// A Grafana re-fire finds the claim already taken.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnError(sql.ErrNoRows)

	resp, err = r.SendAlert(context.Background(), payload)
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
	r, _, calls := newAlertTestReporter(t, "high")

	resp, err := r.SendResolveAlert(context.Background(), models.AlertPayload{
		Title:       "Warning alert",
		Severity:    "warning",
		Environment: "production",
		DedupKey:    "grafana:warn1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "severity_below_threshold" {
		t.Fatalf("expected resolve skip on low severity, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("resolve delivery attempted %d times for below-threshold severity", got)
	}
}

// TestAlertPathsUseEmbeds verifies both the firing alert and the resolved
// alert render as bordered embeds, not plain text, and carry the right bar
// color (severity-colored when firing, green once resolved).
func TestAlertPathsUseEmbeds(t *testing.T) {
	var payloads []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		payloads = append(payloads, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r, mock := newMockReporter(t, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		HighSeverityThreshold: "high",
	})

	payload := models.AlertPayload{
		Title:       "Monitoring Target Down",
		Severity:    "critical",
		Environment: "production",
		StartedAt:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		DedupKey:    "grafana:down1",
	}

	// Firing alert: claim succeeds, then delivers.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))
	resp, err := r.SendAlert(context.Background(), payload)
	if err != nil || !resp.Sent {
		t.Fatalf("alert failed: resp=%+v err=%v", resp, err)
	}

	// Resolved alert: releases the claim, then delivers.
	mock.ExpectExec("DELETE FROM idempotency_records").
		WillReturnResult(sqlmock.NewResult(0, 1))
	resp, err = r.SendResolveAlert(context.Background(), payload)
	if err != nil || !resp.Sent {
		t.Fatalf("resolved alert failed: resp=%+v err=%v", resp, err)
	}

	if len(payloads) != 2 {
		t.Fatalf("expected 2 webhook calls, got %d", len(payloads))
	}
	for i, p := range payloads {
		if _, hasContent := p["content"]; hasContent {
			t.Errorf("payload %d used plain content instead of embeds", i)
		}
		embeds, ok := p["embeds"].([]interface{})
		if !ok || len(embeds) != 1 {
			t.Fatalf("payload %d: expected exactly 1 embed, got %#v", i, p["embeds"])
		}
		embed := embeds[0].(map[string]interface{})
		if i == 0 {
			if embed["color"] != float64(0xE74C3C) {
				t.Errorf("firing alert bar should be critical red, got %v", embed["color"])
			}
			if !strings.Contains(embed["title"].(string), "🚨") {
				t.Errorf("firing alert title should carry the alarm marker: %v", embed["title"])
			}
		} else {
			if embed["color"] != float64(0x2ECC71) {
				t.Errorf("resolved alert bar should be green, got %v", embed["color"])
			}
			if !strings.Contains(embed["title"].(string), "✅") {
				t.Errorf("resolved alert title should carry the check marker: %v", embed["title"])
			}
		}
	}
}
