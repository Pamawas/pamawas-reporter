package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Pamawas/pamawas-reporter/fakes"
	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/stretchr/testify/require"
)

// TestSendToDiscordChunks verifies report content exceeding Discord's 2000-char limit is split into multiple webhook calls.
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
		ID:         "test-id",
		Content:    sb.String(),
		ReportType: "daily",
	}

	r := NewReporter(nil, ReporterConfig{
		DiscordWebhookURL: server.URL,
		// Small delay for testing to keep tests fast; default is 1s in prod
		DiscordRateLimitDelay: 10 * time.Millisecond,
	}, testMetrics())

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

// TestSendToDiscordRateLimitRetry verifies that a Discord HTTP 429 with a Retry-After header is retried after the delay and eventually succeeds.
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

	report := models.Report{ID: "rl-test", Content: "test content", ReportType: "daily"}
	err := r.sendToDiscord(context.Background(), report)
	require.Nil(t, err, "sendToDiscord should succeed after rate-limit retry")
	require.GreaterOrEqual(t, attempts, 2, "expected at least 2 attempts (initial + retry)")
}

// TestSendToDiscordRateLimitExhausted verifies that persistent Discord rate-limiting fails the chunk after max retries.
func TestSendToDiscordRateLimitExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	r := NewReporter(nil, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		DiscordRateLimitDelay: 10 * time.Millisecond,
	}, testMetrics())

	report := models.Report{ID: "rl-exhaust", Content: "test content", ReportType: "daily"}
	err := r.sendToDiscord(context.Background(), report)
	require.Error(t, err, "sendToDiscord should fail when rate limit persists")
	require.Contains(t, err.Error(), "rate limited")
}

// TestSendToDiscordPacing verifies that the configurable pacing delay is
// honored between chunk sends. When DiscordRateLimitDelay is 0, the default
// 1s delay is used; when set, it overrides the default.
func TestSendToDiscordPacing(t *testing.T) {
	var times []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		times = append(times, time.Now())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r := NewReporter(nil, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		DiscordRateLimitDelay: 20 * time.Millisecond,
	}, testMetrics())

	// Build content that produces 3 chunks
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		sb.WriteString("x")
	}
	report := models.Report{ID: "pace-test", Content: sb.String(), ReportType: "daily"}
	err := r.sendToDiscord(context.Background(), report)
	require.Nil(t, err)
	require.GreaterOrEqual(t, len(times), 3, "expected at least 3 chunk sends")

	// Verify pacing: each gap between sends should be >= the configured delay
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		require.GreaterOrEqual(t, gap, 15*time.Millisecond,
			"gap between chunk %d and %d should be >= pacing delay", i-1, i)
	}
}

// TestParseRetryAfter verifies retry_after parsing from both header (seconds)
// and JSON body (milliseconds).
func TestParseRetryAfter(t *testing.T) {
	r := NewReporter(nil, ReporterConfig{}, testMetrics())

	// Header value in seconds
	got := r.parseRetryAfter("3", nil)
	require.Equal(t, 3*time.Second, got)

	// JSON body with milliseconds
	body := []byte(`{"retry_after": 1500.5, "global": true}`)
	got = r.parseRetryAfter("", body)
	require.Equal(t, 1500*time.Millisecond, got)

	// Empty header and body -> default fallback
	got = r.parseRetryAfter("", nil)
	require.Equal(t, 2*time.Second, got)
}
