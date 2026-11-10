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
	writeJSON(w, map[string]interface{}{
		"uptime":  time.Since(h.reporter.StartTime()).String(),
		"version": "1.0.0",
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
