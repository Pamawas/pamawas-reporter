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

const (
	// ReportPolicyVersion is the version of the report policy for idempotency
	ReportPolicyVersion = 1

	// TemplateVersion is the current template version
	TemplateVersion = "pamawas-report-v1"

	// DefaultTimezone is the default IANA timezone for daily reports
	DefaultTimezone = "Asia/Jakarta"
)

// ReporterConfig holds the configuration for the reporter
type ReporterConfig struct {
	DatabaseURL            string
	Port                   string
	DiscordWebhookURL      string
	TelegramBotToken       string
	TelegramChatID         string
	TelegramAPIBaseURL     string
	EmailSMTPHost          string
	EmailSMTPPort          int
	EmailUsername          string
	EmailPassword          string
	EmailFrom              string
	EmailTo                string
	ReportTemplate         string
	ReportInterval         time.Duration
	Mode                   string
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

// StartWorker starts the background report worker (legacy compatibility)
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

// GenerateAndSendDailyReport generates a daily digest report and sends it (legacy compatibility)
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
	log.Info().Msg("Generating daily report (legacy)")

	// Get incidents from the last 24 hours (legacy behavior)
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

	// Create report record (legacy)
	report := models.Report{
		ID:         uuid.NewString(),
		IncidentID: "daily_" + time.Now().Format("20060102"),
		Content:    reportContent,
		SentAt:     time.Now(),
		Channels:   []string{"discord", "telegram", "email"},
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

// ProcessReportRequest processes a report request from the scheduler
func (r *Reporter) ProcessReportRequest(ctx context.Context, payload models.ReportPayload) (*models.ReportResponse, error) {
	log.Info().
		Str("request_id", payload.RequestID).
		Str("report_type", payload.ReportType).
		Msg("Processing report request")

	// Parse period boundaries
	periodStart, err := time.Parse(time.RFC3339, payload.PeriodStart)
	if err != nil {
		return nil, fmt.Errorf("invalid period_start: %w", err)
	}
	periodEnd, err := time.Parse(time.RFC3339, payload.PeriodEnd)
	if err != nil {
		return nil, fmt.Errorf("invalid period_end: %w", err)
	}

	// Check if report request exists and claim it
	requestID, err := r.claimReportRequest(ctx, payload.RequestID, periodStart, periodEnd, payload.Timezone)
	if err != nil {
		return nil, fmt.Errorf("failed to claim report request: %w", err)
	}

	// Select incidents based on report type
	var incidentIDs []string
	var inclusionReasons map[string]string

	switch payload.ReportType {
	case "daily":
		incidentIDs, inclusionReasons, err = r.selectDailyIncidents(ctx, periodStart, periodEnd, payload.Timezone)
		if err != nil {
			return nil, fmt.Errorf("failed to select daily incidents: %w", err)
		}
	case "high_severity":
		incidentIDs = payload.IncidentIDs
		inclusionReasons = make(map[string]string)
		for _, id := range incidentIDs {
			inclusionReasons[id] = models.InclusionReasonHighSeverityImmediate
		}
	default:
		return nil, fmt.Errorf("unknown report type: %s", payload.ReportType)
	}

	// Load evidence for all selected incidents
	incidentEvidence, err := r.loadEvidenceForIncidents(ctx, incidentIDs)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load evidence for incidents")
		// Continue with empty evidence rather than failing
		incidentEvidence = make(map[string][]models.Evidence)
	}

	// Generate report content
	reportContent, err := r.generateReportContent(ctx, payload.ReportType, periodStart, periodEnd, payload.Timezone, incidentIDs, incidentEvidence, inclusionReasons)
	if err != nil {
		return nil, fmt.Errorf("failed to generate report content: %w", err)
	}

	// Create report record
	report := models.Report{
		ID:              uuid.NewString(),
		RequestID:       requestID,
		ReportType:      payload.ReportType,
		PeriodStart:     periodStart,
		PeriodEnd:       periodEnd,
		Timezone:        payload.Timezone,
		TemplateVersion: TemplateVersion,
		Content:         reportContent,
		GeneratedAt:     time.Now(),
		Status:          models.ReportStatusGenerated,
		CreatedAt:       time.Now(),
	}

	// Persist report and report_incidents transactionally
	if err := r.persistReportWithIncidents(ctx, report, incidentIDs, inclusionReasons); err != nil {
		return nil, fmt.Errorf("failed to persist report: %w", err)
	}

	// Create pending delivery attempts for configured channels
	if err := r.createDeliveryAttempts(ctx, report); err != nil {
		return nil, fmt.Errorf("failed to create delivery attempts: %w", err)
	}

	// Update report request status to generated
	if err := r.updateReportRequestStatus(ctx, requestID, "generated", 0, "", nil); err != nil {
		log.Error().Err(err).Str("request_id", requestID).Msg("Failed to update report request status")
		// Don't fail the whole operation for this
	}

	// Send report via configured channels (async)
	go func() {
		r.deliverReport(report)
	}()

	log.Info().
		Str("report_id", report.ID).
		Str("request_id", requestID).
		Int("incident_count", len(incidentIDs)).
		Msg("Report generated and delivery initiated")

	return &models.ReportResponse{
		ReportID: report.ID,
		Status:   report.Status,
		Message:  "Report generated successfully",
	}, nil
}

// claimReportRequest claims a report request for processing
func (r *Reporter) claimReportRequest(ctx context.Context, requestID string, _periodStart, _periodEnd time.Time, _timezone string) (string, error) {
	query := `
		UPDATE report_requests
		SET status = 'generating',
		    attempts = attempts + 1,
		    updated_at = now(),
		    lease_expires_at = now() + interval '10 minutes'
		WHERE id = $1
		  AND status IN ('pending', 'failed_retryable')
		  AND (lease_expires_at IS NULL OR lease_expires_at < now())
		RETURNING id
	`

	var returnedID string
	err := r.db.QueryRowContext(ctx, query, requestID).Scan(&returnedID)
	if err == sql.ErrNoRows {
		// Check if already generated
		var status string
		err = r.db.QueryRowContext(ctx, "SELECT status FROM report_requests WHERE id = $1", requestID).Scan(&status)
		if err == nil && status == "generated" {
			return "", fmt.Errorf("report request already generated")
		}
		return "", fmt.Errorf("report request not available for processing: %w", err)
	}
	if err != nil {
		return "", err
	}

	return returnedID, nil
}

// selectDailyIncidents selects incidents for a daily report using lifecycle overlap
func (r *Reporter) selectDailyIncidents(ctx context.Context, periodStart, periodEnd time.Time, _timezone string) ([]string, map[string]string, error) {
	// Lifecycle overlap: incident [started_at, COALESCE(resolved_at, infinity)) overlaps [period_start, period_end)
	query := `
		SELECT i.id, i.started_at, i.resolved_at, i.status
		FROM incidents i
		WHERE i.started_at < $2
		  AND (i.resolved_at IS NULL OR i.resolved_at > $1)
		  AND i.status IN ('open', 'investigating', 'resolved', 'suppressed')
		ORDER BY i.started_at DESC
	`

	rows, err := r.db.QueryContext(ctx, query, periodStart, periodEnd)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	var incidentIDs []string
	inclusionReasons := make(map[string]string)

	for rows.Next() {
		var id string
		var startedAt, resolvedAt time.Time
		var status string
		var resolvedAtNull sql.NullTime

		if err := rows.Scan(&id, &startedAt, &resolvedAtNull, &status); err != nil {
			return nil, nil, err
		}
		if resolvedAtNull.Valid {
			resolvedAt = resolvedAtNull.Time
		}

		incidentIDs = append(incidentIDs, id)

		// Determine inclusion reason
		if startedAt.After(periodStart) && startedAt.Before(periodEnd) {
			inclusionReasons[id] = models.InclusionReasonNewlyStarted
		} else if resolvedAtNull.Valid && resolvedAt.After(periodStart) && resolvedAt.Before(periodEnd) {
			inclusionReasons[id] = models.InclusionReasonResolvedDuring
		} else {
			inclusionReasons[id] = models.InclusionReasonOngoing
		}
	}

	return incidentIDs, inclusionReasons, rows.Err()
}

// loadEvidenceForIncidents loads the latest terminal investigation evidence for each incident
func (r *Reporter) loadEvidenceForIncidents(ctx context.Context, incidentIDs []string) (map[string][]models.Evidence, error) {
	if len(incidentIDs) == 0 {
		return make(map[string][]models.Evidence), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(incidentIDs))
	args := make([]interface{}, len(incidentIDs))
	for i, id := range incidentIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := "SELECT e.id, e.incident_id, e.type, e.content, e.source, e.confidence,\n" +
		"       e.supports_evidence, e.contradicts_evidence, e.ordinal\n" +
		"FROM evidence e\n" +
		"JOIN investigation_runs ir ON e.run_id = ir.id\n" +
		"WHERE e.incident_id IN (" + strings.Join(placeholders, ",") + ")\n" +
		"  AND ir.status = 'completed'\n" +
		"ORDER BY e.incident_id, ir.completed_at DESC, e.ordinal"
	// #nosec G202 -- placeholders are parameterized, not user input

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	incidentEvidence := make(map[string][]models.Evidence)
	seenIncidents := make(map[string]bool)

	for rows.Next() {
		var ev models.Evidence
		var supportsJSON, contradictsJSON []byte
		var incidentID string

		if err := rows.Scan(
			&ev.ID, &incidentID, &ev.Type, &ev.Content, &ev.Source, &ev.Confidence,
			&supportsJSON, &contradictsJSON, &ev.Ordinal,
		); err != nil {
			return nil, err
		}

		if err := json.Unmarshal(supportsJSON, &ev.Supports); err != nil {
			ev.Supports = []string{}
		}
		if err := json.Unmarshal(contradictsJSON, &ev.Contradicts); err != nil {
			ev.Contradicts = []string{}
		}

		// Only take evidence from the latest completed run per incident
		if !seenIncidents[incidentID] {
			seenIncidents[incidentID] = true
			incidentEvidence[incidentID] = []models.Evidence{}
		}
		incidentEvidence[incidentID] = append(incidentEvidence[incidentID], ev)
	}

	return incidentEvidence, rows.Err()
}

// generateReportContent generates the report content with honest rendering
func (r *Reporter) generateReportContent(
	ctx context.Context,
	reportType string,
	periodStart, periodEnd time.Time,
	timezone string,
	incidentIDs []string,
	incidentEvidence map[string][]models.Evidence,
	inclusionReasons map[string]string,
) (string, error) {

	// Load incident details
	incidents, err := r.getIncidentsByIDs(ctx, incidentIDs)
	if err != nil {
		return "", err
	}

	var sb strings.Builder

	writeReportHeader(&sb, reportType, periodStart, periodEnd, timezone)

	// Summary
	totalIncidents := len(incidents)
	needsAttention, resolved := countIncidentStatuses(incidents)

	// Availability - only if we have a real metric
	availability := r.getAvailabilityMetric(ctx, periodStart, periodEnd)
	if availability >= 0 {
		fmt.Fprintf(&sb, "Infrastructure was %.2f%% available during this period.\n", availability)
	} else {
		sb.WriteString("Availability: unavailable (no named availability metric configured)\n")
	}

	fmt.Fprintf(&sb, "%d incidents occurred. %d need attention. %d resolved.\n\n",
		totalIncidents, needsAttention, resolved)

	// Incident details
	writeIncidentDetails(&sb, incidents, incidentEvidence, inclusionReasons)

	fmt.Fprintf(&sb, "---\nTemplate: %s | Generated: %s\n", TemplateVersion, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

	return sb.String(), nil
}

// writeReportHeader writes the report title and period header based on type.
func writeReportHeader(sb *strings.Builder, reportType string, periodStart, periodEnd time.Time, timezone string) {
	utcStart := periodStart.In(time.UTC).Format("2006-01-02 15:04:05 UTC")
	utcEnd := periodEnd.In(time.UTC).Format("2006-01-02 15:04:05 UTC")
	if reportType == "daily" {
		fmt.Fprintf(sb, "📅 Daily Infrastructure Report\n")
		fmt.Fprintf(sb, "Period: %s to %s (%s)\n\n", utcStart, utcEnd, timezone)
	} else {
		fmt.Fprintf(sb, "🚨 High Severity Immediate Report\n")
		fmt.Fprintf(sb, "Period: %s to %s (UTC)\n\n", utcStart, utcEnd)
	}
}

// countIncidentStatuses counts how many incidents need attention vs are resolved.
func countIncidentStatuses(incidents []models.Incident) (needsAttention, resolved int) {
	for _, inc := range incidents {
		switch inc.Status {
		case "open", "investigating":
			needsAttention++
		case "resolved":
			resolved++
		}
	}
	return needsAttention, resolved
}

// writeIncidentDetails writes a formatted block for each incident including
// its evidence findings.
func writeIncidentDetails(sb *strings.Builder, incidents []models.Incident, incidentEvidence map[string][]models.Evidence, inclusionReasons map[string]string) {
	if len(incidents) == 0 {
		sb.WriteString("No incidents detected.\n\n")
		return
	}
	for _, inc := range incidents {
		reasonLabel := inclusionReasonLabel(inclusionReasons[inc.ID])
		fmt.Fprintf(sb, "📋 Incident: %s (%s)%s\n", inc.Title, inc.Status, reasonLabel)
		fmt.Fprintf(sb, "   ID: %s | Severity: %s | Services: %s\n", inc.ID[:min(8, len(inc.ID))], inc.Severity, strings.Join(inc.AffectedServices, ", "))
		fmt.Fprintf(sb, "   Started: %s\n", inc.StartedAt.Format("2006-01-02 15:04:05"))
		if !inc.ResolvedAt.IsZero() {
			fmt.Fprintf(sb, "   Resolved: %s\n", inc.ResolvedAt.Format("2006-01-02 15:04:05"))
		}

		writeEvidence(sb, incidentEvidence[inc.ID])

		sb.WriteString("\n")
	}
}

// inclusionReasonLabel returns a human-readable suffix for the given inclusion
// reason, or an empty string if the reason is unknown.
func inclusionReasonLabel(reason string) string {
	switch reason {
	case models.InclusionReasonNewlyStarted:
		return " (newly started)"
	case models.InclusionReasonResolvedDuring:
		return " (resolved during period)"
	case models.InclusionReasonOngoing:
		return " (ongoing)"
	case models.InclusionReasonHighSeverityImmediate:
		return " (high severity immediate)"
	}
	return ""
}

// writeEvidence writes the investigation findings for an incident, or a notice
// that no investigation was available.
func writeEvidence(sb *strings.Builder, evList []models.Evidence) {
	if len(evList) == 0 {
		sb.WriteString("   🔍 Investigation Findings: investigation unavailable\n")
		return
	}
	sb.WriteString("   🔍 Investigation Findings:\n")
	for _, ev := range evList {
		fmt.Fprintf(sb, "      %s [%s] (%.0f%%) %s\n",
			evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100, ev.Content)
	}
}

// evidenceIcon returns the emoji icon for a given evidence type.
func evidenceIcon(evType string) string {
	switch evType {
	case "fact":
		return "✅"
	case "likely_cause":
		return "🎯"
	case "hypothesis":
		return "💭"
	case "unknown":
		return "❓"
	}
	return "📝"
}

// getIncidentsByIDs loads incident details by IDs
func (r *Reporter) getIncidentsByIDs(ctx context.Context, incidentIDs []string) ([]models.Incident, error) {
	if len(incidentIDs) == 0 {
		return []models.Incident{}, nil
	}

	placeholders := make([]string, len(incidentIDs))
	args := make([]interface{}, len(incidentIDs))
	for i, id := range incidentIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := "SELECT id, title, status, started_at, resolved_at, severity, affected_services\n" +
		"FROM incidents\n" +
		"WHERE id IN (" + strings.Join(placeholders, ",") + ")\n" +
		"ORDER BY started_at DESC"
	// #nosec G202 -- placeholders are parameterized, not user input

	rows, err := r.db.QueryContext(ctx, query, args...)
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
		var resolvedAt sql.NullTime

		if err := rows.Scan(
			&inc.ID, &inc.Title, &inc.Status, &inc.StartedAt, &resolvedAt,
			&inc.Severity, &affectedServicesJSON,
		); err != nil {
			return nil, err
		}
		if resolvedAt.Valid {
			inc.ResolvedAt = resolvedAt.Time
		}
		if err := json.Unmarshal(affectedServicesJSON, &inc.AffectedServices); err != nil {
			inc.AffectedServices = []string{}
		}
		incidents = append(incidents, inc)
	}

	return incidents, rows.Err()
}

// getAvailabilityMetric returns the availability percentage if a named metric exists, -1 otherwise
func (r *Reporter) getAvailabilityMetric(ctx context.Context, periodStart, periodEnd time.Time) float64 {
	// For MVP, we don't have a configured availability metric
	// This would query Prometheus for a specific availability metric
	// Return -1 to indicate unavailable
	return -1
}

// persistReportWithIncidents persists the report and report_incidents in a transaction
func (r *Reporter) persistReportWithIncidents(ctx context.Context, report models.Report, incidentIDs []string, inclusionReasons map[string]string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Error().Err(rbErr).Msg("Failed to rollback transaction")
			}
		}
	}()

	// Insert report
	_, err = tx.ExecContext(ctx, `
		INSERT INTO reports (id, request_id, report_type, period_start, period_end, timezone, template_version, content, generated_at, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, report.ID, report.RequestID, report.ReportType, report.PeriodStart, report.PeriodEnd,
		report.Timezone, report.TemplateVersion, report.Content, report.GeneratedAt, report.Status, report.CreatedAt)
	if err != nil {
		return err
	}

	// Insert report_incidents
	for _, incidentID := range incidentIDs {
		reason := inclusionReasons[incidentID]
		if reason == "" {
			reason = models.InclusionReasonOngoing
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO report_incidents (report_id, incident_id, inclusion_reason)
			VALUES ($1, $2, $3)
		`, report.ID, incidentID, reason)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// createDeliveryAttempts creates pending delivery attempts for each configured channel
func (r *Reporter) createDeliveryAttempts(ctx context.Context, report models.Report) error {
	channels := []struct {
		name           string
		destinationKey string
		enabled        bool
	}{
		{"discord", r.config.DiscordWebhookURL, r.config.DiscordWebhookURL != ""},
		{"telegram", r.config.TelegramChatID, r.config.TelegramBotToken != "" && r.config.TelegramChatID != ""},
		{"email", r.config.EmailTo, r.config.EmailSMTPHost != "" && r.config.EmailUsername != "" && r.config.EmailPassword != ""},
	}

	for _, ch := range channels {
		if !ch.enabled {
			continue
		}

		attempt := models.DeliveryAttempt{
			ID:             uuid.NewString(),
			ReportID:       report.ID,
			Channel:        ch.name,
			DestinationKey: ch.destinationKey,
			Status:         models.DeliveryStatusPending,
			Attempts:       0,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}

		_, err := r.db.ExecContext(ctx, `
			INSERT INTO delivery_attempts (id, report_id, channel, destination_key, status, attempts, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, attempt.ID, attempt.ReportID, attempt.Channel, attempt.DestinationKey,
			attempt.Status, attempt.Attempts, attempt.CreatedAt, attempt.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to create delivery attempt for %s: %w", ch.name, err)
		}
	}

	return nil
}

// deliverReport delivers the report via all configured channels
func (r *Reporter) deliverReport(report models.Report) {
	ctx := context.Background()

	channels := []struct {
		name           string
		sendFunc       func(context.Context, models.Report) error
		destinationKey string
	}{
		{"discord", r.sendToDiscord, r.config.DiscordWebhookURL},
		{"telegram", r.sendToTelegram, r.config.TelegramChatID},
		{"email", r.sendToEmail, r.config.EmailTo},
	}

	var wg sync.WaitGroup
	for _, ch := range channels {
		if ch.destinationKey == "" {
			continue
		}
		wg.Add(1)
		go func(channel string, sendFn func(context.Context, models.Report) error, destKey string) {
			defer wg.Done()
			r.deliverChannel(ctx, report, channel, sendFn, destKey)
		}(ch.name, ch.sendFunc, ch.destinationKey)
	}

	wg.Wait()

	// Update report status based on delivery attempts
	r.updateReportDeliveryStatus(ctx, report.ID)
}

// deliverChannel delivers a report via a single channel with retry logic
func (r *Reporter) deliverChannel(ctx context.Context, report models.Report, channel string, sendFn func(context.Context, models.Report) error, destinationKey string) {
	maxAttempts := 3
	baseDelay := 10 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Claim the delivery attempt
		attemptID, err := r.claimDeliveryAttempt(ctx, report.ID, channel, destinationKey)
		if err != nil {
			log.Error().Err(err).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to claim delivery attempt")
			return
		}

		// Update status to sending
		if updateErr := r.updateDeliveryAttemptStatus(ctx, attemptID, models.DeliveryStatusSending, attempt, "", nil); updateErr != nil {
			log.Error().Err(updateErr).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to update delivery attempt status")
			return
		}

		// Send the report
		err = sendFn(ctx, report)

		if err == nil {
			// Success
			if updateErr := r.updateDeliveryAttemptStatus(ctx, attemptID, models.DeliveryStatusSent, attempt, "", nil); updateErr != nil {
				log.Error().Err(updateErr).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to update delivery attempt status")
			}
			log.Info().Str("channel", channel).Str("report_id", report.ID).Msg("Report delivered successfully")
			return
		}

		// Check if error is retryable
		if r.isRetryableError(err) && attempt < maxAttempts {
			// Schedule retry
			delay := baseDelay * time.Duration(attempt)
			nextAttempt := time.Now().Add(delay)
			if updateErr := r.updateDeliveryAttemptStatus(ctx, attemptID, models.DeliveryStatusRetryable, attempt, "", &nextAttempt); updateErr != nil {
				log.Error().Err(updateErr).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to update delivery attempt status")
			}
			log.Warn().Err(err).Str("channel", channel).Str("report_id", report.ID).Int("attempt", attempt).Msg("Delivery failed, will retry")
			time.Sleep(delay)
			continue
		}

		// Terminal failure
		safeErrorCode := r.classifyError(err)
		if updateErr := r.updateDeliveryAttemptStatus(ctx, attemptID, models.DeliveryStatusFailedTerminal, attempt, safeErrorCode, nil); updateErr != nil {
			log.Error().Err(updateErr).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to update delivery attempt status")
		}
		log.Error().Err(err).Str("channel", channel).Str("report_id", report.ID).Str("safe_error_code", safeErrorCode).Msg("Delivery failed terminally")
		return
	}
}

// claimDeliveryAttempt claims a pending delivery attempt for processing
func (r *Reporter) claimDeliveryAttempt(ctx context.Context, reportID, channel, destinationKey string) (string, error) {
	query := `
		UPDATE delivery_attempts
		SET status = 'sending',
		    attempts = attempts + 1,
		    updated_at = now(),
		    lease_expires_at = now() + interval '5 minutes'
		WHERE report_id = $1 AND channel = $2 AND destination_key = $3
		  AND status IN ('pending', 'retryable')
		  AND (lease_expires_at IS NULL OR lease_expires_at < now())
		RETURNING id
	`

	var attemptID string
	err := r.db.QueryRowContext(ctx, query, reportID, channel, destinationKey).Scan(&attemptID)
	if err == sql.ErrNoRows {
		// Check if already sent
		var status string
		err = r.db.QueryRowContext(ctx, `
			SELECT status FROM delivery_attempts WHERE report_id = $1 AND channel = $2 AND destination_key = $3
		`, reportID, channel, destinationKey).Scan(&status)
		if err == nil && status == "sent" {
			return "", fmt.Errorf("delivery already completed")
		}
		return "", fmt.Errorf("delivery attempt not available: %w", err)
	}
	return attemptID, err
}

// updateDeliveryAttemptStatus updates the status of a delivery attempt
func (r *Reporter) updateDeliveryAttemptStatus(ctx context.Context, attemptID, status string, attempts int, safeErrorCode string, nextAttemptAt *time.Time) error {
	query := `
		UPDATE delivery_attempts
		SET status = $1,
		    attempts = $2,
		    safe_error_code = $3,
		    next_attempt_at = $4,
		    updated_at = now()
		WHERE id = $5
	`
	_, err := r.db.ExecContext(ctx, query, status, attempts, safeErrorCode, nextAttemptAt, attemptID)
	return err
}

// updateReportDeliveryStatus updates the report status based on delivery attempts
func (r *Reporter) updateReportDeliveryStatus(ctx context.Context, reportID string) {
	query := `
		SELECT status FROM delivery_attempts WHERE report_id = $1
	`
	rows, err := r.db.QueryContext(ctx, query, reportID)
	if err != nil {
		log.Error().Err(err).Str("report_id", reportID).Msg("Failed to query delivery attempts")
		return
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	allSent := true
	anySent := false
	anyPending := false
	var status string

	for rows.Next() {
		var scanStatus string
		if scanErr := rows.Scan(&scanStatus); scanErr != nil {
			continue
		}
		status = scanStatus
		switch status {
		case "sent":
			anySent = true
		case "pending", "sending", "retryable":
			allSent = false
			anyPending = true
		case "failed_terminal":
			allSent = false
		}
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		log.Error().Err(rowsErr).Str("report_id", reportID).Msg("Error iterating delivery attempts")
		return
	}

	var newStatus string
	if allSent && anySent {
		newStatus = models.ReportStatusDelivered
	} else if anySent && (anyPending || !allSent) {
		newStatus = models.ReportStatusPartiallyDelivered
	} else if !anySent {
		newStatus = models.ReportStatusDeliveryFailed
	} else {
		newStatus = models.ReportStatusGenerated
	}

	_, err = r.db.ExecContext(ctx, `
		UPDATE reports SET status = $1 WHERE id = $2
	`, newStatus, reportID)
	if err != nil {
		log.Error().Err(err).Str("report_id", reportID).Msg("Failed to update report status")
	}
}

// updateReportRequestStatus updates the report request status
func (r *Reporter) updateReportRequestStatus(ctx context.Context, requestID, status string, attempts int, safeErrorCode string, nextAttemptAt *time.Time) error {
	query := `
		UPDATE report_requests
		SET status = $1,
		    attempts = $2,
		    safe_error_code = $3,
		    next_attempt_at = $4,
		    updated_at = now(),
		    lease_expires_at = NULL
		WHERE id = $5
	`
	_, err := r.db.ExecContext(ctx, query, status, attempts, safeErrorCode, nextAttemptAt, requestID)
	return err
}

// isRetryableError determines if an error is retryable
func (r *Reporter) isRetryableError(err error) bool {
	errStr := strings.ToLower(err.Error())
	// Network errors, timeouts, 429, 5xx are retryable
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "network") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "429")
}

// classifyError classifies an error into a safe error code
func (r *Reporter) classifyError(err error) string {
	errStr := strings.ToLower(err.Error())
	switch {
	case strings.Contains(errStr, "timeout"):
		return "timeout"
	case strings.Contains(errStr, "connection refused"):
		return "connection_refused"
	case strings.Contains(errStr, "429"):
		return "rate_limited"
	case strings.Contains(errStr, "502") || strings.Contains(errStr, "503") || strings.Contains(errStr, "504"):
		return "upstream_error"
	case strings.Contains(errStr, "401") || strings.Contains(errStr, "403"):
		return "auth_failed"
	case strings.Contains(errStr, "certificate") || strings.Contains(errStr, "tls"):
		return "cert_error"
	default:
		return "unknown_error"
	}
}

// getRecentIncidents retrieves incidents from the last duration (legacy)
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
		var resolvedAt sql.NullTime

		if err := rows.Scan(
			&inc.ID, &inc.Title, &inc.Status, &inc.StartedAt, &resolvedAt,
			&inc.Severity, &affectedServicesJSON,
		); err != nil {
			return nil, err
		}
		if resolvedAt.Valid {
			inc.ResolvedAt = resolvedAt.Time
		}
		if err := json.Unmarshal(affectedServicesJSON, &inc.AffectedServices); err != nil {
			return nil, err
		}
		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
}

// getEvidenceForIncident retrieves evidence findings for a specific incident (legacy)
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

// generateDailyReport creates a formatted daily digest report (legacy)
func (r *Reporter) generateDailyReport(incidents []models.Incident) string {
	renderStart := time.Now()
	defer func() {
		r.metrics.TemplateRenderDuration.Observe(time.Since(renderStart).Seconds())
	}()

	// Don't fabricate a health percentage without a named metric and denominator
	// Per normative contract: "Never generate an availability percentage without a named metric and denominator"

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
			"TotalIncidents": len(incidents),
			"NeedsAttention": needsAttention,
			"Recovered":      recovered,
			"Incidents":      incidents,
			"Evidence":       incidentEvidence,
			"Timestamp":      time.Now().Format("2006-01-02 15:04:05"),
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
						"fact":         "✅",
						"likely_cause": "🎯",
						"hypothesis":   "💭",
						"unknown":      "❓",
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

	// Removed fabricated "No critical outage occurred" claim
	return sb.String()
}

// sendReport sends the report via configured channels (legacy)
func (r *Reporter) sendReport(report models.Report) error {
	var wg sync.WaitGroup
	var errMutex sync.Mutex
	var firstError error
	ctx := context.Background()

	// Discord
	if r.config.DiscordWebhookURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.sendToDiscord(ctx, report); err != nil {
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
			if err := r.sendToTelegram(ctx, report); err != nil {
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
			if err := r.sendToEmail(ctx, report); err != nil {
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
func (r *Reporter) sendToDiscord(ctx context.Context, report models.Report) error {
	if r.config.DiscordWebhookURL == "" {
		return nil
	}

	payload := map[string]interface{}{
		"content":  report.Content,
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
func (r *Reporter) sendToTelegram(ctx context.Context, report models.Report) error {
	if r.config.TelegramBotToken == "" || r.config.TelegramChatID == "" {
		return nil
	}

	telegramBase := r.config.TelegramAPIBaseURL
	if telegramBase == "" {
		telegramBase = "https://api.telegram.org"
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramBase, r.config.TelegramBotToken)
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
func (r *Reporter) sendToEmail(_ context.Context, report models.Report) error {
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
	emailCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()

	if err := client.DialAndSendWithContext(emailCtx, msg); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Info().Str("smtp_host", r.config.EmailSMTPHost).Str("to", r.config.EmailTo).Msg("Email sent successfully")
	return nil
}

// saveReport saves the report to database (legacy)
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
