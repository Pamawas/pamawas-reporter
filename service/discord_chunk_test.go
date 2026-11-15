package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
func TestDailyReportSendsDigestAndThreadedDetail(t *testing.T) {
	var payloads []map[string]interface{}
	var paths []string
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		payloads = append(payloads, body)
		paths = append(paths, r.URL.String())
		// wait=true (used for the channel message) gets the created message back.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_123"}`))
	}))
	defer capture.Close()

	incidentID := "inc_abc123"
	report := models.Report{
		ID:              "test-id",
		Content:         "plain text content that must NOT be sent as content",
		ReportType:      "daily",
		Timezone:        "UTC",
		TemplateVersion: "pamawas-report-v2",
		GeneratedAt:     time.Now(),
		PeriodEnd:       time.Now(),
	}

	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: capture.URL})
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("test-id").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}).
			AddRow(incidentID, models.InclusionReasonNewlyStarted))
	// getIncidentsByIDs selects exactly these 10 columns.
	mock.ExpectQuery("SELECT id, title, status, started_at, resolved_at, severity, environment").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "status", "started_at", "resolved_at",
			"severity", "environment", "affected_services",
			"correlation_policy", "correlation_version",
		}).AddRow(incidentID, "Monitoring Target Down", "open", time.Now(),
			nil, "critical", "production", "{last-labs}",
			"service_time_window", 1))
	mock.ExpectQuery("FROM evidence e").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "incident_id", "type", "content", "source", "source_detail",
			"confidence", "supports_evidence", "contradicts_evidence", "ordinal",
		}).AddRow("evd_1", incidentID, "fact", "CPU load spiked to 17",
			"prometheus", "node_load1", 0.95, "{}", "{}", 0))

	if err := r.sendToDiscord(context.Background(), report); err != nil {
		t.Fatalf("sendToDiscord failed: %v", err)
	}

	require.Len(t, payloads, 2, "expected a channel message and one thread post")

	// The channel message is the digest: one card a reader can scan.
	channel, ok := payloads[0]["embeds"].([]interface{})
	require.True(t, ok, "channel payload must contain embeds")
	require.Len(t, channel, 1, "the channel gets exactly one digest card")
	digest, _ := channel[0].(map[string]interface{})
	require.Contains(t, digest["title"], "Daily Report")
	desc, _ := digest["description"].(string)
	require.Contains(t, desc, "Needs attention")
	require.Contains(t, desc, "Monitoring Target Down")
	// A digest states counts, not per-incident transcripts.
	require.NotContains(t, desc, "CPU load spiked to 17")
	require.NotEmpty(t, payloads[0]["thread_name"], "the digest creates a thread for detail")
	_, hasContent := payloads[0]["content"]
	require.False(t, hasContent, "reports must not be sent as plain content")

	// The detail goes into that thread, so the channel stays scannable.
	require.Contains(t, paths[1], "thread_id=msg_123")
	thread, ok := payloads[1]["embeds"].([]interface{})
	require.True(t, ok, "thread payload must contain embeds")
	require.Len(t, thread, 1)
	detail, _ := thread[0].(map[string]interface{})
	require.Contains(t, detail["title"], "Monitoring Target Down")
	detailDesc, _ := detail["description"].(string)
	require.Contains(t, detailDesc, "CPU load spiked to 17")
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
