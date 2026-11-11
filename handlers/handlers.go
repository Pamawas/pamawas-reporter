package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	"github.com/Pamawas/pamawas-reporter/config"
	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
	"github.com/Pamawas/pamawas-reporter/service"
)

// writeJSON encodes v as JSON to w, logging any error.
func writeJSON(w http.ResponseWriter, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("failed to encode JSON response")
	}
}

// Handler holds dependencies for HTTP handlers
type Handler struct {
	reporter *service.Reporter
	cfg      config.Config
	metrics  *metrics.Metrics
	db       *sql.DB
}

// NewHandler creates a new handler with dependencies.
func NewHandler(db *sql.DB, cfg config.Config, m *metrics.Metrics) *Handler {
	reporterCfg := service.ReporterConfig{
		DatabaseURL:                      cfg.DatabaseURL,
		Port:                             cfg.Port,
		DiscordWebhookURL:                cfg.DiscordWebhookURL,
		TelegramBotToken:                 cfg.TelegramBotToken,
		TelegramChatID:                   cfg.TelegramChatID,
		TelegramAPIBaseURL:               cfg.TelegramAPIBaseURL,
		EmailSMTPHost:                    cfg.EmailSMTPHost,
		EmailSMTPPort:                    cfg.EmailSMTPPort,
		EmailUsername:                    cfg.EmailUsername,
		EmailPassword:                    cfg.EmailPassword,
		EmailFrom:                        cfg.EmailFrom,
		EmailTo:                          cfg.EmailTo,
		HighSeverityInvestigationTimeout: cfg.HighSeverityInvestigationTimeout,
		PrometheusURL:                    cfg.PrometheusURL,
		AvailabilityMetric:               cfg.AvailabilityMetric,
		ReportTemplate:                   cfg.ReportTemplate,
	}

	reporter := service.NewReporter(db, reporterCfg, m)

	return &Handler{
		reporter: reporter,
		cfg:      cfg,
		metrics:  m,
		db:       db,
	}
}

// HealthHandler handles health check requests
func (h *Handler) HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		log.Error().Str("method", r.Method).Str("path", r.URL.Path).
			Msg("HealthHandler: method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Debug().Str("path", r.URL.Path).Msg("HealthHandler: received health check")
	if err := h.db.PingContext(r.Context()); err != nil {
		h.metrics.DBConnectionErrors.Inc()
		log.Error().Err(err).Msg("HealthHandler: database connection failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, models.HealthResponse{Status: "unhealthy", Error: fmt.Sprintf("Database connection failed: %v", err)})
		return
	}
	w.WriteHeader(http.StatusOK)
	writeJSON(w, models.HealthResponse{Status: "healthy", Timestamp: time.Now().UTC(), Version: "1.0.0"})
}

// ReadyHandler handles readiness check requests
func (h *Handler) ReadyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		log.Error().Str("method", r.Method).Str("path", r.URL.Path).
			Msg("ReadyHandler: method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Debug().Str("path", r.URL.Path).Msg("ReadyHandler: received readiness check")
	if err := h.db.PingContext(r.Context()); err != nil {
		log.Error().Err(err).Msg("ReadyHandler: database not ready")
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, models.ReadyResponse{Status: "not ready", Error: fmt.Sprintf("Database not ready: %v", err)})
		return
	}
	w.WriteHeader(http.StatusOK)
	writeJSON(w, models.ReadyResponse{Status: "ready"})
}

// ReportHandler handles typed report requests from the scheduler.
func (h *Handler) ReportHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		log.Error().Str("method", r.Method).Str("path", r.URL.Path).
			Msg("ReportHandler: method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Info().
		Str("request_id", r.Header.Get("X-Request-ID")).
		Str("path", r.URL.Path).
		Msg("ReportHandler: processing report request")

	var payload models.ReportPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Error().Err(err).Str("path", r.URL.Path).
			Str("request_id", payload.RequestID).
			Msg("ReportHandler: invalid request body")
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Input validation
	if payload.RequestID == "" {
		log.Error().Str("path", r.URL.Path).Msg("ReportHandler: missing request_id")
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}
	if payload.ReportType == "" {
		log.Error().Str("request_id", payload.RequestID).Msg("ReportHandler: missing report_type")
		http.Error(w, "report_type is required", http.StatusBadRequest)
		return
	}
	if payload.ReportType != "daily" && payload.ReportType != "high_severity" {
		log.Error().Str("request_id", payload.RequestID).
			Str("report_type", payload.ReportType).
			Msg("ReportHandler: invalid report_type")
		http.Error(w, "report_type must be 'daily' or 'high_severity'", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse(time.RFC3339, payload.PeriodStart); err != nil {
		log.Error().Err(err).Str("request_id", payload.RequestID).
			Str("period_start", payload.PeriodStart).
			Msg("ReportHandler: invalid period_start")
		http.Error(w, "period_start must be a valid RFC3339 timestamp", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse(time.RFC3339, payload.PeriodEnd); err != nil {
		log.Error().Err(err).Str("request_id", payload.RequestID).
			Str("period_end", payload.PeriodEnd).
			Msg("ReportHandler: invalid period_end")
		http.Error(w, "period_end must be a valid RFC3339 timestamp", http.StatusBadRequest)
		return
	}
	periodStart, _ := time.Parse(time.RFC3339, payload.PeriodStart)
	periodEnd, _ := time.Parse(time.RFC3339, payload.PeriodEnd)
	if !periodEnd.After(periodStart) {
		log.Error().Str("request_id", payload.RequestID).
			Msg("ReportHandler: period_end must be after period_start")
		http.Error(w, "period_end must be after period_start", http.StatusBadRequest)
		return
	}
	if payload.ReportType == "high_severity" && len(payload.IncidentIDs) == 0 {
		log.Error().Str("request_id", payload.RequestID).Msg("ReportHandler: high_severity requires incident_ids")
		http.Error(w, "incident_ids is required for high_severity reports", http.StatusBadRequest)
		return
	}

	log.Debug().
		Str("request_id", payload.RequestID).
		Str("report_type", payload.ReportType).
		Msg("ReportHandler: decoded payload")

	resp, err := h.reporter.ProcessReportRequest(r.Context(), payload)
	if err != nil {
		log.Error().Err(err).
			Str("request_id", payload.RequestID).
			Str("report_type", payload.ReportType).
			Msg("ReportHandler: report generation failed")
		http.Error(w, fmt.Sprintf("Report generation failed: %v", err), http.StatusInternalServerError)
		return
	}

	log.Info().
		Str("report_id", resp.ReportID).
		Str("request_id", payload.RequestID).
		Str("status", resp.Status).
		Msg("ReportHandler: report generated successfully")

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, resp)
}

// StatusHandler returns the current status of the reporter
func (h *Handler) StatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		log.Error().Str("method", r.Method).Str("path", r.URL.Path).
			Msg("StatusHandler: method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Debug().Str("path", r.URL.Path).Msg("StatusHandler: requested")

	// Gather circuit breaker states per channel
	cbStates := make(map[string]interface{})
	for _, channel := range []string{"discord", "telegram", "email", "prometheus"} {
		cb := h.reporter.CircuitBreakerFor(channel)
		snap := cb.Snapshot()
		cbStates[channel] = map[string]interface{}{
			"state":    snap.State,
			"failures": snap.Failures,
		}
	}

	writeJSON(w, map[string]interface{}{
		"uptime":             time.Since(h.reporter.StartTime()).String(),
		"version":            "1.0.0",
		"circuit_breakers":   cbStates,
	})
}

// MetricsHandler returns the Prometheus metrics handler
func (h *Handler) MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// Reporter returns the reporter instance
func (h *Handler) Reporter() *service.Reporter {
	return h.reporter
}

// RetryStalledDeliveriesHandler recovers stalled delivery attempts and triggers
// a retry sweep. Used by the scheduler or an operator to recover from leaked
// leases.
func (h *Handler) RetryStalledDeliveriesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		log.Error().Str("method", r.Method).Str("path", r.URL.Path).
			Msg("RetryStalledDeliveriesHandler: method not allowed")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	count, err := h.reporter.RecoverStalledDeliveries(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("RetryStalledDeliveriesHandler: failed to recover stalled deliveries")
		http.Error(w, fmt.Sprintf("Failed to recover stalled deliveries: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"recovered": count,
		"message":   "Stalled delivery attempts recovered",
	})
}
