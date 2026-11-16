package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/stretchr/testify/require"
)

// TestSendToDiscordUsesEmbeds verifies reports are delivered as embeds: one
// summary card plus one bordered card per incident. Discord's proportional
// font makes ASCII text boxes impossible to align, so the box comes from an
// embed (colored left bar) instead of the text template.
//
// A daily report is ONE message. Webhooks cannot create threads, so anything
// split off as "detail" would land in the channel and recreate the wall of
// text the digest exists to avoid.
func TestDailyReportIsOneDigestMessage(t *testing.T) {
	var payloads []map[string]interface{}
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		payloads = append(payloads, body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_123"}`))
	}))
	defer capture.Close()

	incidentID := "inc_abc123"
	report := models.Report{
		ID: "test-id", ReportType: "daily", Timezone: "UTC",
		TemplateVersion: "pamawas-report-v2", GeneratedAt: time.Now(), PeriodEnd: time.Now(),
	}

	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: capture.URL})
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("test-id").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}).
			AddRow(incidentID, models.InclusionReasonNewlyStarted))
	mock.ExpectQuery("SELECT id, title, status, started_at, resolved_at, severity, environment").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "resolved_at",
			"severity", "environment", "affected_services",
			"correlation_policy", "correlation_version",
		}).AddRow(incidentID, "Monitoring Target Down", "open", time.Now(),
			nil, "critical", "production", "{last-labs}", "service_time_window", 1))

	if err := r.sendToDiscord(context.Background(), report); err != nil {
		t.Fatalf("sendToDiscord failed: %v", err)
	}

	require.Len(t, payloads, 1, "a daily report is exactly one message")
	// thread_name is rejected with 400 by non-forum channels; it must never
	// be sent.
	require.NotContains(t, payloads[0], "thread_name")

	embeds, ok := payloads[0]["embeds"].([]interface{})
	require.True(t, ok, "payload must contain embeds")
	require.Len(t, embeds, 1, "the channel gets exactly one digest card")
	digest, _ := embeds[0].(map[string]interface{})
	require.Contains(t, digest["title"], "Daily Report")
	desc, _ := digest["description"].(string)
	require.Contains(t, desc, "Needs attention")
	require.Contains(t, desc, "Monitoring Target Down")
}

// TestDailyDigestGroupsRecoveredConditions verifies a busy night stays
// scannable: repeated occurrences of one condition collapse to a single line
// with a count, rather than one line each.
func TestDailyDigestGroupsRecoveredConditions(t *testing.T) {
	base := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	var recovered []models.Incident
	for i := 0; i < 12; i++ {
		started := base.Add(time.Duration(i) * 10 * time.Minute)
		recovered = append(recovered, models.Incident{
			ID: fmt.Sprintf("inc_%d", i), Title: "Monitoring Target Down", Status: "resolved",
			Severity: "critical", AffectedServices: []string{"last-labs"},
			StartedAt: started, ResolvedAt: started.Add(5 * time.Minute),
		})
	}

	var sb strings.Builder
	digestRecoveredSection(&sb, recovered)
	out := sb.String()

	require.Contains(t, out, "×12", "repeated occurrences must collapse to a count")
	require.Contains(t, out, "~5m0s each")
	require.Equal(t, 1, strings.Count(out, "Monitoring Target Down"),
		"the condition must appear once, not once per occurrence")
}

// TestHighSeverityReportIsOneTriageCard verifies the immediate-report path is
// untouched by the daily digest: a single incident still renders as ONE card in
// triage order (cause, actions, evidence).
func TestHighSeverityReportIsOneTriageCard(t *testing.T) {
	var payloads []map[string]interface{}
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		payloads = append(payloads, body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	defer capture.Close()

	incidentID := "inc_abc123"
	report := models.Report{
		ID: "test-id", ReportType: "high_severity", Timezone: "UTC",
		TemplateVersion: "pamawas-report-v2", GeneratedAt: time.Now(),
	}

	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: capture.URL})
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("test-id").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}).
			AddRow(incidentID, models.InclusionReasonHighSeverityImmediate))
	mock.ExpectQuery("SELECT id, title, status, started_at, resolved_at, severity, environment").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "resolved_at",
			"severity", "environment", "affected_services",
			"correlation_policy", "correlation_version",
		}).AddRow(incidentID, "Monitoring Target Down", "open", time.Now(),
			nil, "critical", "production", "{last-labs}", "service_time_window", 1))
	mock.ExpectQuery("FROM evidence e").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "incident_id", "type", "content", "source", "source_detail",
			"confidence", "supports_evidence", "contradicts_evidence", "ordinal",
		}).AddRow("evd_1", incidentID, "fact", "CPU load spiked to 17",
			"prometheus", "node_load1", 0.95, "{}", "{}", 0))

	if err := r.sendToDiscord(context.Background(), report); err != nil {
		t.Fatalf("sendToDiscord failed: %v", err)
	}

	require.Len(t, payloads, 1, "a single-incident report is one message, no thread")
	embeds, ok := payloads[0]["embeds"].([]interface{})
	require.True(t, ok)
	require.Len(t, embeds, 1, "one incident means one card")

	card, _ := embeds[0].(map[string]interface{})
	require.Equal(t, float64(0xE74C3C), card["color"], "critical incident card must be red")
	desc, _ := card["description"].(string)
	require.Contains(t, desc, "Supporting evidence")
	require.Contains(t, desc, "CPU load spiked to 17")
	require.NotContains(t, desc, "Availability:")
}

// TestSendToDiscordRateLimitRetry verifies a Discord 429 with Retry-After is
// retried after the delay and eventually succeeds. Exercised at the transport
// level, which is where rate limiting lives, so the test does not need a
// database to render embeds first.
func TestSendToDiscordRateLimitRetry(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			// First attempt: rate limited, with retry_after in header (seconds)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second attempt: success
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r := NewReporter(nil, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		DiscordRateLimitDelay: 10 * time.Millisecond,
	}, testMetrics())

	err := r.sendDiscordChunkWithRetry(context.Background(), 0, []byte(`{"embeds":[]}`))
	require.Nil(t, err, "sendDiscordChunkWithRetry should succeed after rate-limit retry")
	require.GreaterOrEqual(t, attempts, 2, "expected at least 2 attempts (initial + retry)")
}

// TestDailyDigestGroupsOpenConditions verifies the needs-attention list stays
// readable when correlation opens several incidents for one flapping condition.
// A real report had ten identical "Monitoring Target Down · last-labs" lines.
func TestDailyDigestGroupsOpenConditions(t *testing.T) {
	base := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	var open []models.Incident
	for i := 0; i < 10; i++ {
		open = append(open, models.Incident{
			ID: fmt.Sprintf("inc_%d", i), Title: "Monitoring Target Down", Status: "open",
			Severity: "critical", AffectedServices: []string{"last-labs"},
			StartedAt: base.Add(-time.Hour),
		})
	}

	var sb strings.Builder
	digestOpenSection(&sb, open)
	out := sb.String()

	require.Contains(t, out, "(×10, reopened)")
	require.Equal(t, 1, strings.Count(out, "Monitoring Target Down"),
		"the condition must appear once, not once per incident")
	require.Contains(t, out, "open since")
}
