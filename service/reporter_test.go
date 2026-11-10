package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

func testMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		ReportsGeneratedTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_reports_total"}),
		DeliveryTotal:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_delivery_total"}, []string{"channel", "status"}),
		DBConnectionErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "test_db_errors_total"}),
	}
}

func newMockReporter(t *testing.T, cfg ReporterConfig) (*Reporter, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("Failed to close database: %v", closeErr)
		}
	})
	return NewReporter(db, cfg, testMetrics()), mock
}

func TestNewReporterAndStop(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{})
	if r.httpClient.Timeout != 10*time.Second || r.StartTime().IsZero() {
		t.Fatalf("unexpected reporter state: %+v", r)
	}
	if err := r.ctx.Err(); err != nil {
		t.Fatalf("reporter context is canceled before Stop: %v", err)
	}
	r.Stop()
	if err := r.ctx.Err(); err != context.Canceled {
		t.Fatalf("reporter context error = %v, want context.Canceled", err)
	}
}

func TestSendToDiscord(t *testing.T) {
	var payload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", req.Method, req.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	r, _ := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	if err := r.sendToDiscord(context.Background(), models.Report{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if payload["content"] != "hello" || payload["username"] != "Pamawas Reporter" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSendToDiscordReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	r, _ := newMockReporter(t, ReporterConfig{DiscordWebhookURL: server.URL})
	if err := r.sendToDiscord(context.Background(), models.Report{Content: "non-empty content"}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v", err)
	}
}

func TestSendToTelegram(t *testing.T) {
	var req *http.Request
	var payload map[string]interface{}
	r, _ := newMockReporter(t, ReporterConfig{TelegramBotToken: "token", TelegramChatID: "chat"})
	r.httpClient.Transport = roundTripperFunc(func(rq *http.Request) (*http.Response, error) {
		req = rq
		if err := json.NewDecoder(rq.Body).Decode(&payload); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	if err := r.sendToTelegram(context.Background(), models.Report{Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://api.telegram.org/bottoken/sendMessage" || payload["chat_id"] != "chat" || payload["parse_mode"] != "Markdown" {
		t.Fatalf("request=%v payload=%#v", req.URL, payload)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
