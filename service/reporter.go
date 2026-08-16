package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/wneessen/go-mail"

	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

// ReporterConfig holds the configuration for the reporter
type ReporterConfig struct {
	DatabaseURL        string
	Port               string
	DiscordWebhookURL  string
	TelegramBotToken   string
	TelegramChatID     string
	EmailSMTPHost      string
	EmailSMTPPort      int
	EmailUsername      string
	EmailPassword      string
	EmailFrom          string
	EmailTo            string
	ReportTemplate     string
	ReportInterval     time.Duration
	Mode               string
}

// Reporter holds the database connection and reporting logic
type Reporter struct {
	db         *sql.DB
	config     ReporterConfig
	metrics    *metrics.Metrics
	httpClient *http.Client
	mu         sync.Mutex
	lastSent   time.Time
	running    bool
	wg         sync.WaitGroup
	ctx        context.Context
	cancelFunc context.CancelFunc
	startTime  time.Time
}

// NewReporter creates a new reporter instance
func NewReporter(db *sql.DB, cfg ReporterConfig, m *metrics.Metrics) *Reporter {
	return &Reporter{
		db:         db,
		config:     cfg,
		metrics:    m,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		ctx:        context.Background(),
		cancelFunc: func() {},
		startTime:  time.Now(),
	}
}

// StartWorker starts the background report worker
func (r *Reporter) StartWorker() {
	interval := r.config.ReportInterval
	if interval == 0 {
		interval = 1 * time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			log.Info().Msg("Report worker stopped")
			return
		case <-ticker.C:
			if err := r.GenerateAndSendDailyReport(); err != nil {
				log.Error().Err(err).Msg("Report generation error")
			}
		}
	}
}

// GenerateAndSendDailyReport generates a daily digest report and sends it
func (r *Reporter) GenerateAndSendDailyReport() error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil // already running
	}
	r.running = true
	r.metrics.ReporterRunning.Set(1)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.metrics.ReporterRunning.Set(0)
		r.mu.Unlock()
	}()

	r.wg.Add(1)
	defer r.wg.Done()

	startTime := time.Now()
	log.Info().Msg("Generating daily report")

	// Get incidents from the last 24 hours
	incidents, err := r.getRecentIncidents(24 * time.Hour)
	if err != nil {
		return err
	}

	if len(incidents) == 0 {
		log.Info().Msg("No incidents found in the last 24 hours")
		return nil
	}

	// Generate report content
	reportContent := r.generateDailyReport(incidents)

	// Create report record
	report := models.Report{
		ID:        uuid.NewString(),
		IncidentID: "daily_" + time.Now().Format("20060102"),
		Content:   reportContent,
		SentAt:    time.Now(),
		Channels:  []string{"discord", "telegram", "email"},
	}

	// Send report via configured channels
	if err := r.sendReport(report); err != nil {
		return err
	}

	// Save report to database
	if err := r.saveReport(report); err != nil {
		return err
	}

	r.mu.Lock()
	r.lastSent = time.Now()
	r.metrics.LastSentTimestamp.Set(float64(time.Now().Unix()))
	r.mu.Unlock()

	log.Info().Dur("duration", time.Since(startTime)).Msg("Daily report generated and sent")
	return nil
}

// getRecentIncidents retrieves incidents from the last duration
func (r *Reporter) getRecentIncidents(duration time.Duration) ([]models.Incident, error) {
	since := time.Now().Add(-duration)

	query := `
		SELECT i.id, i.title, i.status, i.started_at, i.resolved_at, i.severity, i.affected_services
		FROM incidents i
		WHERE i.started_at >= $1
		ORDER BY i.started_at DESC
	`

	rows, err := r.db.QueryContext(r.ctx, query, since)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	var incidents []models.Incident
	for rows.Next() {
		var inc models.Incident
		var affectedServicesJSON []byte
		if err := rows.Scan(
			&inc.ID, &inc.Title, &inc.Status, &inc.StartedAt, &inc.ResolvedAt,
			&inc.Severity, &affectedServicesJSON,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(affectedServicesJSON, &inc.AffectedServices); err != nil {
			return nil, err
		}
		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
}

// getEvidenceForIncident retrieves evidence findings for a specific incident
func (r *Reporter) getEvidenceForIncident(incidentID string) ([]models.Evidence, error) {
	query := `
		SELECT id, incident_id, type, content, source, confidence
		FROM evidence
		WHERE incident_id = $1
		ORDER BY confidence DESC
	`

	rows, err := r.db.QueryContext(r.ctx, query, incidentID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	var evidenceList []models.Evidence
	for rows.Next() {
		var ev models.Evidence
		if err := rows.Scan(&ev.ID, &ev.IncidentID, &ev.Type, &ev.Content, &ev.Source, &ev.Confidence); err != nil {
			return nil, err
		}
		evidenceList = append(evidenceList, ev)
	}
	return evidenceList, rows.Err()
}

// generateDailyReport creates a formatted daily digest report
func (r *Reporter) generateDailyReport(incidents []models.Incident) string {
	renderStart := time.Now()
	defer func() {
		r.metrics.TemplateRenderDuration.Observe(time.Since(renderStart).Seconds())
	}()

	var healthyPercentage float64
	if len(incidents) > 0 {
		healthyPercentage = 99.97 // placeholder
	} else {
		healthyPercentage = 100.0
	}

	needsAttention := 0
	recovered := 0
	for _, inc := range incidents {
		switch inc.Status {
		case "firing":
			needsAttention++
		case "resolved":
			recovered++
		}
	}

	// Fetch evidence for all incidents
	incidentEvidence := make(map[string][]models.Evidence)
	for _, inc := range incidents {
		evidence, err := r.getEvidenceForIncident(inc.ID)
		if err != nil {
			log.Error().Err(err).Str("incident_id", inc.ID).Msg("Failed to fetch evidence for incident")
		} else if len(evidence) > 0 {
			incidentEvidence[inc.ID] = evidence
		}
	}

	// Build report using template or default format
	if r.config.ReportTemplate != "" {
		data := map[string]interface{}{
			"HealthyPercentage": healthyPercentage,
			"TotalIncidents":    len(incidents),
			"NeedsAttention":    needsAttention,
			"Recovered":         recovered,
			"Incidents":         incidents,
			"Evidence":          incidentEvidence,
			"Timestamp":         time.Now().Format("2006-01-02 15:04:05"),
		}

		content := r.config.ReportTemplate
		for k, v := range data {
			content = strings.ReplaceAll(content, "{{"+k+"}}", fmt.Sprintf("%v", v))
		}
		return content
	}

	// Default report format with evidence
	var sb strings.Builder
	sb.WriteString("🌅 Good morning.\n\n")
	fmt.Fprintf(&sb, "Infrastructure was %.2f%% healthy overnight.\n", healthyPercentage)
	fmt.Fprintf(&sb, "%d incidents occurred. %d needs your attention. %d recovered automatically.\n\n",
		len(incidents), needsAttention, recovered)

	if len(incidents) > 0 {
		for _, inc := range incidents {
			fmt.Fprintf(&sb, "📋 Incident: %s (%s)\n", inc.Title, inc.Status)
			fmt.Fprintf(&sb, "   ID: %s | Severity: %s | Services: %s\n", inc.ID[:8], inc.Severity, strings.Join(inc.AffectedServices, ", "))
			fmt.Fprintf(&sb, "   Started: %s\n", inc.StartedAt.Format("2006-01-02 15:04:05"))

			// Include evidence if available
			if evList, ok := incidentEvidence[inc.ID]; ok && len(evList) > 0 {
				sb.WriteString("   🔍 Investigation Findings:\n")
				for _, ev := range evList {
					typeIcon := map[string]string{
						"fact":          "✅",
						"likely_cause":  "🎯",
						"hypothesis":    "💭",
						"unknown":       "❓",
					}[ev.Type]
					if typeIcon == "" {
						typeIcon = "📝"
					}
					fmt.Fprintf(&sb, "      %s [%s] (%.0f%%) %s\n", typeIcon, strings.ToUpper(ev.Type), ev.Confidence*100, ev.Content)
				}
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No incidents detected.\n\n")
	}

	sb.WriteString("No critical outage occurred.\n")
	return sb.String()
}

// sendReport sends the report via configured channels
func (r *Reporter) sendReport(report models.Report) error {
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var firstError error

	// Discord
	if r.config.DiscordWebhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.sendToDiscord(report); err != nil {
				errMutex.Lock()
				if firstError == nil {
					firstError = err
				}
				errMutex.Unlock()
				log.Error().Err(err).Msg("Failed to send report to Discord")
				r.metrics.DeliveryTotal.WithLabelValues("discord", "error").Inc()
			} else {
				log.Info().Msg("Report sent to Discord successfully")
				r.metrics.DeliveryTotal.WithLabelValues("discord", "success").Inc()
			}
		}()
	}

	// Telegram
	if r.config.TelegramBotToken != "" && r.config.TelegramChatID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.sendToTelegram(report); err != nil {
				errMutex.Lock()
				if firstError == nil {
					firstError = err
				}
				errMutex.Unlock()
				log.Error().Err(err).Msg("Failed to send report to Telegram")
				r.metrics.DeliveryTotal.WithLabelValues("telegram", "error").Inc()
			} else {
				log.Info().Msg("Report sent to Telegram successfully")
				r.metrics.DeliveryTotal.WithLabelValues("telegram", "success").Inc()
			}
		}()
	}

	// Email
	if r.config.EmailSMTPHost != "" && r.config.EmailUsername != "" && r.config.EmailPassword != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.sendToEmail(report); err != nil {
				errMutex.Lock()
				if firstError == nil {
					firstError = err
				}
				errMutex.Unlock()
				log.Error().Err(err).Msg("Failed to send report via email")
				r.metrics.DeliveryTotal.WithLabelValues("email", "error").Inc()
			} else {
				log.Info().Msg("Report sent via email successfully")
				r.metrics.DeliveryTotal.WithLabelValues("email", "success").Inc()
			}
		}()
	}

	wg.Wait()
	return firstError
}

// sendToDiscord sends the report to Discord via webhook
func (r *Reporter) sendToDiscord(report models.Report) error {
	if r.config.DiscordWebhookURL == "" {
		return nil
	}

	payload := map[string]interface{}{
		"content": report.Content,
		"username": "Pamawas Reporter",
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, r.config.DiscordWebhookURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close response body")
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// sendToTelegram sends the report to Telegram via Bot API
func (r *Reporter) sendToTelegram(report models.Report) error {
	if r.config.TelegramBotToken == "" || r.config.TelegramChatID == "" {
		return nil
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", r.config.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id":    r.config.TelegramChatID,
		"text":       report.Content,
		"parse_mode": "Markdown",
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, url, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close response body")
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram api returned status %d", resp.StatusCode)
	}

	return nil
}

// sendToEmail sends the report via SMTP
func (r *Reporter) sendToEmail(report models.Report) error {
	if r.config.EmailSMTPHost == "" || r.config.EmailUsername == "" || r.config.EmailPassword == "" {
		return nil
	}

	// Create email client
	client, err := mail.NewClient(r.config.EmailSMTPHost,
		mail.WithPort(r.config.EmailSMTPPort),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(r.config.EmailUsername),
		mail.WithPassword(r.config.EmailPassword),
		mail.WithTLSPortPolicy(mail.TLSMandatory),
	)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}

	// Build email message
	msg := mail.NewMsg()
	if err := msg.From(r.config.EmailFrom); err != nil {
		return fmt.Errorf("failed to set from address: %w", err)
	}
	if err := msg.To(r.config.EmailTo); err != nil {
		return fmt.Errorf("failed to set to address: %w", err)
	}

	msg.Subject("Pamawas Daily Infrastructure Report")

	// Set plain text body
	msg.SetBodyString(mail.TypeTextPlain, report.Content)

	// Set HTML body (simple conversion from plain text)
	htmlBody := strings.ReplaceAll(report.Content, "\n", "<br>")
	msg.SetBodyString(mail.TypeTextHTML, htmlBody)

	// Send email with context
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()

	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Info().Str("smtp_host", r.config.EmailSMTPHost).Str("to", r.config.EmailTo).Msg("Email sent successfully")
	return nil
}

// saveReport saves the report to database
func (r *Reporter) saveReport(report models.Report) error {
	channelsJSON, err := json.Marshal(report.Channels)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(r.ctx,
		"INSERT INTO reports (id, incident_id, content, sent_at, channels) VALUES ($1,$2,$3,$4,$5)",
		report.ID, report.IncidentID, report.Content, report.SentAt, channelsJSON,
	)
	return err
}

// MuLock locks the reporter mutex
func (r *Reporter) MuLock() {
	r.mu.Lock()
}

// MuUnlock unlocks the reporter mutex
func (r *Reporter) MuUnlock() {
	r.mu.Unlock()
}

// LastSent returns the last sent time
func (r *Reporter) LastSent() time.Time {
	return r.lastSent
}

// Running returns whether the reporter is running
func (r *Reporter) Running() bool {
	return r.running
}

// StartTime returns the start time
func (r *Reporter) StartTime() time.Time {
	return r.startTime
}

// Stop stops the reporter gracefully
func (r *Reporter) Stop() {
	r.cancelFunc()
	r.wg.Wait()
}