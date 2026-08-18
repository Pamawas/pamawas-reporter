package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestLoggingMiddlewareAddsContextAndLogsResponse(t *testing.T) {
	var output bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = old })

	var contextLog bytes.Buffer
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := GetLoggerFromContext(r.Context()).Output(&contextLog)
		logger.Info().Msg("inside")
		w.WriteHeader(http.StatusCreated)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/items?q=1", nil)
	req.Header.Set("X-Trace-ID", "trace-123")
	rr := httptest.NewRecorder()

	LoggingMiddleware("reporter", next).ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d", rr.Code)
	}
	var entry map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v; output=%q", err, output.String())
	}
	if entry["service"] != "reporter" || entry["trace_id"] != "trace-123" || entry["status"] != float64(201) {
		t.Fatalf("unexpected log entry: %#v", entry)
	}
	if !bytes.Contains(contextLog.Bytes(), []byte(`"service":"reporter"`)) {
		t.Fatalf("request context missing logger fields: %s", contextLog.String())
	}
}

func TestLoggingMiddlewareGeneratesTraceID(t *testing.T) {
	var output bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = old })
	LoggingMiddleware("reporter", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(
		httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
	)
	var entry map[string]interface{}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if trace, ok := entry["trace_id"].(string); !ok || len(trace) != 8 {
		t.Fatalf("generated trace_id = %#v", entry["trace_id"])
	}
}

func TestErrorLoggingMiddlewareRecoversPanic(t *testing.T) {
	var output bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = old })
	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/panic", nil)
	req.Header.Set("X-Trace-ID", "panic-trace")

	ErrorLoggingMiddleware("reporter", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rr.Code)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"trace_id":"panic-trace"`)) || !bytes.Contains(output.Bytes(), []byte("Panic recovered")) {
		t.Fatalf("unexpected panic log: %s", output.String())
	}
}

func TestAddContextFields(t *testing.T) {
	var output bytes.Buffer
	logger := zerolog.New(&output)
	ctx := logger.WithContext(context.Background())
	ctx = AddContextFields(ctx, map[string]interface{}{"incident_id": "inc-1", "attempt": 2})
	contextLogger := GetLoggerFromContext(ctx)
	contextLogger.Info().Msg("test")
	if !bytes.Contains(output.Bytes(), []byte(`"incident_id":"inc-1"`)) || !bytes.Contains(output.Bytes(), []byte(`"attempt":2`)) {
		t.Fatalf("fields missing from logger: %s", output.String())
	}
}
