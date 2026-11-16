package service

import (
	"context"
	"fmt"
	"sort"
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

// How much of an open condition's cause and action reach the morning digest.
//
// One line each, clipped: the digest is scanned, not read. The full findings
// stay in the database and are served per incident on demand.
const (
	digestCauseLimit  = 180
	digestActionLimit = 140
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
// The three report shapes are read in different circumstances, so they render
// differently:
//
//   - daily: a morning digest. ONE card listing what happened, with a detail
//     card only for incidents that still need attention.
//   - single incident: ONE card with the triage order (cause, actions,
//     evidence), because it is read by whoever is on call right now.
//   - multi-incident immediate: a summary card plus one card per incident.
func (r *Reporter) buildDiscordMessage(ctx context.Context, report models.Report) (*discordMessage, error) {
	incidents, inclusionReasons, err := r.loadReportIncidents(ctx, report.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load report incidents: %w", err)
	}

	if report.ReportType == "daily" {
		return r.buildDailyDigestMessage(ctx, report, incidents)
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
		embeds = append(embeds, buildIncidentEmbed(report, inc, evidence[inc.ID], inclusionReasons[inc.ID]))
	}

	return &discordMessage{Username: "Pamawas Reporter", Embeds: embeds}, nil
}

// buildDailyDigestMessage renders the daily report as ONE morning digest card.
//
// A daily report is read on a phone before coffee. It was previously emitted as
// a summary card plus a full card per incident, each carrying the complete
// investigation transcript; forty incidents became forty-odd cards of scrolling
// in which the one incident still open was impossible to find.
//
// The digest answers the three questions a reader actually has, in order: what
// is still broken, what recovered, and what happened overall. It is deliberately
// a single message: webhooks cannot create threads (thread_name only works in
// forum channels), so anything posted as "detail" would land in the channel and
// recreate the wall of text.
func (r *Reporter) buildDailyDigestMessage(ctx context.Context, report models.Report, incidents []models.Incident) (*discordMessage, error) {
	open, recovered := splitByStatus(incidents)

	return &discordMessage{
		Username: "Pamawas Reporter",
		Embeds:   []discordEmbed{r.buildDailyDigestEmbed(ctx, report, incidents, open, recovered)},
	}, nil
}

// buildDailyDigestEmbed renders the digest itself: one scannable card.
func (r *Reporter) buildDailyDigestEmbed(ctx context.Context, report models.Report, incidents, open, recovered []models.Incident) discordEmbed {
	// The summary is fetched here, synchronously, so it is part of the message
	// that goes out. It is never posted as a follow-up: a digest that arrives
	// and then gains its headline a minute later is worse than one that never
	// had one. summarizeDay is bounded by LLM_SUMMARY_TIMEOUT and returns ""
	// on any failure, so delivery still happens on time without it.
	summary := r.summarizeDay(ctx, report, incidents)

	availability := r.getAvailabilityMetric(ctx, report.PeriodStart, report.PeriodEnd)
	availabilityLine := "**Availability:** unavailable (no availability metric configured)"
	if availability >= 0 {
		availabilityLine = fmt.Sprintf("**Availability:** %.2f%%", availability)
	}

	var sb strings.Builder
	if summary != "" {
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, "%s\n**%d incident(s)** · **%d** still open · **%d** recovered\n",
		availabilityLine, len(incidents), len(open), len(recovered))

	// Needs-attention incidents are listed individually: each one is a thing
	// someone must decide about today. Recovered ones are grouped by condition,
	// because "Monitoring Target Down ×12" is what a reader needs from history,
	// not twelve identical lines.
	//
	// Findings are loaded only for the open incidents. They are what the reader
	// has to act on before starting the day; a recovered incident's cause is
	// history, and 40 paragraphs of it would recreate the wall of text the
	// digest exists to replace.
	openEvidence := r.loadEvidenceForOpen(ctx, open)
	digestOpenSection(&sb, open, openEvidence)
	digestRecoveredSection(&sb, recovered)

	if len(incidents) == 0 {
		sb.WriteString("\nNothing to report.")
	}

	// Red while anything is still open, green once everything recovered.
	color := discordColorResolved
	if len(open) > 0 {
		color = discordColorCritical
	}

	return discordEmbed{
		Title:       truncateRunes(fmt.Sprintf("📅 Daily Report · %s", report.PeriodStart.In(loadLocation(report.Timezone)).Format("2006-01-02")), discordMaxTitle),
		Description: truncateRunes(sb.String(), discordMaxDescription),
		Color:       color,
		Footer:      reportFooter(report),
		Timestamp:   report.GeneratedAt.UTC().Format(time.RFC3339),
	}
}

// loadEvidenceForOpen fetches findings for the incidents that are still open.
//
// A failure is logged and treated as "no findings": the digest must still go
// out, and a missing cause line is better than a missing report.
func (r *Reporter) loadEvidenceForOpen(ctx context.Context, open []models.Incident) map[string][]models.Evidence {
	if len(open) == 0 {
		return nil
	}
	ids := make([]string, 0, len(open))
	for _, inc := range open {
		ids = append(ids, inc.ID)
	}
	evidence, err := r.loadEvidenceForIncidents(ctx, ids)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load findings for open incidents; digest continues without them")
		return nil
	}
	return evidence
}

// digestOpenSection writes incidents that still need attention, grouped by
// condition.
//
// Correlation can open several incidents for one condition (the same target
// flapping), and listing each one separately produced ten identical lines in a
// real report. Grouping shows the reader that it is one problem, how many times
// it reopened, and how long the earliest instance has been open — which is the
// number that matters.
//
// Each group also carries its most likely cause and the first recommended
// action, on two indented lines. That is the whole reason the investigation
// runs: an on-call reader needs to know what to do about the open condition,
// and a bare "still open" line made them open a second card to find out.
func digestOpenSection(sb *strings.Builder, open []models.Incident, evidence map[string][]models.Evidence) {
	if len(open) == 0 {
		return
	}

	type group struct {
		title    string
		severity string
		affected []string
		count    int
		earliest time.Time
		// evidenceFrom is the most recent occurrence, whose findings are the
		// current ones. An older occurrence's cause may describe a different
		// failure of the same condition.
		evidenceFrom string
		latest       time.Time
	}
	var order []string
	groups := make(map[string]*group)

	for _, inc := range open {
		key := inc.Title + "\x00" + affectedLabel(inc)
		g, ok := groups[key]
		if !ok {
			g = &group{title: inc.Title, severity: inc.Severity,
				affected: inc.AffectedServices, earliest: inc.StartedAt,
				evidenceFrom: inc.ID, latest: inc.StartedAt}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		if inc.StartedAt.Before(g.earliest) {
			g.earliest = inc.StartedAt
		}
		if inc.StartedAt.After(g.latest) {
			g.latest = inc.StartedAt
			g.evidenceFrom = inc.ID
		}
	}

	fmt.Fprintf(sb, "\n**🔴 Needs attention**\n")
	for i, key := range order {
		g := groups[key]
		if sb.Len() > discordMaxDescription-300 {
			fmt.Fprintf(sb, "• _… and %d more conditions_\n", len(order)-i)
			return
		}

		line := fmt.Sprintf("• %s **%s**", severityColor(g.severity), escapeDiscordMarkdown(g.title))
		if affected := strings.Join(g.affected, ", "); affected != "" {
			line += " · " + escapeDiscordMarkdown(affected)
		}
		fmt.Fprintf(sb, "%s · open since %s", line, g.earliest.UTC().Format("15:04 UTC"))
		if g.count > 1 {
			fmt.Fprintf(sb, " (×%d, reopened)", g.count)
		}
		sb.WriteString("\n")

		writeDigestTriageLines(sb, evidence[g.evidenceFrom])
	}
}

// writeDigestTriageLines writes a one-line cause and a one-line action for an
// open condition.
//
// Deliberately one line each and clipped: the digest is read in ten seconds.
// The full findings, including every fact and every action, stay in the
// database and are available per incident on demand.
func writeDigestTriageLines(sb *strings.Builder, evList []models.Evidence) {
	cause, actions, _ := triageFindings(evList)

	if cause != nil {
		label := "🎯"
		if cause.Type == "hypothesis" {
			label = "💭" // unconfirmed: say so with the icon, not a paragraph
		}
		fmt.Fprintf(sb, "  %s %s\n", label, clipSentence(cause.Content, digestCauseLimit))
	}
	if len(actions) > 0 {
		fmt.Fprintf(sb, "  🛠 %s\n", clipSentence(actions[0].Content, digestActionLimit))
	}
}

// clipSentence shortens text to at most limit runes, cutting at a word
// boundary so the result does not end mid-token.
func clipSentence(s string, limit int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len([]rune(flat)) <= limit {
		return escapeDiscordMarkdown(flat)
	}
	runes := []rune(flat)
	cut := string(runes[:limit])
	if idx := strings.LastIndex(cut, " "); idx > limit/2 {
		cut = cut[:idx]
	}
	return escapeDiscordMarkdown(strings.TrimRight(cut, " ,.;:")) + "…"
}

// digestRecoveredSection writes recovered incidents grouped by condition.
//
// A busy night produces the same alert repeatedly; listing every occurrence
// buries the one that still needs attention and tells the reader nothing beyond
// the count. Grouping keeps the card scannable and surfaces the pattern.
func digestRecoveredSection(sb *strings.Builder, recovered []models.Incident) {
	if len(recovered) == 0 {
		return
	}

	type group struct {
		title    string
		severity string
		affected []string
		count    int
		total    time.Duration
	}
	var order []string
	groups := make(map[string]*group)

	for _, inc := range recovered {
		// Identity is the condition, not the occurrence.
		key := inc.Title + "\x00" + affectedLabel(inc)
		g, ok := groups[key]
		if !ok {
			g = &group{title: inc.Title, severity: inc.Severity, affected: inc.AffectedServices}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		g.total += inc.ResolvedAt.Sub(inc.StartedAt)
	}

	fmt.Fprintf(sb, "\n**✅ Recovered**\n")
	for i, key := range order {
		g := groups[key]
		if sb.Len() > discordMaxDescription-120 {
			fmt.Fprintf(sb, "• _… and %d more conditions_\n", len(order)-i)
			return
		}

		line := fmt.Sprintf("• %s **%s** · %s", severityColor(g.severity),
			escapeDiscordMarkdown(g.title), escapeDiscordMarkdown(strings.Join(g.affected, ", ")))
		if len(g.affected) == 0 {
			line = fmt.Sprintf("• %s **%s**", severityColor(g.severity), escapeDiscordMarkdown(g.title))
		}
		if g.count > 1 {
			// Average is the honest figure: one long outage and several short
			// ones are not the same story, and a total would hide that.
			avg := (g.total / time.Duration(g.count)).Round(time.Second)
			fmt.Fprintf(sb, "%s ×%d · ~%s each\n", line, g.count, avg)
		} else {
			fmt.Fprintf(sb, "%s · %s\n", line, g.total.Round(time.Second))
		}
	}
}

// splitByStatus partitions incidents into those still needing attention and
// those already recovered, each sorted for reading: most severe first among the
// open ones, most recent first among the recovered.
func splitByStatus(incidents []models.Incident) (open, recovered []models.Incident) {
	for _, inc := range incidents {
		switch inc.Status {
		case "open", "investigating":
			open = append(open, inc)
		default:
			recovered = append(recovered, inc)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		if a, b := severityRank(open[i].Severity), severityRank(open[j].Severity); a != b {
			return a > b
		}
		return open[i].StartedAt.Before(open[j].StartedAt)
	})
	sort.SliceStable(recovered, func(i, j int) bool {
		return recovered[i].ResolvedAt.After(recovered[j].ResolvedAt)
	})
	return open, recovered
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
	embed := buildIncidentEmbed(report, inc, nil, inclusionReason)

	cause, actions, evidence := triageFindings(evList)

	var sb strings.Builder

	// What is broken leads the card. A reader opening this was paged about a
	// specific thing; making them read a paragraph of analysis before reaching
	// "what, where, how long" inverts their question. Discord renders the
	// description above the fields, so this has to be in the description —
	// fields alone always appear below the findings.
	sb.WriteString(r.incidentHeadline(inc, report))

	if cause != nil {
		// Say which kind of claim this is. A hypothesis is the investigator
		// admitting it did not converge, and presenting that under "Likely
		// cause" with no qualifier told a paged engineer the opposite of what
		// the evidence supports.
		if cause.Type == "hypothesis" {
			fmt.Fprintf(&sb, "\n**💭 Possible cause** _(hypothesis, %.0f%% confidence — not confirmed)_\n", cause.Confidence*100)
		} else {
			sb.WriteString("\n**🎯 Likely cause**\n")
		}
		fmt.Fprintf(&sb, "%s%s\n", escapeDiscordMarkdown(cause.Content), evidenceSourceSuffix(*cause))
	}
	if len(actions) > 0 {
		sb.WriteString("\n**🛠 Suggested actions**\n")
		for i, rec := range actions {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, escapeDiscordMarkdown(rec.Content))
		}
	}
	if len(evidence) > 0 {
		sb.WriteString("\n**📋 Supporting evidence**\n")
		for _, ev := range evidence {
			fmt.Fprintf(&sb, "%s **%s** (%.0f%%) %s%s\n",
				evidenceIcon(ev.Type), strings.ToUpper(ev.Type), ev.Confidence*100,
				escapeDiscordMarkdown(ev.Content), evidenceSourceSuffix(ev))
		}
	}
	if cause == nil && len(actions) == 0 && len(evidence) == 0 {
		sb.WriteString("_🔍 Investigation unavailable_")
	}

	embed.Description = truncateRunes(sb.String(), discordMaxDescription)
	// The headline above already states what is affected, how severe it is and
	// how long it has been down, so those fields would repeat it in a second
	// block below the findings. Keep only what the headline cannot carry:
	// exact timestamps and the incident ID someone will paste into a search.
	embed.Fields = onlyFields(embed.Fields, "Started", "Resolved", "Downtime", "Incident ID")
	embed.Footer = reportFooter(report)
	embed.Timestamp = report.GeneratedAt.UTC().Format(time.RFC3339)
	return embed
}

// onlyFields keeps the named fields, preserving their original order and
// skipping any that are absent.
func onlyFields(fields []discordEmbedField, keep ...string) []discordEmbedField {
	if len(fields) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(keep))
	for _, name := range keep {
		allowed[name] = struct{}{}
	}
	kept := make([]discordEmbedField, 0, len(keep))
	for _, f := range fields {
		if _, ok := allowed[f.Name]; ok {
			kept = append(kept, f)
		}
	}
	return kept
}

// incidentHeadline renders the first thing an on-call reader needs: what broke,
// where, how bad, and whether it is still broken.
//
// One line, because it is a glance, not a paragraph: the fields below the
// description already carry the exact timestamps and the incident ID for anyone
// who needs to look further.
func (r *Reporter) incidentHeadline(inc models.Incident, report models.Report) string {
	parts := []string{severityColor(inc.Severity) + " **" + escapeDiscordMarkdown(inc.Title) + "**"}
	parts = append(parts, "on **"+escapeDiscordMarkdown(affectedLabel(inc))+"**")
	// The severity word, not only the badge colour: a colour is the only
	// carrier of "critical" otherwise, which is invisible to a colour-blind
	// reader and ambiguous to everyone skimming.
	if inc.Severity != "" {
		parts = append(parts, "· "+strings.ToUpper(escapeDiscordMarkdown(inc.Severity)))
	}
	if inc.Environment != "" {
		parts = append(parts, "("+escapeDiscordMarkdown(inc.Environment)+")")
	}

	var state string
	if inc.ResolvedAt.IsZero() {
		// "Still down" already carries the status, so the status word itself
		// would read "Still down — open open". The badge keeps the colour cue.
		state = fmt.Sprintf("%s Still down — %s so far",
			statusColor(inc.Status), formatElapsed(report.GeneratedAt.Sub(inc.StartedAt)))
	} else {
		state = fmt.Sprintf("%s Recovered after %s",
			statusColor(inc.Status), formatElapsed(inc.ResolvedAt.Sub(inc.StartedAt)))
	}

	return strings.Join(parts, " ") + "\n" + state + "\n"
}

// formatElapsed renders a duration the way an on-call reader says it: "4m12s",
// "52m", "2h05m", "1d3h". Seconds are shown only while they still matter — a
// freshly paged incident — and dropped once the unit above is the whole story,
// because "52m00s" reads like a timer rather than a duration.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%dm", int(d.Minutes()))
		}
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		if d%time.Hour == 0 {
			return fmt.Sprintf("%dh", int(d.Hours()))
		}
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
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
func buildIncidentEmbed(report models.Report, inc models.Incident, evList []models.Evidence, inclusionReason string) discordEmbed {
	loc := loadLocation(report.Timezone)

	// The report name leads the title. A bare "✅ RAM" was indistinguishable
	// from an immediate alert card, which made a daily report containing a
	// `warning` incident look like a warning alert that had bypassed the
	// HIGH_SEVERITY_THRESHOLD gate. The gate was working; the card just never
	// said where it came from. Status stays in the Status field and the bar
	// colour, so the title is free to carry provenance.
	title := fmt.Sprintf("%s · %s", reportLabel(report), escapeDiscordMarkdown(inc.Title))

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
		Footer:      reportFooter(report),
	}
}

// reportLabel names the report an incident card belongs to.
//
// Without it a report card is indistinguishable from an immediate alert card,
// and that caused real confusion: a daily report containing a `warning`
// incident looked like a warning alert that had bypassed the
// HIGH_SEVERITY_THRESHOLD gate. The gate was working — those cards come from
// the scheduled report, which summarizes every incident regardless of
// severity — but nothing on the card said so.
func reportLabel(report models.Report) string {
	if report.ReportType == "daily" {
		return "📅 Daily Report"
	}
	return "🚨 High-Severity Report"
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
			escapeDiscordMarkdown(ev.Content), evidenceSourceSuffix(ev))
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
			line := fmt.Sprintf("%d. %s\n", i+1, escapeDiscordMarkdown(rec.Content))
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
	// AlertKey is the dedup key backing the one-shot claim. It is the only
	// stable identifier available at alert time, so it links the firing page,
	// the resolved page, and the follow-up report.
	AlertKey string
	// PriorOccurrences is how many times this same condition (environment +
	// affected entity + alert name) started in the 24h before this one. Zero
	// means this is the first time today; a high number means the reader is
	// looking at a chronic problem, which changes what they should do next.
	PriorOccurrences int
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
	// Target, job and service values are machine identifiers — `docker_cadvisor`,
	// `system_node` — so they need the same escaping as evidence text: Discord
	// reads a bare underscore as an italic delimiter and drops it.
	if s.Target != "" {
		fields = append(fields, discordEmbedField{Name: "Target", Value: escapeDiscordMarkdown(s.Target), Inline: true})
	}
	if s.Job != "" {
		fields = append(fields, discordEmbedField{Name: "Job", Value: escapeDiscordMarkdown(s.Job), Inline: true})
	}
	// Only show Services when it says something the target does not: sources
	// that name no service leave it empty, and ingest derives it from the
	// target otherwise, so repeating it would be noise.
	if s.Services != "" && s.Services != s.Target {
		fields = append(fields, discordEmbedField{Name: "Services", Value: escapeDiscordMarkdown(s.Services), Inline: true})
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

// recurrenceLine states how often this condition has recurred in the last 24h.
//
// It is the one piece of history that changes an on-call reader's decision: a
// first occurrence is something to watch, a sixth tells them the last five were
// never really fixed. Returns "" when there is no history to report.
func (s alertSubject) recurrenceLine() string {
	if s.PriorOccurrences <= 0 {
		return ""
	}
	return fmt.Sprintf("🔁 **%s occurrence** in the last 24h — this condition keeps recurring.",
		ordinal(s.PriorOccurrences+1))
}

// ordinal renders 1 -> "1st", 2 -> "2nd", 22 -> "22nd".
func ordinal(n int) string {
	suffix := "th"
	// 11, 12 and 13 take "th" despite their last digit.
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// buildAlertEmbed renders the immediate high-severity alert as a card.
//
// This is the firing notification: it carries the subject's identity and says
// the investigation will follow up. The follow-up report arrives later as its
// own cards with the findings.
func buildAlertEmbed(subject alertSubject) discordEmbed {
	fields := subject.fields()
	// "Firing for" is the urgency signal: a 30-second blip and a 40-minute
	// outage read identically from a start timestamp alone, and the reader
	// would have to subtract it from the clock to find out which this is.
	// It also completes the inline grid at two full rows of three.
	if !subject.StartedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Firing for",
			Value:  time.Since(subject.StartedAt).Round(time.Second).String(),
			Inline: true,
		})
	}
	fields = trimAlertFields(fields)

	// State what the system is actually doing. The previous line promised
	// "findings once complete", which told a paged engineer nothing they could
	// act on and implied progress that had not started yet.
	description := "🔍 Investigation starts automatically once the incident is correlated. Findings follow as a separate report."
	if recurrence := subject.recurrenceLine(); recurrence != "" {
		description = recurrence + "\n\n" + description
	}

	return discordEmbed{
		Title:       truncateRunes("🚨 "+escapeDiscordMarkdown(subject.Title), discordMaxTitle),
		Description: truncateRunes(description, discordMaxDescription),
		Color:       severityEmbedColor(subject.Severity),
		Fields:      fields,
		Footer:      alertFooter(subject.AlertKey),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}

// alertFooter records the alert's correlation key. No incident exists yet when
// an alert fires, so this key is what lets the follow-up report be matched to
// the page that started it.
func alertFooter(alertKey string) *discordEmbedFooter {
	if alertKey == "" {
		return nil
	}
	return &discordEmbedFooter{
		Text: truncateRunes("Alert key: "+alertKey, discordMaxFooter),
	}
}

// buildResolvedAlertEmbed renders the alert-resolved message as a card.
//
// The description states the one fact the fields cannot: how long it was out.
// It used to read "The alert has recovered and is no longer firing.", which
// repeated both the ✅ title and the Resolved field without adding anything.
// Downtime is folded into that sentence rather than kept as a seventh field,
// which would wrap alone onto its own row (Discord fits three inline fields per
// row) and made the card look unfinished.
// buildResolvedAlertEmbed renders the alert-resolved message as a card.
//
// The description states the one fact the fields cannot: how long it was out.
// It used to read "The alert has recovered and is no longer firing.", which
// repeated both the ✅ title and the Resolved field without adding anything.
// Downtime is folded into that sentence rather than kept as a seventh field,
// which would wrap alone onto its own row (Discord fits three inline fields per
// row) and made the card look unfinished.
//
// It deliberately reports no cause: the investigation is still running when a
// resolve is announced, and the findings report follows separately. Saying so
// is better than leaving the reader to wonder where the analysis is.
func buildResolvedAlertEmbed(subject alertSubject, resolvedAt time.Time, incidentID string) discordEmbed {
	fields := subject.fields()
	description := "Recovered."
	if !resolvedAt.IsZero() {
		fields = append(fields, discordEmbedField{
			Name:   "Resolved",
			Value:  resolvedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			Inline: true,
		})
		if !subject.StartedAt.IsZero() {
			description = fmt.Sprintf("Recovered after **%s**.",
				resolvedAt.Sub(subject.StartedAt).Round(time.Second))
		}
	}
	if recurrence := subject.recurrenceLine(); recurrence != "" {
		// On a recovery this matters as much as on the page: a condition that
		// has already come back repeatedly is not fixed, whatever this
		// particular recovery looks like.
		description += "\n\n" + recurrence
	}
	// Promise findings only when an incident is linked. Without one we cannot
	// tell "correlation has not run yet" from "none will ever exist", so the
	// card says nothing rather than asserting either.
	if incidentID != "" {
		description += "\n\n🔍 Investigation findings follow in a separate report."
	}
	fields = trimAlertFields(fields)

	embed := discordEmbed{
		Title:       truncateRunes("✅ "+escapeDiscordMarkdown(subject.Title), discordMaxTitle),
		Description: truncateRunes(description, discordMaxDescription),
		Color:       discordColorResolved,
		Fields:      fields,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	if incidentID != "" {
		embed.Footer = &discordEmbedFooter{
			Text: truncateRunes("Incident: "+incidentID, discordMaxFooter),
		}
	} else {
		// No incident linked yet: keep the alert key so the page can still be
		// matched to the report that follows.
		embed.Footer = alertFooter(subject.AlertKey)
	}
	return embed
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
