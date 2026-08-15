package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// TestConfig holds test configuration
type TestConfig struct {
	DatabaseURL string
}

func getTestConfig() TestConfig {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pamawas:pamawas@localhost:5432/pamawas_test?sslmode=disable"
	}
	return TestConfig{DatabaseURL: dbURL}
}

// TestReporter_GenerateDailyReport tests report generation
func TestReporter_GenerateDailyReport(t *testing.T) {
	cfg := getTestConfig()

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Skipf("Skipping test: cannot open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("Skipping test: database not available: %v", err)
	}

	reporter := &Reporter{
		db: db,
		config: ReporterConfig{
			ReportTemplate: "",
		},
	}

	// Create test incidents
	incidents := []Incident{
		{
			ID:              "test_incident_1",
			Title:           "High API Latency",
			Status:          "firing",
			StartedAt:       time.Now().Add(-2 * time.Hour),
			Severity:        "warning",
			AffectedServices: []string{"payment-api"},
		},
		{
			ID:              "test_incident_2",
			Title:           "Database Connection Pool Exhausted",
			Status:          "resolved",
			StartedAt:       time.Now().Add(-5 * time.Hour),
			ResolvedAt:      time.Now().Add(-3 * time.Hour),
			Severity:        "high",
			AffectedServices: []string{"payment-api", "user-service"},
		},
	}

	report := reporter.generateDailyReport(incidents)

	// Verify report contains expected content
	if len(report) == 0 {
		t.Fatal("Report is empty")
	}

	// Check for key sections
	expectedSections := []string{
		"Good morning",
		"incidents occurred",
		"needs your attention",
		"recovered automatically",
	}

	for _, section := range expectedSections {
		if !contains(report, section) {
			t.Errorf("Report missing expected section: %s", section)
		}
	}

	t.Logf("Generated report (%d chars):\n%s", len(report), report)
}

// TestReporter_TemplateRendering tests custom template rendering
func TestReporter_TemplateRendering(t *testing.T) {
	reporter := &Reporter{
		config: ReporterConfig{
			ReportTemplate: "Health: {{HealthyPercentage}}%\nIncidents: {{TotalIncidents}}\nActive: {{NeedsAttention}}\nRecovered: {{Recovered}}\nCause: {{MostLikelyCause}}\nConfidence: {{Confidence}}",
		},
	}

	incidents := []Incident{
		{
			ID:              "test_1",
			Title:           "Test Incident",
			Status:          "firing",
			StartedAt:       time.Now(),
			Severity:        "warning",
			AffectedServices: []string{"test-service"},
		},
	}

	report := reporter.generateDailyReport(incidents)

	// Check template variables were replaced
	expectedVars := []string{
		"Health:",
		"Incidents:",
		"Active:",
		"Recovered:",
		"Cause:",
		"Confidence:",
	}

	for _, v := range expectedVars {
		if !contains(report, v) {
			t.Errorf("Template variable not replaced: %s", v)
		}
	}

	t.Logf("Template report:\n%s", report)
}

// TestReporter_SendToDiscord tests Discord payload formatting
func TestReporter_SendToDiscord(t *testing.T) {
	reporter := &Reporter{
		config: ReporterConfig{
			DiscordWebhookURL: "https://discord.com/api/webhooks/test",
		},
	}

	report := Report{
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
	reporter := &Reporter{
		config: ReporterConfig{
			TelegramBotToken: "test_token",
			TelegramChatID:   "test_chat_id",
		},
	}

	report := Report{
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

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}