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
func TestSendToDiscordUsesEmbeds(t *testing.T) {
	var raw map[string]interface{}
	capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusNoContent)
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
	}

	// The reporter loads the report's incidents and evidence from the database.
	r, mock := newMockReporter(t, ReporterConfig{DiscordWebhookURL: capture.URL})
	mock.ExpectQuery("SELECT ri.incident_id, ri.inclusion_reason").
		WithArgs("test-id").
		WillReturnRows(sqlmock.NewRows([]string{"incident_id", "inclusion_reason"}).
			AddRow(incidentID, models.InclusionReasonHighSeverityImmediate))
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

	embeds, ok := raw["embeds"].([]interface{})
	require.True(t, ok, "payload must contain embeds")
	// One incident means one card: the report header and the incident belong
	// together, so splitting them into two cards was redundant.
	require.Len(t, embeds, 1, "a single-incident report is one card")
	_, hasContent := raw["content"]
	require.False(t, hasContent, "reports must not be sent as plain content")

	incidentEmbed, _ := embeds[0].(map[string]interface{})
	require.Equal(t, float64(0xE74C3C), incidentEmbed["color"], "critical incident card must be red")
	require.Contains(t, incidentEmbed["title"], "Monitoring Target Down")
	require.Contains(t, incidentEmbed["description"], "CPU load spiked to 17")
	// The card is built for triage: evidence, not report-generator metadata.
	// "Availability: 100.00%" during a critical incident misleads, and the
	// incident count says nothing about this incident.
	desc, _ := incidentEmbed["description"].(string)
	require.Contains(t, desc, "Supporting evidence")
	require.NotContains(t, desc, "Availability:")
	require.NotContains(t, desc, "Incidents:")
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
