package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Pamawas/pamawas-reporter/models"
)

// Discord renders reports as embeds rather than plain text.
//
// A text template cannot draw a box: Discord uses a proportional font, so the
// `┌─│└` characters the v2 template emits never line up and the card spills
// across the channel. Embeds are the only construct that produces a real
// bordered card — a colored bar down the left edge, a boxed body, and bold
// field labels.
//
// Embeds are built from the structured report data (incidents, evidence,
// inclusion reasons) rather than by parsing the rendered text, so the Discord
// presentation stays independent of the text template used for Telegram and
// email. The data is re-read for every delivery attempt, which also makes
// retried deliveries render identically to the first attempt.

// Discord API limits. Exceeding any of these rejects the whole message.
const (
	discordMaxEmbeds      = 10
	discordMaxTitle       = 256
	discordMaxDescription = 4096
	discordMaxFields      = 25
	discordMaxFieldName   = 256
	discordMaxFieldValue  = 1024
	discordMaxFooter      = 2048
)

// Discord embed colors (colored bar down the left edge).
const (
	discordColorResolved = 0x2ECC71 // green
	discordColorCritical = 0xE74C3C // red
	discordColorHigh     = 0xE67E22 // orange
	discordColorWarning  = 0xF1C40F // yellow
	discordColorInfo     = 0x3498DB // blue
	discordColorNeutral  = 0x95A5A6 // gray
)

// discordEmbedField is one bold label / value pair inside an embed.
type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// discordEmbedFooter is the small gray line at the bottom of an embed.
type discordEmbedFooter struct {
	Text string `json:"text"`
}

// discordEmbed is a single bordered card.
type discordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

// discordMessage is the webhook payload.
type discordMessage struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

// buildDiscordMessage renders a report as a summary embed followed by one
// bordered card per incident.
func (r *Reporter) buildDiscordMessage(ctx context.Context, report models.Report) (*discordMessage, error) {
	incidents, inclusionReasons, err := r.loadReportIncidents(ctx, report.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load report incidents: %w", err)
	}

	incidentIDs := make([]string, 0, len(incidents))
	for _, inc := range incidents {
		incidentIDs = append(incidentIDs, inc.ID)
	}
	evidence, err := r.loadEvidenceForIncidents(ctx, incidentIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load evidence: %w", err)
	}

	embeds := []discordEmbed{r.buildSummaryEmbed(ctx, report, incidents)}
	for _, inc := range incidents {
		if len(embeds) >= discordMaxEmbeds {
			log.Warn().
				Str("report_id", report.ID).
				Int("omitted", len(incidents)-len(embeds)+1).
				Msg("Discord embed limit reached; remaining incidents omitted from the card view")
			break
		}
		embeds = append(embeds, buildIncidentEmbed(inc, evidence[inc.ID], inclusionReasons[inc.ID], report.Timezone))
	}

	return &discordMessage{Username: "Pamawas Reporter", Embeds: embeds}, nil
}

// buildSummaryEmbed renders the report headline, availability, and counts.
func (r *Reporter) buildSummaryEmbed(ctx context.Context, report models.Report, incidents []models.Incident) discordEmbed {
	needsAttention, resolvedCount := countIncidentStatuses(incidents)

	title := "📅 Daily Infrastructure Report"
	if report.ReportType != "daily" {
		title = "🚨 High Severity Immediate Report"
	}

	// Availability is only claimed when a real metric backs it; otherwise the
	// report states it is unavailable rather than inventing a percentage.
	availability := r.getAvailabilityMetric(ctx, report.PeriodStart, report.PeriodEnd)
	availabilityLine := "**Availability:** unavailable (no availability metric configured)"
	if availability >= 0 {
		availabilityLine = fmt.Sprintf("**Availability:** %.2f%%", availability)
	}

	periodStart := report.PeriodStart.In(loadLocation(report.Timezone)).Format("2006-01-02 15:04 MST")
	periodEnd := report.PeriodEnd.In(loadLocation(report.Timezone)).Format("2006-01-02 15:04 MST")

	description := strings.Join([]string{
		availabilityLine,
		fmt.Sprintf("**Incidents:** %d total · **%d** need attention · **%d** resolved",
			len(incidents), needsAttention, resolvedCount),
		"",
		fmt.Sprintf("🕒 %s → %s", periodStart, periodEnd),
	}, "\n")

	// Red when anything needs attention, green when everything recovered.
	color := discordColorResolved
	if needsAttention > 0 {
		color = discordColorCritical
	}

	return discordEmbed{
		Title:       truncateRunes(title, discordMaxTitle),
		Description: truncateRunes(description, discordMaxDescription),
		Color:       color,
		Footer: &discordEmbedFooter{
			Text: truncateRunes(fmt.Sprintf("Template: %s · Generated: %s",
				report.TemplateVersion,
				report.GeneratedAt.UTC().Format("2006-01-02 15:04:05 UTC")), discordMaxFooter),
		},
		Timestamp: report.GeneratedAt.UTC().Format(time.RFC3339),
	}
}

// buildIncidentEmbed renders one incident as a bordered card: colored left bar,
// status in the title, bold metadata fields, and the investigation findings.
func buildIncidentEmbed(inc models.Incident, evList []models.Evidence, inclusionReason, timezone string) discordEmbed {
	loc := loadLocation(timezone)

	title := fmt.Sprintf("🚨 %s", inc.Title)
	if inc.Status == "resolved" {
		title = fmt.Sprintf("✅ %s", inc.Title)
	}

	services := strings.Join(inc.AffectedServices, ", ")
	if services == "" {
		services = "unknown"
	}
	environment := inc.Environment
	if environment == "" {
		environment = "unknown"
	}

	fields := []discordEmbedField{
		{Name: "Status", Value: fmt.Sprintf("%s %s%s", statusColor(inc.Status), inc.Status, inclusionReasonLabel(inclusionReason)), Inline: true},
		{Name: "Severity", Value: fmt.Sprintf("%s %s", severityColor(inc.Severity), inc.Severity), Inline: true},
		{Name: "Environment", Value: environment, Inline: true},
		{Name: "Services", Value: services, Inline: false},
		{Name: "Started", Value: inc.StartedAt.In(loc).Format("2006-01-02 15:04:05 MST"), Inline: true},
	}
	if !inc.ResolvedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Resolved",
			Value:  inc.ResolvedAt.In(loc).Format("2006-01-02 15:04:05 MST"),
			Inline: true,
		})
	}
	fields = append(fields, discordEmbedField{Name: "Incident ID", Value: fmt.Sprintf("`%s`", inc.ID), Inline: false})

	// Findings go in the description: it allows 4096 chars, where a field
	// value stops at 1024. Findings are the substance of the report, so they
	// must not be truncated to a single field.
	description := formatFindingsForEmbed(evList)

	for i := range fields {
		fields[i].Name = truncateRunes(fields[i].Name, discordMaxFieldName)
		fields[i].Value = truncateRunes(fields[i].Value, discordMaxFieldValue)
	}
	if len(fields) > discordMaxFields {
		fields = fields[:discordMaxFields]
	}

	return discordEmbed{
		Title:       truncateRunes(title, discordMaxTitle),
		Description: description,
		Color:       incidentColor(inc),
		Fields:      fields,
	}
}

// formatFindingsForEmbed renders investigation findings as a compact list.
func formatFindingsForEmbed(evList []models.Evidence) string {
	if len(evList) == 0 {
		return "_🔍 Investigation unavailable_"
	}

	var sb strings.Builder
	sb.WriteString("**🔍 Investigation Findings**\n")
	for i, ev := range evList {
		line := fmt.Sprintf("%s **%s** (%.0f%%) %s\n",
			evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100, ev.Content)
		// Leave room for the truncation notice and the Discord 4096 limit.
		if sb.Len()+len(line) > discordMaxDescription-60 {
			fmt.Fprintf(&sb, "\n_… %d more findings omitted_", len(evList)-i)
			break
		}
		sb.WriteString(line)
	}
	return truncateRunes(sb.String(), discordMaxDescription)
}

// incidentColor picks the left-bar color: green once resolved, otherwise the
// severity's color so the worst incidents stand out first.
func incidentColor(inc models.Incident) int {
	if inc.Status == "resolved" {
		return discordColorResolved
	}
	switch strings.ToLower(inc.Severity) {
	case "critical":
		return discordColorCritical
	case "high":
		return discordColorHigh
	case "warning":
		return discordColorWarning
	case "info":
		return discordColorInfo
	default:
		return discordColorNeutral
	}
}

// loadReportIncidents returns the report's incidents plus their inclusion
// reasons, ordered unresolved-first then by start time.
func (r *Reporter) loadReportIncidents(ctx context.Context, reportID string) ([]models.Incident, map[string]string, error) {
	const query = `
		SELECT ri.incident_id, ri.inclusion_reason
		FROM report_incidents ri
		WHERE ri.report_id = $1
	`

	rows, err := r.db.QueryContext(ctx, query, reportID)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close rows")
		}
	}()

	var ids []string
	reasons := make(map[string]string)
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		reasons[id] = reason
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	incidents, err := r.getIncidentsByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}

	// Present incidents that need attention first, then most recent.
	unresolved := make([]models.Incident, 0, len(incidents))
	resolved := make([]models.Incident, 0, len(incidents))
	for _, inc := range incidents {
		if inc.Status == "resolved" {
			resolved = append(resolved, inc)
			continue
		}
		unresolved = append(unresolved, inc)
	}

	return append(unresolved, resolved...), reasons, nil
}

// truncateRunes shortens s to at most limit characters (not bytes), appending
// an ellipsis when it had to cut. Discord counts characters, so a byte-based
// cut could still exceed the limit or split a multi-byte character.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// buildAlertEmbed renders the immediate high-severity alert as a card.
//
// This is the firing notification: it carries the incident basics and says the
// investigation will follow up. The follow-up report arrives later as its own
// cards with the findings.
func buildAlertEmbed(incidentID, title, severity, environment string, startedAt time.Time) discordEmbed {
	fields := []discordEmbedField{
		{Name: "Severity", Value: fmt.Sprintf("%s %s", severityColor(severity), severity), Inline: true},
		{Name: "Environment", Value: orUnknown(environment), Inline: true},
	}
	if !startedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Started",
			Value:  startedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			Inline: true,
		})
	}
	fields = append(fields, discordEmbedField{
		Name:   "Incident ID",
		Value:  fmt.Sprintf("`%s`", incidentID),
		Inline: false,
	})

	for i := range fields {
		fields[i].Name = truncateRunes(fields[i].Name, discordMaxFieldName)
		fields[i].Value = truncateRunes(fields[i].Value, discordMaxFieldValue)
	}

	return discordEmbed{
		Title:       truncateRunes("🚨 "+title, discordMaxTitle),
		Description: "🔍 Investigation will follow up with findings once complete.",
		Color:       severityEmbedColor(severity),
		Fields:      fields,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}

// buildResolvedAlertEmbed renders the alert-resolved message as a card.
func buildResolvedAlertEmbed(incidentID, title, severity, environment string, resolvedAt, startedAt time.Time) discordEmbed {
	fields := []discordEmbedField{
		{Name: "Severity", Value: fmt.Sprintf("%s %s", severityColor(severity), severity), Inline: true},
		{Name: "Environment", Value: orUnknown(environment), Inline: true},
	}
	if !startedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Started",
			Value:  startedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			Inline: true,
		})
	}
	if !resolvedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Resolved",
			Value:  resolvedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			Inline: true,
		})
		fields = append(fields, discordEmbedField{
			Name:   "Downtime",
			Value:  resolvedAt.Sub(startedAt).Round(time.Second).String(),
			Inline: true,
		})
	}
	fields = append(fields, discordEmbedField{
		Name:   "Incident ID",
		Value:  fmt.Sprintf("`%s`", incidentID),
		Inline: false,
	})

	for i := range fields {
		fields[i].Name = truncateRunes(fields[i].Name, discordMaxFieldName)
		fields[i].Value = truncateRunes(fields[i].Value, discordMaxFieldValue)
	}

	return discordEmbed{
		Title:       truncateRunes("✅ "+title, discordMaxTitle),
		Description: "The alert has recovered and is no longer firing.",
		Color:       discordColorResolved,
		Fields:      fields,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}

// severityEmbedColor maps a severity to its embed bar color.
func severityEmbedColor(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return discordColorCritical
	case "high":
		return discordColorHigh
	case "warning":
		return discordColorWarning
	case "info":
		return discordColorInfo
	default:
		return discordColorNeutral
	}
}

// orUnknown returns s, or "unknown" when empty.
func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
