package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
	"github.com/wneessen/go-mail"

	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/models"
)

const (
	// ReportPolicyVersion is the version of the report policy for idempotency
	ReportPolicyVersion = 1

	// DefaultTimezone is the default IANA timezone for daily reports
	DefaultTimezone = "Asia/Jakarta"
)

// ReporterConfig holds the configuration for the reporter
type ReporterConfig struct {
	DatabaseURL        string
	Port               string
	DiscordWebhookURL  string
	TelegramBotToken   string
	TelegramChatID     string
	TelegramAPIBaseURL string
	EmailSMTPHost      string
	EmailSMTPPort      int
	EmailUsername      string
	EmailPassword      string
	EmailFrom          string
	EmailTo            string
	// HighSeverityInvestigationTimeout is how long the reporter waits for an
	// incident's investigation to reach a terminal state before generating a
	// high-severity report. This prevents reports from being generated before
	// investigation evidence is available. Zero disables the wait.
	HighSeverityInvestigationTimeout time.Duration
	// DiscordRateLimitDelay is the minimum delay between Discord webhook chunk sends.
	// Set to 0 to use the default (1 second). Configure this to control pacing
	// and avoid hitting Discord's ~5 messages per 5 seconds rate limit.
	DiscordRateLimitDelay time.Duration
	// PrometheusURL is the base URL of the Prometheus server used to query
	// availability metrics.
	PrometheusURL string
	// AvailabilityMetric is the Prometheus metric name used to compute
	// availability (e.g. "up").
	AvailabilityMetric string
	// ReportTemplate selects the report rendering template: "v1" (legacy
	// plain text) or "v2" (Uptime Kuma-style card layout). Defaults to "v1".
	ReportTemplate string
}

// Reporter holds the database connection and reporting logic
type Reporter struct {
	db             *sql.DB
	config         ReporterConfig
	metrics        *metrics.Metrics
	httpClient     *http.Client
	ctx            context.Context
	cancelFunc     context.CancelFunc
	startTime      time.Time
	circuitBreakers map[string]*CircuitBreaker
	cbMu           sync.Mutex
}

// NewReporter creates a new reporter instance
func NewReporter(db *sql.DB, cfg ReporterConfig, m *metrics.Metrics) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())
	cbMap := make(map[string]*CircuitBreaker)
	// Circuit breaker per delivery channel: trip after 3 consecutive failures,
	// stay open for 30 seconds before allowing a single trial call.
	for _, channel := range []string{"discord", "telegram", "email", "prometheus"} {
		cbMap[channel] = NewCircuitBreaker(3, 30*time.Second)
	}
	return &Reporter{
		db:             db,
		config:         cfg,
		metrics:        m,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		ctx:            ctx,
		cancelFunc:     cancel,
		startTime:      time.Now(),
		circuitBreakers: cbMap,
	}
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

	// Load evidence for all selected incidents.
	// Note: the scheduler now dispatches high-severity report requests only after
	// investigations reach a terminal state (checkHighSeverityFollowUp), so the
	// reporter does not block waiting for investigation completion here.
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
		TemplateVersion: fmt.Sprintf("pamawas-report-%s", r.config.ReportTemplate),
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
		r.deliverReport(ctx, report)
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

	const query = `SELECT e.id, e.incident_id, e.type, e.content, e.source, e.confidence,
       e.supports_evidence, e.contradicts_evidence, e.ordinal
FROM evidence e
JOIN investigation_runs ir ON e.run_id = ir.id
WHERE e.incident_id = ANY($1)
  AND ir.status = 'completed'
ORDER BY e.incident_id, ir.completed_at DESC, e.ordinal`

	rows, err := r.db.QueryContext(ctx, query, pq.Array(incidentIDs))
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
	needsAttention, resolvedCount := countIncidentStatuses(incidents)

	// Availability - only if we have a real metric
	availability := r.getAvailabilityMetric(ctx, periodStart, periodEnd)
	if availability >= 0 {
		fmt.Fprintf(&sb, "Infrastructure was %.2f%% available during this period.\n", availability)
	} else {
		sb.WriteString("Availability: unavailable (no named availability metric configured)\n")
	}

	fmt.Fprintf(&sb, "%d incidents occurred. %d need attention. %d resolved.\n\n",
		totalIncidents, needsAttention, resolvedCount)

	// Split incidents into unresolved (needs attention) and solved sections so
	// the daily report clearly separates what still needs work from what is solved.
	var unresolved, solved []models.Incident
	for _, inc := range incidents {
		switch inc.Status {
		case "open", "investigating":
			unresolved = append(unresolved, inc)
		case "resolved":
			solved = append(solved, inc)
		}
	}

	// Incident details, divided into unresolved and solved sections
	if r.config.ReportTemplate == "v2" {
		writeIncidentDetailsV2(&sb, unresolved, solved, incidentEvidence, inclusionReasons, timezone)
	} else {
		writeIncidentDetails(&sb, unresolved, solved, incidentEvidence, inclusionReasons)
	}

	fmt.Fprintf(&sb, "---\nTemplate: pamawas-report-%s | Generated: %s\n", r.config.ReportTemplate, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

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
// its evidence findings, divided into unresolved (needs attention) and solved
// (resolved) sections.
func writeIncidentDetails(sb *strings.Builder, unresolved, resolved []models.Incident, incidentEvidence map[string][]models.Evidence, inclusionReasons map[string]string) {
	if len(unresolved) == 0 && len(resolved) == 0 {
		sb.WriteString("No incidents detected.\n\n")
		return
	}

	sb.WriteString("## Unresolved (needs attention)\n")
	writeIncidentDetailsList(sb, unresolved, incidentEvidence, inclusionReasons)

	if len(resolved) > 0 {
		sb.WriteString("## Solved (resolved during period)\n")
		writeIncidentDetailsList(sb, resolved, incidentEvidence, inclusionReasons)
	}

	sb.WriteString("\n")
}

// writeIncidentDetailsList renders a flat list of incidents (v1 template).
func writeIncidentDetailsList(sb *strings.Builder, incidents []models.Incident, incidentEvidence map[string][]models.Evidence, inclusionReasons map[string]string) {
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

// statusColor returns an emoji status badge for the given incident status.
func statusColor(status string) string {
	switch status {
	case "open", "investigating":
		return "🟡"
	case "resolved":
		return "🟢"
	case "suppressed":
		return "⚫"
	}
	return "⚪"
}

// severityColor returns an emoji severity badge for the given severity level.
func severityColor(severity string) string {
	switch severity {
	case "critical":
		return "🔴"
	case "high":
		return "🟠"
	case "warning":
		return "🟡"
	case "low":
		return "🟢"
	case "info":
		return "🔵"
	}
	return "⚪"
}

// loadLocation returns a time.Location for the given timezone string, falling back to UTC.
func loadLocation(timezone string) *time.Location {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Warn().Str("timezone", timezone).Msg("failed to load timezone, falling back to UTC")
		return time.UTC
	}
	return loc
}

// writeIncidentDetailsV2 renders incident details using Uptime Kuma-style card
// layout: each incident is presented as a bordered card with status chip,
// key metrics, and formatted investigation findings. Incidents are split into
// unresolved (needs attention) and solved (resolved) sections.
func writeIncidentDetailsV2(sb *strings.Builder, unresolved, resolved []models.Incident, incidentEvidence map[string][]models.Evidence, inclusionReasons map[string]string, timezone string) {
	if len(unresolved) == 0 && len(resolved) == 0 {
		sb.WriteString("No incidents detected.\n\n")
		return
	}

	sb.WriteString("## Unresolved (needs attention)\n")
	writeIncidentCardListV2(sb, unresolved, incidentEvidence, inclusionReasons, timezone)

	if len(resolved) > 0 {
		sb.WriteString("## Solved (resolved during period)\n")
		writeIncidentCardListV2(sb, resolved, incidentEvidence, inclusionReasons, timezone)
	}

	sb.WriteString("\n")
}

// writeIncidentCardListV2 renders a list of incidents as cards (v2 template).
func writeIncidentCardListV2(sb *strings.Builder, incidents []models.Incident, incidentEvidence map[string][]models.Evidence, inclusionReasons map[string]string, timezone string) {
	for _, inc := range incidents {
		writeIncidentCardV2(sb, inc, incidentEvidence[inc.ID], inclusionReasons[inc.ID], timezone)
		sb.WriteString("\n")
	}
}

// writeIncidentCardV2 renders a single incident as a bordered Uptime Kuma-style card.
func writeIncidentCardV2(sb *strings.Builder, inc models.Incident, evList []models.Evidence, inclusionReason string, timezone string) {
	statusBadge := statusColor(inc.Status)
	sevBadge := severityColor(inc.Severity)

	reasonLabel := inclusionReasonLabel(inclusionReason)

	startedLocal := inc.StartedAt.In(loadLocation(timezone))
	startedStr := startedLocal.Format("2006-01-02 15:04:05")

	var resolvedStr string
	if !inc.ResolvedAt.IsZero() {
		resolvedLocal := inc.ResolvedAt.In(loadLocation(timezone))
		resolvedStr = resolvedLocal.Format("2006-01-02 15:04:05")
	}

	services := strings.Join(inc.AffectedServices, ", ")

	// Full incident id (opaque) plus a short form for quick scanning.
	idShort := inc.ID
	if len(idShort) > 8 {
		idShort = idShort[:8]
	}

	// Human-readable context lines.
	envLine := inc.Environment
	if envLine == "" {
		envLine = "unknown"
	}
	corrLine := inc.CorrelationPolicy
	if inc.CorrelationVersion > 0 {
		corrLine = fmt.Sprintf("%s (v%d)", corrLine, inc.CorrelationVersion)
	}
	if corrLine == "" {
		corrLine = "none"
	}

	// Card border width based on content (min 60 chars)
	borderWidth := 60
	if len(inc.Title)+len(statusBadge)+10 > borderWidth {
		borderWidth = len(inc.Title) + len(statusBadge) + 10
	}
	if len(statusBadge)+len(sevBadge)+len("Severity: ")+len(inc.Severity)+len(" | Services: ")+len(services) > borderWidth {
		borderWidth = len(statusBadge) + len(sevBadge) + len("Severity: ") + len(inc.Severity) + len(" | Services: ") + len(services)
	}
	if len("🌍 Environment: ")+len(envLine) > borderWidth {
		borderWidth = len("🌍 Environment: ") + len(envLine)
	}
	if len("🔗 Correlation: ")+len(corrLine) > borderWidth {
		borderWidth = len("🔗 Correlation: ") + len(corrLine)
	}
	if len("🔖 ID: ")+len(inc.ID) > borderWidth {
		borderWidth = len("🔖 ID: ") + len(inc.ID)
	}

	borderTop := "┌─" + strings.Repeat("─", borderWidth) + "─┐\n"
	borderBottom := "└─" + strings.Repeat("─", borderWidth) + "─┘\n"

	// Pad a line to border width, guarding against content wider than the border
	padLine := func(content string) string {
		padding := borderWidth - len(content)
		if padding < 0 {
			padding = 0
		}
		return "│ " + content + strings.Repeat(" ", padding) + " │\n"
	}

	sb.WriteString(borderTop)
	sb.WriteString(padLine(fmt.Sprintf("%s %s (%s)%s", statusBadge, inc.Title, inc.Status, reasonLabel)))
	sb.WriteString(padLine(fmt.Sprintf("📛 Name: %s", inc.Title)))
	sb.WriteString(padLine(fmt.Sprintf("%s Severity: %s   |   Services: %s", sevBadge, inc.Severity, services)))
	sb.WriteString(padLine(fmt.Sprintf("🌍 Environment: %s", envLine)))
	sb.WriteString(padLine(fmt.Sprintf("🔗 Correlation: %s", corrLine)))
	sb.WriteString(padLine(fmt.Sprintf("📅 Started: %s", startedStr)))
	if resolvedStr != "" {
		sb.WriteString(padLine(fmt.Sprintf("✅ Resolved: %s", resolvedStr)))
	}
	sb.WriteString(padLine(fmt.Sprintf("🔖 ID: %s", inc.ID)))
	sb.WriteString(padLine(fmt.Sprintf("   short: %s", idShort)))

	if len(evList) == 0 {
		sb.WriteString(padLine("🔍 Investigation Findings: investigation unavailable"))
	} else {
		sb.WriteString(padLine("🔍 Investigation Findings:"))
		for _, ev := range evList {
			sb.WriteString(padLine(fmt.Sprintf("   %s %s (%.0f%%) %s",
				evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100, ev.Content)))
		}
	}

	sb.WriteString(borderBottom)
	sb.WriteString("\n")
}

func parseAffectedServices(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var arr pq.StringArray
	if err := arr.Scan(raw); err == nil {
		return []string(arr), nil
	}
	var result []string
	if err := json.Unmarshal(raw, &result); err == nil {
		return result, nil
	}
	return nil, fmt.Errorf("invalid affected_services format: %s", string(raw))
}

// getIncidentsByIDs loads incident details by IDs
func (r *Reporter) getIncidentsByIDs(ctx context.Context, incidentIDs []string) ([]models.Incident, error) {
	if len(incidentIDs) == 0 {
		return []models.Incident{}, nil
	}

	const query = `SELECT id, title, status, started_at, resolved_at, severity, affected_services
FROM incidents
WHERE id = ANY($1)
ORDER BY started_at DESC`

	rows, err := r.db.QueryContext(ctx, query, pq.Array(incidentIDs))
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
		var rawServices []byte
		var resolvedAt sql.NullTime

		if err := rows.Scan(
			&inc.ID, &inc.Title, &inc.Status, &inc.StartedAt, &resolvedAt,
			&inc.Severity, &rawServices,
		); err != nil {
			return nil, err
		}
		if resolvedAt.Valid {
			inc.ResolvedAt = resolvedAt.Time
		}
		services, parseErr := parseAffectedServices(rawServices)
		if parseErr != nil {
			return nil, parseErr
		}
		inc.AffectedServices = services
		incidents = append(incidents, inc)
	}

	return incidents, rows.Err()
}

// getAvailabilityMetric queries Prometheus for the configured availability
// metric (e.g. "up") and returns the average availability percentage over the
// given period. Returns -1 if no metric is configured or the query fails.
func (r *Reporter) getAvailabilityMetric(ctx context.Context, periodStart, periodEnd time.Time) float64 {
	if r.config.PrometheusURL == "" || r.config.AvailabilityMetric == "" {
		return -1
	}

	// Query Prometheus for the average value of the availability metric over
	// the reporting period. Using avg_over_time gives a percentage-like value
	// for up/down metrics (1 = up, 0 = down).
	query := url.Values{}
	query.Set("query", fmt.Sprintf("avg_over_time(%s[%ds])",
		r.config.AvailabilityMetric, int(periodEnd.Sub(periodStart).Seconds())))
	query.Set("time", periodEnd.Format(time.RFC3339))

	apiURL := r.config.PrometheusURL + "/api/v1/query?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create Prometheus availability query")
		return -1
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		log.Error().Err(err).Str("url", apiURL).Msg("Prometheus availability query failed")
		return -1
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close Prometheus response body")
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Error().Int("status", resp.StatusCode).Str("url", apiURL).Msg("Prometheus availability query returned non-2xx")
		return -1
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read Prometheus availability response")
		return -1
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Value []interface{} `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if unmarshalErr := json.Unmarshal(body, &result); unmarshalErr != nil {
		log.Error().Err(unmarshalErr).Msg("Failed to unmarshal Prometheus availability response")
		return -1
	}

	if result.Status != "success" || len(result.Data.Result) == 0 {
		log.Warn().Str("status", result.Status).Int("result_count", len(result.Data.Result)).
			Msg("Prometheus availability query returned no results")
		return -1
	}

	// The value array is [timestamp, value_string]
	val := result.Data.Result[0].Value
	if len(val) < 2 {
		return -1
	}
	valStr, ok := val[1].(string)
	if !ok {
		return -1
	}

	availability, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		log.Error().Err(err).Str("value", valStr).Msg("Failed to parse Prometheus availability value")
		return -1
	}

	// avg_over_time of "up" returns a fraction (0-1). Convert to percentage.
	if availability > 0 && availability <= 1 {
		availability *= 100
	}

	return availability
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
func (r *Reporter) deliverReport(ctx context.Context, report models.Report) {
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

	cb := r.circuitBreakerFor(channel)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Circuit breaker: skip if channel is open
		if !cb.Allow() {
			log.Warn().Str("channel", channel).Str("report_id", report.ID).
				Msg("Circuit breaker open, skipping delivery attempt")
			r.metrics.DeliveryTotal.WithLabelValues(channel, "circuit_open").Inc()
			return
		}

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
			cb.OnSuccess()
			r.metrics.DeliveryTotal.WithLabelValues(channel, "sent").Inc()
			if updateErr := r.updateDeliveryAttemptStatus(ctx, attemptID, models.DeliveryStatusSent, attempt, "", nil); updateErr != nil {
				log.Error().Err(updateErr).Str("channel", channel).Str("report_id", report.ID).Msg("Failed to update delivery attempt status")
			}
			log.Info().Str("channel", channel).Str("report_id", report.ID).Msg("Report delivered successfully")
			return
		}

		// Check if error is retryable
		if r.isRetryableError(err) && attempt < maxAttempts {
			cb.OnFailure()
			r.metrics.DeliveryTotal.WithLabelValues(channel, "retryable").Inc()
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
		cb.OnFailure()
		r.metrics.DeliveryTotal.WithLabelValues(channel, "failed_terminal").Inc()
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
	case strings.Contains(errStr, "400"):
		return "bad_request"
	case strings.Contains(errStr, "certificate") || strings.Contains(errStr, "tls"):
		return "cert_error"
	default:
		return "unknown_error"
	}
}

// sendToDiscord sends the report to Discord via webhook
// Discord's incoming webhook limits the "content" field to 2000 characters.
// We chunk the content into 2000-char segments and send each as a separate
// webhook call so the full report is delivered even if it exceeds the limit.
// Rate limiting: Discord allows ~5 messages per 5 seconds per webhook.
// We pace sends with a base delay and honor 429 retry_after headers.
func (r *Reporter) sendToDiscord(ctx context.Context, report models.Report) error {
	if r.config.DiscordWebhookURL == "" {
		return nil
	}

	if strings.TrimSpace(report.Content) == "" {
		log.Warn().Str("report_id", report.ID).Msg("Discord: report content is empty, nothing to send")
		return nil
	}

	const maxDiscordContent = 2000
	chunks := chunkString(report.Content, maxDiscordContent)

	var firstErr error
	// Discord rate limit: ~5 messages per 5 seconds = 1 message/second sustained
	// Use a pacing delay between chunks to avoid triggering rate limits
	basePacingDelay := r.config.DiscordRateLimitDelay
	if basePacingDelay <= 0 {
		basePacingDelay = 1 * time.Second
	}

	for i, chunk := range chunks {
		payload := map[string]interface{}{
			"content":  chunk,
			"username": "Pamawas Reporter",
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Error().Err(err).Int("chunk", i).Msg("Failed to marshal Discord payload")
			continue
		}

		// Per-chunk retry logic with 429 handling
		chunkErr := r.sendDiscordChunkWithRetry(ctx, i, payloadBytes)
		if chunkErr != nil {
			if firstErr == nil {
				firstErr = chunkErr
			}
			// Continue with remaining chunks even if one fails
			// The error is logged inside sendDiscordChunkWithRetry
			continue
		}

		// Pace between chunks to respect Discord's rate limit
		// Skip delay after the last chunk
		if i < len(chunks)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(basePacingDelay):
			}
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return nil
}

// sendDiscordChunkWithRetry sends a single chunk with retry logic for rate limits (429)
// Returns error only if all retries exhausted or non-retryable error occurs
func (r *Reporter) sendDiscordChunkWithRetry(ctx context.Context, chunkIndex int, payloadBytes []byte) error {
	const maxRetries = 3
	const baseRetryDelay = 2 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.config.DiscordWebhookURL, strings.NewReader(string(payloadBytes)))
		if err != nil {
			log.Error().Err(err).Int("chunk", chunkIndex).Msg("Failed to create Discord request")
			return err // Non-retryable
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.httpClient.Do(req)
		if err != nil {
			log.Error().Err(err).Int("chunk", chunkIndex).Int("attempt", attempt+1).Msg("Failed to send Discord webhook request")
			if attempt == maxRetries {
				return err
			}
			// Exponential backoff for network errors
			delay := baseRetryDelay * time.Duration(attempt+1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		// Read body to allow connection reuse, but we only need headers for 429
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Error().Err(readErr).Int("chunk", chunkIndex).Int("attempt", attempt+1).Msg("Failed to read Discord response body")
		}
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Error().Err(closeErr).Int("chunk", chunkIndex).Int("attempt", attempt+1).Msg("Failed to close Discord response body")
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Info().Int("chunk", chunkIndex).Int("chunk_len", len(payloadBytes)).Msg("Discord webhook sent successfully")
			return nil
		}

		// Handle 429 rate limit with retry_after header
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := r.parseRetryAfter(resp.Header.Get("Retry-After"), respBody)
			log.Warn().
				Int("chunk", chunkIndex).
				Int("attempt", attempt+1).
				Dur("retry_after", retryAfter).
				Msg("Discord rate limited (429), will retry")

			if attempt == maxRetries {
				rateLimitErr := fmt.Errorf("discord webhook rate limited after %d retries (chunk %d)", maxRetries, chunkIndex)
				log.Error().Err(rateLimitErr).Int("chunk", chunkIndex).Msg("Discord webhook error")
				return rateLimitErr
			}

			// Wait for retry_after (or default backoff) before retrying
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryAfter):
			}
			continue
		}

		// Non-429 error: log and return (don't retry other error codes here,
		// the caller's firstErr mechanism will capture it)
		err = fmt.Errorf("discord webhook returned status %d (chunk %d)", resp.StatusCode, chunkIndex)
		log.Error().Err(err).Int("chunk", chunkIndex).Int("attempt", attempt+1).Msg("Discord webhook error")
		return err
	}

	return fmt.Errorf("discord chunk %d: max retries exhausted", chunkIndex)
}

// parseRetryAfter extracts retry-after from header or response body
// Discord sends retry_after in seconds in the header or as milliseconds in JSON body
func (r *Reporter) parseRetryAfter(header string, body []byte) time.Duration {
	// Try header first (seconds)
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}

	// Try JSON body (milliseconds) - Discord format: {"retry_after": 1234.5, "global": true}
	var retryAfterJSON struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(body, &retryAfterJSON); err == nil && retryAfterJSON.RetryAfter > 0 {
		return time.Duration(retryAfterJSON.RetryAfter) * time.Millisecond
	}

	// Default fallback
	return 2 * time.Second
}

// chunkString splits s into slices of at most maxLen characters, preferring
// to break at newlines when possible to avoid cutting words mid-line.
func chunkString(s string, maxLen int) []string {
	if len(s) == 0 {
		return []string{""}
	}
	var chunks []string
	for len(s) > 0 {
		if len(s) <= maxLen {
			chunks = append(chunks, s)
			return chunks
		}

		// Try to break at a newline within the last quarter of the chunk
		searchEnd := maxLen - maxLen/4
		if searchEnd < 1 {
			searchEnd = 1
		}
		searchStart := maxLen - searchEnd
		if searchStart < 0 {
			searchStart = 0
		}

		// Look for a newline in the search window
		bestCut := -1
		for i := searchStart; i < len(s) && i < searchEnd; i++ {
			if s[i] == '\n' {
				bestCut = i
				break
			}
		}

		if bestCut >= 0 {
			chunks = append(chunks, s[:bestCut+1])
			s = s[bestCut+1:]
		} else {
			// No newline found; cut at maxLen
			chunks = append(chunks, s[:maxLen])
			s = s[maxLen:]
		}
	}
	return chunks
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payloadBytes)))
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
func (r *Reporter) sendToEmail(ctx context.Context, report models.Report) error {
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
	emailCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := client.DialAndSendWithContext(emailCtx, msg); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	log.Info().Str("smtp_host", r.config.EmailSMTPHost).Str("to", r.config.EmailTo).Msg("Email sent successfully")
	return nil
}

// CircuitBreakerFor returns the circuit breaker for the given channel for observability.
func (r *Reporter) CircuitBreakerFor(channel string) *CircuitBreaker {
	return r.circuitBreakerFor(channel)
}

// circuitBreakerFor returns the circuit breaker for the given channel, creating it lazily.
func (r *Reporter) circuitBreakerFor(channel string) *CircuitBreaker {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()
	cb, ok := r.circuitBreakers[channel]
	if !ok {
		cb = NewCircuitBreaker(3, 30*time.Second)
		r.circuitBreakers[channel] = cb
	}
	return cb
}

// RecoverStalledDeliveries reclaims delivery attempts whose lease has expired
// but are still in 'sending' state, resetting them to 'pending' so they can be
// retried. Returns the number of recovered attempts.
func (r *Reporter) RecoverStalledDeliveries(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET status = 'pending',
		    attempts = attempts - 1,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE status = 'sending'
		  AND lease_expires_at IS NOT NULL
		  AND lease_expires_at < now()
	`)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		r.metrics.StalledDeliveriesTotal.Add(float64(count))
		log.Info().Int64("recovered", count).Msg("Recovered stalled delivery attempts")
	}
	return int(count), nil
}
func (r *Reporter) StartTime() time.Time {
	return r.startTime
}

// Stop stops the reporter gracefully
func (r *Reporter) Stop() {
	r.cancelFunc()
}
