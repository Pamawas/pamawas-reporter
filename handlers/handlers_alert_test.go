package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Pamawas/pamawas-reporter/config"
	"github.com/Pamawas/pamawas-reporter/models"
)

// newAlertHandler wires a Handler whose reporter posts to a fake Discord
// webhook, so the full HTTP path (decode -> validate -> deliver) is exercised.
func newAlertHandler(t *testing.T) (*Handler, sqlmock.Sqlmock, *[]map[string]interface{}) {
	t.Helper()

	var payloads []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode discord payload: %v", err)
		}
		payloads = append(payloads, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Logf("Failed to close database: %v", closeErr)
		}
	})

	cfg := config.Config{DiscordWebhookURL: server.URL, HighSeverityThreshold: "high"}
	h := NewHandler(db, cfg, handlerMetrics())
	// NewHandler builds the reporter from config.Config; the test needs the
	// same Discord URL the reporter saw, which it already has.
	return h, mock, &payloads
}

func postAlert(t *testing.T, fn http.HandlerFunc, payload map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// TestAlertHandlerAcceptsEventSourcedPayload is the regression test for the
// contract break that silently dropped every immediate alert: the handler used
// to reject payloads without incident_id, but ingest has no incident to name
// when it fires (the correlator creates it later).
func TestAlertHandlerAcceptsEventSourcedPayload(t *testing.T) {
	h, mock, payloads := newAlertHandler(t)

	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))

	rec := postAlert(t, h.AlertHandler, map[string]interface{}{
		"request_id":       "evt_1",
		"contract_version": 1,
		"title":            "Monitoring Target Down",
		"severity":         "critical",
		"environment":      "production",
		"services":         "grafana",
		"dedup_key":        "grafana:abc123",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an event-sourced alert, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp models.AlertResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Sent {
		t.Fatalf("expected alert to be sent, got %+v", resp)
	}
	if len(*payloads) != 1 {
		t.Fatalf("expected 1 discord delivery, got %d", len(*payloads))
	}
	if _, hasContent := (*payloads)[0]["content"]; hasContent {
		t.Error("alert used plain content instead of an embed")
	}
}

func TestAlertHandlerRequiresDedupKey(t *testing.T) {
	h, _, payloads := newAlertHandler(t)

	rec := postAlert(t, h.AlertHandler, map[string]interface{}{
		"title":    "Monitoring Target Down",
		"severity": "critical",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without dedup_key, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dedup_key") {
		t.Fatalf("expected the error to name dedup_key, got %q", rec.Body.String())
	}
	if len(*payloads) != 0 {
		t.Fatalf("invalid payload reached the channel (%d deliveries)", len(*payloads))
	}
}

func TestAlertResolvedHandlerAcceptsEventSourcedPayload(t *testing.T) {
	h, mock, payloads := newAlertHandler(t)

	// The resolve path releases the one-shot claim instead of taking it.
	mock.ExpectExec("DELETE FROM idempotency_records").
		WillReturnResult(sqlmock.NewResult(0, 1))

	rec := postAlert(t, h.AlertResolvedHandler, map[string]interface{}{
		"request_id":       "evt_2",
		"contract_version": 1,
		"title":            "Monitoring Target Down",
		"severity":         "critical",
		"environment":      "production",
		"dedup_key":        "grafana:abc123",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an event-sourced resolve, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(*payloads) != 1 {
		t.Fatalf("expected 1 discord delivery, got %d", len(*payloads))
	}
	embeds, ok := (*payloads)[0]["embeds"].([]interface{})
	if !ok || len(embeds) != 1 {
		t.Fatalf("expected exactly 1 embed, got %#v", (*payloads)[0]["embeds"])
	}
	embed, ok := embeds[0].(map[string]interface{})
	if !ok {
		t.Fatalf("embed is not an object: %#v", embeds[0])
	}
	if embed["color"] != float64(0x2ECC71) {
		t.Errorf("resolved alert bar should be green, got %v", embed["color"])
	}
}

func TestAlertHandlersRejectNonPost(t *testing.T) {
	h, _, _ := newAlertHandler(t)
	for name, fn := range map[string]http.HandlerFunc{"alert": h.AlertHandler, "resolved": h.AlertResolvedHandler} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", name, rec.Code)
		}
	}
}
