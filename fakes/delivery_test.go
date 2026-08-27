package fakes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestFakeDeliveryTransport_Discord tests the fake Discord transport
func TestFakeDeliveryTransport_Discord(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	discordServer := transport.StartDiscordServer()
	defer discordServer.Close()

	// Create a test HTTP request to the fake Discord server
	payload := map[string]interface{}{
		"content":  "Test report content",
		"username": "Pamawas Reporter",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", discordServer.URL, strings.NewReader(string(payloadBytes)))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Expected status 204, got %d", resp.StatusCode)
	}

	// Verify delivery was captured
	if !transport.VerifyDelivery("discord", "Test report content") {
		t.Fatalf("Discord delivery not captured. Deliveries: %v", transport.GetDeliveries())
	}

	if count := transport.GetDeliveryCount("discord"); count != 1 {
		t.Fatalf("Expected 1 Discord delivery, got %d", count)
	}
}

// TestFakeDeliveryTransport_Telegram tests the fake Telegram transport
func TestFakeDeliveryTransport_Telegram(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	telegramServer := transport.StartTelegramServer()
	defer telegramServer.Close()

	payload := map[string]interface{}{
		"chat_id":    "fake-chat",
		"text":       "Test report content",
		"parse_mode": "Markdown",
	}
	payloadBytes, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", telegramServer.URL, strings.NewReader(string(payloadBytes)))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	// Verify delivery was captured
	if !transport.VerifyDelivery("telegram", "Test report content") {
		t.Fatalf("Telegram delivery not captured. Deliveries: %v", transport.GetDeliveries())
	}

	if count := transport.GetDeliveryCount("telegram"); count != 1 {
		t.Fatalf("Expected 1 Telegram delivery, got %d", count)
	}
}

// TestFakeDeliveryTransport_MultipleDeliveries tests multiple deliveries
func TestFakeDeliveryTransport_MultipleDeliveries(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	transport.StartDiscordServer()

	// Capture multiple deliveries
	deliveries := []string{"report 1", "report 2", "report 3"}
	for i, content := range deliveries {
		transport.mu.Lock()
		transport.deliveries = append(transport.deliveries, DeliveryRecord{
			Channel:   "discord",
			Content:   content,
			Timestamp: time.Now(),
			Attempt:   i + 1,
		})
		transport.mu.Unlock()
	}

	// Verify all captured
	if count := transport.GetDeliveryCount("discord"); count != 3 {
		t.Fatalf("Expected 3 Discord deliveries, got %d", count)
	}

	// Verify content
	discordDeliveries := transport.GetDeliveriesByChannel("discord")
	if len(discordDeliveries) != 3 {
		t.Fatalf("Expected 3 deliveries, got %d", len(discordDeliveries))
	}
}

// TestFakeDeliveryTransport_Clear tests clearing deliveries
func TestFakeDeliveryTransport_Clear(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	transport.StartDiscordServer()

	// Add a delivery
	transport.mu.Lock()
	transport.deliveries = append(transport.deliveries, DeliveryRecord{
		Channel:   "discord",
		Content:   "test",
		Timestamp: time.Now(),
	})
	transport.mu.Unlock()

	// Clear
	transport.Clear()

	// Verify empty
	if count := transport.GetDeliveryCount("discord"); count != 0 {
		t.Fatalf("Expected 0 deliveries after clear, got %d", count)
	}
}

// TestFakeEmailClient tests the fake email client
func TestFakeEmailClient(t *testing.T) {
	client := NewFakeEmailClient()
	client.Clear()

	if count := client.GetEmailCount(); count != 0 {
		t.Fatalf("Expected 0 emails, got %d", count)
	}
}

// TestFakeDeliveryTransport_VerifyDelivery tests delivery verification
func TestFakeDeliveryTransport_VerifyDelivery(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	transport.StartDiscordServer()

	// Add a delivery with specific content
	transport.mu.Lock()
	transport.deliveries = append(transport.deliveries, DeliveryRecord{
		Channel:   "discord",
		Content:   "Test report with specific incident IN123",
		Timestamp: time.Now(),
	})
	transport.mu.Unlock()

	// Verify delivery with partial content match
	if !transport.VerifyDelivery("discord", "IN123") {
		t.Fatalf("Expected to find delivery with IN123")
	}

	if transport.VerifyDelivery("discord", "NOT_IN_CONTENT") {
		t.Fatalf("Should not find delivery with NOT_IN_CONTENT")
	}
}

// TestFakeDeliveryTransport_SimulateErrors tests error simulation
func TestFakeDeliveryTransport_SimulateErrors(t *testing.T) {
	transport := NewFakeDeliveryTransport()
	defer transport.Close()

	// Test that we can set errors for channels
	transport.SetError("discord", &testError{msg: "discord error"})
	transport.SetError("telegram", &testError{msg: "telegram error"})

	transport.mu.Lock()
	if transport.errors["discord"] == nil {
		t.Fatalf("Expected discord error to be set")
	}
	if transport.errors["telegram"] == nil {
		t.Fatalf("Expected telegram error to be set")
	}
	transport.mu.Unlock()
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}