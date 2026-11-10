package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Pamawas/pamawas-reporter/fakes"
	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/stretchr/testify/require"
)

// TestSendToDiscordChunks verifies that report content exceeding Discord's
// 2000-character limit is split into multiple webhook calls, each within
// the limit, so the full report is delivered instead of being rejected with
// HTTP 400.
func TestSendToDiscordChunks(t *testing.T) {
	fake := fakes.NewFakeDeliveryTransport()
	server := fake.StartDiscordServer()
	defer server.Close()

	// Build a report body that exceeds Discord's 2000-char content limit
	var sb strings.Builder
	for i := 0; i < 2100; i++ {
		sb.WriteString("This is a test message for Discord chunking. ")
	}
	report := models.Report{
		ID:        "test-id",
		Content:   sb.String(),
		ReportType: "daily",
	}

	r := NewReporter(nil, ReporterConfig{DiscordWebhookURL: server.URL}, metrics.NewMetrics())

	// This should succeed without error
	err := r.sendToDiscord(context.Background(), report)
	require.Nil(t, err, "sendToDiscord should not return error for chunked content")

	// Verify Discord was called with multiple chunks (at least 2)
	deliveries := fake.GetDeliveries()
	require.GreaterOrEqual(t, len(deliveries), 2, "should have sent multiple chunks")
	for _, d := range deliveries {
		require.LessOrEqual(t, len(d.Content), 2000, "chunk must not exceed Discord's 2000-char limit")
	}
}