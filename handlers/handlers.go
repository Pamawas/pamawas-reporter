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

// Handler holds dependencies for HTTP handlers
type Handler struct {
	reporter *service.Reporter
	cfg      config.Config
	metrics  *metrics.Metrics
	db       *sql.DB
}

// NewHandler creates a new handler with dependencies
func NewHandler(db *sql.DB, cfg config.Config, m *metrics.Metrics) *Handler {
	reporterCfg := service.ReporterConfig{
		DatabaseURL:        cfg.DatabaseURL,
		Port:               cfg.Port,
		DiscordWebhookURL:  cfg.DiscordWebhookURL,
		TelegramBotToken:   cfg.TelegramBotToken,
		TelegramChatID:     cfg.TelegramChatID,
		EmailSMTPHost:      cfg.EmailSMTPHost,
		EmailSMTPPort:      cfg.EmailSMTPPort,
		EmailUsername:      cfg.EmailUsername,
		EmailPassword:      cfg.EmailPassword,
		EmailFrom:          cfg.EmailFrom,
		ReportTemplate:     cfg.ReportTemplate,
		ReportInterval:     cfg.ReportInterval,
		Mode:               cfg.Mode,
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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.db.PingContext(r.Context()); err != nil {
		h.metrics.DBConnectionErrors.Inc()
		log.Error().Err(err).Msg("Health check failed: database connection")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(models.HealthResponse{
			Status: "unhealthy",
			Error:  fmt.Sprintf("Database connection failed: %v", err),
		})
		return
	}

	h.reporter.MuLock()
	lastSent := h.reporter.LastSent()
	running := h.reporter.Running()
	h.reporter.MuUnlock()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(models.HealthResponse{
		Status:    "healthy",
		Timestamp: time.Now().UTC(),
		LastSent:  lastSent,
		Running:   running,
		Version:   "1.0.0",
	})
}

// ReadyHandler handles readiness check requests
func (h *Handler) ReadyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.db.PingContext(r.Context()); err != nil {
		log.Error().Err(err).Msg("Readiness check failed: database not ready")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(models.ReadyResponse{
			Status: "not ready",
			Error:  fmt.Sprintf("Database not ready: %v", err),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(models.ReadyResponse{Status: "ready"})
}

// ReportHandler handles manual report generation
func (h *Handler) ReportHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.reporter.GenerateAndSendDailyReport(); err != nil {
		http.Error(w, fmt.Sprintf("Report generation failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(models.TriggerResponse{
		Message: "Daily report triggered successfully",
	})
}

// StatusHandler returns the current status of the reporter
func (h *Handler) StatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.reporter.MuLock()
	defer h.reporter.MuUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"last_sent": h.reporter.LastSent(),
		"running":   h.reporter.Running(),
		"uptime":    time.Since(h.reporter.StartTime()).String(),
		"version":   "1.0.0",
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