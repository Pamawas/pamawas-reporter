package fakes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// FakeDeliveryTransport is a test transport that captures delivery attempts
type FakeDeliveryTransport struct {
	mu          sync.Mutex
	deliveries  []DeliveryRecord
	servers     map[string]*httptest.Server
	errors      map[string]error
	delays      map[string]time.Duration
	statusCodes map[string]int
}

// DeliveryRecord represents a captured delivery attempt
type DeliveryRecord struct {
	Channel        string
	DestinationKey string
	Content        string
	Timestamp      time.Time
	Attempt        int
	Error          error
	StatusCode     int
}

// NewFakeDeliveryTransport creates a new fake delivery transport
func NewFakeDeliveryTransport() *FakeDeliveryTransport {
	return &FakeDeliveryTransport{
		deliveries:  []DeliveryRecord{},
		servers:     make(map[string]*httptest.Server),
		errors:      make(map[string]error),
		delays:      make(map[string]time.Duration),
		statusCodes: make(map[string]int),
	}
}

// StartDiscordServer starts a fake Discord webhook server
func (f *FakeDeliveryTransport) StartDiscordServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		content, ok := payload["content"].(string)
	if !ok {
		http.Error(w, "invalid content field", http.StatusBadRequest)
		return
	}

		f.mu.Lock()
		record := DeliveryRecord{
			Channel:    "discord",
			Content:    content,
			Timestamp:  time.Now(),
			StatusCode: http.StatusNoContent,
		}
		f.deliveries = append(f.deliveries, record)
		f.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	}))
	f.servers["discord"] = server
	return server
}

// StartTelegramServer starts a fake Telegram Bot API server
func (f *FakeDeliveryTransport) StartTelegramServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		text, ok := payload["text"].(string)
	if !ok {
		http.Error(w, "invalid text field", http.StatusBadRequest)
		return
	}

		f.mu.Lock()
		record := DeliveryRecord{
			Channel:    "telegram",
			Content:    text,
			Timestamp:  time.Now(),
			StatusCode: http.StatusOK,
		}
		f.deliveries = append(f.deliveries, record)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":     true,
			"result": map[string]interface{}{},
		}); err != nil {
			http.Error(w, "encoding error", http.StatusInternalServerError)
			return
		}
	}))
	f.servers["telegram"] = server
	return server
}

// StartEmailServer starts a fake SMTP server (for testing, we just capture the email)
func (f *FakeDeliveryTransport) StartEmailServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For email, we'll just capture the request
		f.mu.Lock()
		record := DeliveryRecord{
			Channel:   "email",
			Timestamp: time.Now(),
		}
		f.deliveries = append(f.deliveries, record)
		f.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	f.servers["email"] = server
	return server
}

// SetError sets an error to be returned for a channel
func (f *FakeDeliveryTransport) SetError(channel string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors[channel] = err
}

// SetDelay sets a delay for a channel (simulates slow network)
func (f *FakeDeliveryTransport) SetDelay(channel string, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delays[channel] = delay
}

// SetStatusCode sets a custom status code for a channel
func (f *FakeDeliveryTransport) SetStatusCode(channel string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCodes[channel] = code
}

// GetDeliveries returns all captured deliveries
func (f *FakeDeliveryTransport) GetDeliveries() []DeliveryRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]DeliveryRecord, len(f.deliveries))
	copy(result, f.deliveries)
	return result
}

// GetDeliveriesByChannel returns deliveries for a specific channel
func (f *FakeDeliveryTransport) GetDeliveriesByChannel(channel string) []DeliveryRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []DeliveryRecord
	for _, d := range f.deliveries {
		if d.Channel == channel {
			result = append(result, d)
		}
	}
	return result
}

// Clear clears all captured deliveries
func (f *FakeDeliveryTransport) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = []DeliveryRecord{}
}

// Close shuts down all test servers
func (f *FakeDeliveryTransport) Close() {
	for _, server := range f.servers {
		server.Close()
	}
	f.servers = make(map[string]*httptest.Server)
}

// GetDiscordURL returns the Discord webhook URL
func (f *FakeDeliveryTransport) GetDiscordURL() string {
	if server, ok := f.servers["discord"]; ok {
		return server.URL
	}
	return ""
}

// GetTelegramURL returns the Telegram bot API URL
func (f *FakeDeliveryTransport) GetTelegramURL() string {
	if server, ok := f.servers["telegram"]; ok {
		return server.URL
	}
	return ""
}

// VerifyDelivery verifies that a delivery was made with expected content
func (f *FakeDeliveryTransport) VerifyDelivery(channel, expectedContent string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deliveries {
		if d.Channel == channel && strings.Contains(d.Content, expectedContent) {
			return true
		}
	}
	return false
}

// GetDeliveryCount returns the count of deliveries for a channel
func (f *FakeDeliveryTransport) GetDeliveryCount(channel string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, d := range f.deliveries {
		if d.Channel == channel {
			count++
		}
	}
	return count
}

// FakeEmailClient is a fake email client that captures emails
type FakeEmailClient struct {
	mu     sync.Mutex
	emails []EmailRecord
}

// EmailRecord represents a captured email
type EmailRecord struct {
	From        string
	To          []string
	Subject     string
	Body        string
	HTMLBody    string
	Timestamp   time.Time
	Error       error
}

// NewFakeEmailClient creates a new fake email client
func NewFakeEmailClient() *FakeEmailClient {
	return &FakeEmailClient{
		emails: []EmailRecord{},
	}
}

// DialAndSendWithContext captures the email
func (f *FakeEmailClient) DialAndSendWithContext(ctx context.Context, msg interface{}) error {
	// In tests, we capture what we can from the message
	record := EmailRecord{
		Timestamp: time.Now(),
	}
	// Try to extract fields if msg is a *mail.Msg
	// This is a simplified capture
	f.mu.Lock()
	f.emails = append(f.emails, record)
	f.mu.Unlock()
	return nil
}

// GetEmails returns all captured emails
func (f *FakeEmailClient) GetEmails() []EmailRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]EmailRecord, len(f.emails))
	copy(result, f.emails)
	return result
}

// Clear clears all captured emails
func (f *FakeEmailClient) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails = []EmailRecord{}
}

// GetEmailCount returns the count of captured emails
func (f *FakeEmailClient) GetEmailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.emails)
}