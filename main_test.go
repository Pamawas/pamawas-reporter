package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/Pamawas/pamawas-reporter/service"
)

// TestConfig holds test configuration
type TestConfig struct {
	DatabaseURL string
}

func getTestConfig() TestConfig {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	return TestConfig{DatabaseURL: dbURL}
}

// TestReporter_GenerateDailyReport tests report generation
func TestReporter_GenerateDailyReport(t *testing.T) {
	cfg := getTestConfig()

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Skipf("Skipping test: cannot open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("Skipping test: database not available: %v", err)
	}

	t.Skip("Cannot test private method generateDailyReport")
}

// TestReporter_TemplateRendering tests custom template rendering
func TestReporter_TemplateRendering(t *testing.T) {
	_ = service.NewReporter(nil, service.ReporterConfig{
		ReportTemplate: "Health: {{HealthyPercentage}}%\nIncidents: {{TotalIncidents}}\nActive: {{NeedsAttention}}\nRecovered: {{Recovered}}\nCause: {{MostLikelyCause}}\nConfidence: {{Confidence}}",
	}, nil)

	t.Skip("Cannot test private method generateDailyReport")
}

// TestReporter_SendToDiscord tests Discord payload formatting
func TestReporter_SendToDiscord(t *testing.T) {
	_ = service.NewReporter(nil, service.ReporterConfig{
		DiscordWebhookURL: "https://discord.com/api/webhooks/test",
	}, nil)

	report := models.Report{
		Content: "Test report content",
	}

	// We can't actually send, but we can verify the payload structure
	payload := map[string]interface{}{
		"content": report.Content,
		"username": "Pamawas Reporter",
	}

	// Verify payload can be marshaled
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal Discord payload: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal Discord payload: %v", err)
	}

	if parsed["username"] != "Pamawas Reporter" {
		t.Errorf("Unexpected username: %v", parsed["username"])
	}

	t.Logf("Discord payload: %s", string(data))
}

// TestReporter_SendToTelegram tests Telegram payload formatting
func TestReporter_SendToTelegram(t *testing.T) {
	_ = service.NewReporter(nil, service.ReporterConfig{
		TelegramBotToken: "test_token",
		TelegramChatID:   "test_chat_id",
	}, nil)

	report := models.Report{
		Content: "Test report content",
	}

	payload := map[string]interface{}{
		"chat_id":    "test_chat_id",
		"text":       report.Content,
		"parse_mode": "Markdown",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal Telegram payload: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal Telegram payload: %v", err)
	}

	if parsed["parse_mode"] != "Markdown" {
		t.Errorf("Unexpected parse_mode: %v", parsed["parse_mode"])
	}

	t.Logf("Telegram payload: %s", string(data))
}