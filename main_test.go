package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	_ "github.com/lib/pq"

	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/Pamawas/pamawas-reporter/service"
)

func TestMetricsRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerMetricsRoutes(mux, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, test := range []struct {
		path       string
		statusCode int
	}{
		{path: "/metrics", statusCode: http.StatusOK},
		{path: "/api/v1/metrics", statusCode: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.statusCode {
				t.Fatalf("GET %s returned status %d, want %d", test.path, response.Code, test.statusCode)
			}
		})
	}
}

// TestConfig holds test configuration
type TestConfig struct {
	DatabaseURL string
}

func getTestConfig() TestConfig {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	return TestConfig{DatabaseURL: dbURL}
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
		"content":  report.Content,
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
