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

// buildDiscordMessage renders a report as Discord embeds.
//
// A report about a single incident is ONE card: the report header (availability,
// period) and the incident's identity and findings belong together, and
// splitting them produced two cards for a notification that describes one
// thing. Multi-incident reports keep a summary card followed by one card per
// incident, because there the summary is genuinely a table of contents.
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

	if len(incidents) == 1 {
		inc := incidents[0]
		return &discordMessage{
			Username: "Pamawas Reporter",
			Embeds: []discordEmbed{
				r.buildSingleIncidentEmbed(report, inc, evidence[inc.ID], inclusionReasons[inc.ID]),
			},
		}, nil
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

// buildSingleIncidentEmbed renders a one-incident report as a card built for
// someone who has just been paged.
//
// The order is deliberate: what is broken, the likely cause, what to do about
// it, then the supporting evidence. Report-generator metadata (availability for
// the period, incident counts) is omitted — during a single incident it says
// nothing about the incident, and "Availability: 100.00%" on a card about a
// critical outage actively misleads.
func (r *Reporter) buildSingleIncidentEmbed(report models.Report, inc models.Incident, evList []models.Evidence, inclusionReason string) discordEmbed {
	embed := buildIncidentEmbed(inc, nil, inclusionReason, report.Timezone)

	cause, actions, evidence := triageFindings(evList)

	var sb strings.Builder
	if cause != nil {
		sb.WriteString("**🎯 Likely cause**\n")
		fmt.Fprintf(&sb, "%s%s\n", cause.Content, evidenceSourceSuffix(*cause))
	}
	if len(actions) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("**🛠 Suggested actions**\n")
		for i, rec := range actions {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, rec.Content)
		}
	}
	if len(evidence) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("**📋 Supporting evidence**\n")
		for _, ev := range evidence {
			fmt.Fprintf(&sb, "%s **%s** (%.0f%%) %s%s\n",
				evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100,
				ev.Content, evidenceSourceSuffix(ev))
		}
	}
	if sb.Len() == 0 {
		sb.WriteString("_🔍 Investigation unavailable_")
	}

	embed.Description = truncateRunes(sb.String(), discordMaxDescription)
	embed.Footer = reportFooter(report)
	embed.Timestamp = report.GeneratedAt.UTC().Format(time.RFC3339)
	return embed
}

// triageFindings splits evidence into what an on-call reader needs, in reading
// order: the most probable cause, the suggested actions, and the rest of the
// evidence. The single highest-confidence likely_cause leads; a hypothesis is
// used only when the investigation reached no likely cause, and is labelled as
// such by its own type line.
func triageFindings(evList []models.Evidence) (cause *models.Evidence, actions, evidence []models.Evidence) {
	var likely, hypotheses []models.Evidence
	for i := range evList {
		ev := evList[i]
		switch ev.Type {
		case evidenceTypeRecommendation:
			actions = append(actions, ev)
		case "likely_cause":
			likely = append(likely, ev)
		case "hypothesis":
			hypotheses = append(hypotheses, ev)
		default:
			evidence = append(evidence, ev)
		}
	}

	switch {
	case len(likely) > 0:
		best := mostConfident(likely)
		cause = &best
		evidence = append(evidence, remaining(likely, best)...)
	case len(hypotheses) > 0:
		best := mostConfident(hypotheses)
		cause = &best
		evidence = append(evidence, remaining(hypotheses, best)...)
	}
	return cause, actions, evidence
}

// mostConfident returns the highest-confidence finding, keeping input order for
// ties so the card is deterministic.
func mostConfident(list []models.Evidence) models.Evidence {
	best := list[0]
	for _, ev := range list[1:] {
		if ev.Confidence > best.Confidence {
			best = ev
		}
	}
	return best
}

// remaining returns every finding except the one already surfaced as the cause.
func remaining(list []models.Evidence, used models.Evidence) []models.Evidence {
	out := make([]models.Evidence, 0, len(list)-1)
	skipped := false
	for _, ev := range list {
		if !skipped && ev.ID == used.ID && ev.Content == used.Content {
			skipped = true
			continue
		}
		out = append(out, ev)
	}
	return out
}

// buildSummaryEmbed renders the report headline, availability, and counts.
func (r *Reporter) buildSummaryEmbed(ctx context.Context, report models.Report, incidents []models.Incident) discordEmbed {
	needsAttention, _ := countIncidentStatuses(incidents)

	title := "📅 Daily Infrastructure Report"
	if report.ReportType != "daily" {
		title = "🚨 High Severity Immediate Report"
	}

	// Red when anything needs attention, green when everything recovered.
	color := discordColorResolved
	if needsAttention > 0 {
		color = discordColorCritical
	}

	return discordEmbed{
		Title:       truncateRunes(title, discordMaxTitle),
		Description: truncateRunes(r.reportHeader(ctx, report, incidents), discordMaxDescription),
		Color:       color,
		Footer:      reportFooter(report),
		Timestamp:   report.GeneratedAt.UTC().Format(time.RFC3339),
	}
}

// reportHeader renders the lines that describe the report itself: availability,
// incident counts, and the period covered.
func (r *Reporter) reportHeader(ctx context.Context, report models.Report, incidents []models.Incident) string {
	needsAttention, resolvedCount := countIncidentStatuses(incidents)

	// Availability is only claimed when a real metric backs it; otherwise the
	// report states it is unavailable rather than inventing a percentage.
	availability := r.getAvailabilityMetric(ctx, report.PeriodStart, report.PeriodEnd)
	availabilityLine := "**Availability:** unavailable (no availability metric configured)"
	if availability >= 0 {
		availabilityLine = fmt.Sprintf("**Availability:** %.2f%%", availability)
	}

	periodStart := report.PeriodStart.In(loadLocation(report.Timezone)).Format("2006-01-02 15:04 MST")
	periodEnd := report.PeriodEnd.In(loadLocation(report.Timezone)).Format("2006-01-02 15:04 MST")

	return strings.Join([]string{
		availabilityLine,
		fmt.Sprintf("**Incidents:** %d total · **%d** need attention · **%d** resolved",
			len(incidents), needsAttention, resolvedCount),
		"",
		fmt.Sprintf("🕒 %s → %s", periodStart, periodEnd),
	}, "\n")
}

// reportFooter records which template produced the report and when.
func reportFooter(report models.Report) *discordEmbedFooter {
	return &discordEmbedFooter{
		Text: truncateRunes(fmt.Sprintf("Template: %s · Generated: %s",
			report.TemplateVersion,
			report.GeneratedAt.UTC().Format("2006-01-02 15:04:05 UTC")), discordMaxFooter),
	}
}

// buildIncidentEmbed renders one incident as a bordered card: colored left bar,
// status in the title, bold metadata fields, and the investigation findings.
// A nil evList leaves the description empty, so the caller can put its own
// header there (see buildSingleIncidentEmbed).
func buildIncidentEmbed(inc models.Incident, evList []models.Evidence, inclusionReason, timezone string) discordEmbed {
	loc := loadLocation(timezone)

	title := fmt.Sprintf("🚨 %s", inc.Title)
	if inc.Status == "resolved" {
		title = fmt.Sprintf("✅ %s", inc.Title)
	}

	// Only fields that carry information are shown. A field reading "unknown"
	// looks like a fact the investigation established; the truth is that the
	// source never supplied it, and an on-call reader should not have to
	// distinguish the two while triaging.
	fields := []discordEmbedField{
		{Name: "Status", Value: fmt.Sprintf("%s %s%s", statusColor(inc.Status), inc.Status, inclusionReasonLabel(inclusionReason)), Inline: true},
		{Name: "Severity", Value: fmt.Sprintf("%s %s", severityColor(inc.Severity), inc.Severity), Inline: true},
	}
	if environment := inc.Environment; environment != "" {
		fields = append(fields, discordEmbedField{Name: "Environment", Value: environment, Inline: true})
	}
	fields = append(fields, discordEmbedField{Name: "Affected", Value: affectedLabel(inc), Inline: false})

	fields = append(fields, discordEmbedField{
		Name:   "Started",
		Value:  inc.StartedAt.In(loc).Format("2006-01-02 15:04:05 MST"),
		Inline: true,
	})
	if !inc.ResolvedAt.IsZero() {
		fields = append(fields,
			discordEmbedField{
				Name:   "Resolved",
				Value:  inc.ResolvedAt.In(loc).Format("2006-01-02 15:04:05 MST"),
				Inline: true,
			},
			discordEmbedField{
				Name:   "Downtime",
				Value:  inc.ResolvedAt.Sub(inc.StartedAt).Round(time.Second).String(),
				Inline: true,
			},
		)
	}
	fields = append(fields, discordEmbedField{Name: "Incident ID", Value: fmt.Sprintf("`%s`", inc.ID), Inline: false})

	// Findings go in the description: it allows 4096 chars, where a field
	// value stops at 1024. Findings are the substance of the report, so they
	// must not be truncated to a single field.
	description := ""
	if evList != nil {
		description = formatFindingsForEmbed(evList, discordMaxDescription)
	}

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

// formatFindingsForEmbed renders investigation findings as a compact list that
// fits within budget characters.
//
// Each finding is tagged with the system it came from, so a reader can tell a
// metric from a log line from the model's own inference without leaving the
// card. Recommended actions are rendered as their own section: they answer
// "what should I do", not "what did we find".
func formatFindingsForEmbed(evList []models.Evidence, budget int) string {
	if len(evList) == 0 {
		return "_🔍 Investigation unavailable_"
	}

	findings, recommendations := splitRecommendations(evList)

	var sb strings.Builder
	sb.WriteString("**🔍 Investigation Findings**\n")
	for i, ev := range findings {
		line := fmt.Sprintf("%s **%s** (%.0f%%) %s%s\n",
			evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100,
			ev.Content, evidenceSourceSuffix(ev))
		// Leave room for the truncation notice and the recommendations section.
		if sb.Len()+len(line) > budget-200 {
			fmt.Fprintf(&sb, "\n_… %d more findings omitted_\n", len(findings)-i)
			break
		}
		sb.WriteString(line)
	}

	if len(recommendations) > 0 {
		sb.WriteString("\n**🛠 Suggested Actions**\n")
		for i, rec := range recommendations {
			line := fmt.Sprintf("%d. %s\n", i+1, rec.Content)
			if sb.Len()+len(line) > budget-20 {
				fmt.Fprintf(&sb, "_… %d more actions omitted_\n", len(recommendations)-i)
				break
			}
			sb.WriteString(line)
		}
	}

	return truncateRunes(sb.String(), budget)
}

// splitRecommendations partitions evidence into findings and suggested actions,
// preserving each group's order.
func splitRecommendations(evList []models.Evidence) (findings, recommendations []models.Evidence) {
	for _, ev := range evList {
		if ev.Type == evidenceTypeRecommendation {
			recommendations = append(recommendations, ev)
			continue
		}
		findings = append(findings, ev)
	}
	return findings, recommendations
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
		if err = rows.Scan(&id, &reason); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		reasons[id] = reason
	}
	if err = rows.Err(); err != nil {
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

// alertSubject is the identity of what an alert is about. It exists so the
// firing and resolved cards describe the subject identically — the same alert
// must not read differently depending on whether it just fired or recovered.
type alertSubject struct {
	Title       string
	Severity    string
	Environment string
	Services    string
	Target      string
	Job         string
	StartedAt   time.Time
}

// fields renders the identity block shared by the firing and resolved cards.
//
// "What is down" is answered by whichever of these the source actually
// provides. Grafana's default alerting payload names no service at all: it
// carries the target (`instance`) and the scrape job (`job`). Rendering only
// "Services" left those alerts reading "Services: grafana" — the sender, not
// the subject — which is why the target and job are shown first.
func (s alertSubject) fields() []discordEmbedField {
	fields := []discordEmbedField{
		{Name: "Severity", Value: fmt.Sprintf("%s %s", severityColor(s.Severity), s.Severity), Inline: true},
		{Name: "Environment", Value: orUnknown(s.Environment), Inline: true},
	}
	if s.Target != "" {
		fields = append(fields, discordEmbedField{Name: "Target", Value: s.Target, Inline: true})
	}
	if s.Job != "" {
		fields = append(fields, discordEmbedField{Name: "Job", Value: s.Job, Inline: true})
	}
	// Only show Services when it says something the target does not: sources
	// that name no service leave it empty, and ingest derives it from the
	// target otherwise, so repeating it would be noise.
	if s.Services != "" && s.Services != s.Target {
		fields = append(fields, discordEmbedField{Name: "Services", Value: s.Services, Inline: true})
	}
	if !s.StartedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Started",
			Value:  s.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			Inline: true,
		})
	}
	return fields
}

// trimAlertFields applies Discord's field limits to every field.
func trimAlertFields(fields []discordEmbedField) []discordEmbedField {
	for i := range fields {
		fields[i].Name = truncateRunes(fields[i].Name, discordMaxFieldName)
		fields[i].Value = truncateRunes(fields[i].Value, discordMaxFieldValue)
	}
	return fields
}

// buildAlertEmbed renders the immediate high-severity alert as a card.
//
// This is the firing notification: it carries the subject's identity and says
// the investigation will follow up. The follow-up report arrives later as its
// own cards with the findings.
func buildAlertEmbed(subject alertSubject) discordEmbed {
	fields := trimAlertFields(subject.fields())
	return discordEmbed{
		Title:       truncateRunes("🚨 "+subject.Title, discordMaxTitle),
		Description: "🔍 Investigation will follow up with findings once complete.",
		Color:       severityEmbedColor(subject.Severity),
		Fields:      fields,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}

// buildResolvedAlertEmbed renders the alert-resolved message as a card.
func buildResolvedAlertEmbed(subject alertSubject, resolvedAt time.Time) discordEmbed {
	fields := subject.fields()
	if !resolvedAt.IsZero() {
		fields = append(fields,
			discordEmbedField{
				Name:   "Resolved",
				Value:  resolvedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
				Inline: true,
			},
			discordEmbedField{
				Name:   "Downtime",
				Value:  resolvedAt.Sub(subject.StartedAt).Round(time.Second).String(),
				Inline: true,
			},
		)
	}
	fields = trimAlertFields(fields)

	return discordEmbed{
		Title:       truncateRunes("✅ "+subject.Title, discordMaxTitle),
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
