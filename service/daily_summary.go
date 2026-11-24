package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Pamawas/pamawas-reporter/models"
)

// summarizeDay asks the investigator for a short prose summary of the report.
//
// LLM access lives in the investigator service, which owns the model endpoints,
// credentials and prompts; the reporter only ever calls it over HTTP. The call
// is made while rendering, before the message is posted, so the summary is part
// of the digest that goes out rather than arriving as a follow-up.
//
// It returns "" for every failure mode — not configured, unreachable, timeout,
// empty reply — because a daily report that arrives without a narrative is far
// better than one that does not arrive. Callers render the deterministic
// incident lists either way.
func (r *Reporter) summarizeDay(ctx context.Context, report models.Report, incidents []models.Incident) string {
	if r.config.InvestigatorURL == "" || len(incidents) == 0 {
		return ""
	}

	timeout := r.config.SummaryTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload := dailySummaryRequest{
		PeriodStart: report.PeriodStart.Format(time.RFC3339),
		PeriodEnd:   report.PeriodEnd.Format(time.RFC3339),
		Timezone:    report.Timezone,
	}
	for _, inc := range incidents {
		item := dailySummaryIncident{
			Title:     inc.Title,
			Severity:  inc.Severity,
			Status:    inc.Status,
			Affected:  affectedLabel(inc),
			StartedAt: inc.StartedAt.Format(time.RFC3339),
		}
		if !inc.ResolvedAt.IsZero() {
			item.ResolvedAt = inc.ResolvedAt.Format(time.RFC3339)
			item.Duration = inc.ResolvedAt.Sub(inc.StartedAt).Round(time.Second).String()
		}
		payload.Incidents = append(payload.Incidents, item)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to marshal daily summary request")
		return ""
	}

	url := strings.TrimRight(r.config.InvestigatorURL, "/") + "/api/v1/summaries"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create daily summary request")
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if r.config.ServiceToken != "" {
		req.Header.Set("X-Service-Token", r.config.ServiceToken)
	}

	// A dedicated client: the shared httpClient has a 10s timeout, which
	// silently overrode SummaryTimeout and made every summary fail at ~10s
	// even when the budget was 20s.
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("Daily summary request failed; digest continues without it")
		return ""
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close daily summary response body")
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).
			Msg("Investigator returned a non-2xx status for the daily summary; digest continues without it")
		return ""
	}

	var out struct {
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Warn().Err(err).Msg("Failed to decode daily summary response")
		return ""
	}

	summary := strings.TrimSpace(out.Summary)
	if looksLikeSummaryPromptEcho(summary) {
		log.Warn().Str("summary_prefix", truncateSummaryLog(summary)).
			Msg("Investigator summary echoed instructions; using deterministic fallback")
		return deterministicDailySummary(incidents)
	}
	return summary
}

func looksLikeSummaryPromptEcho(summary string) bool {
	lowered := strings.ToLower(strings.Join(strings.Fields(summary), " "))
	if lowered == "" {
		return false
	}
	markers := []string{
		"we need answer",
		"we need to summarize",
		"need produce",
		"the user wants",
		"the user asked",
		"user's request",
		"system prompt",
		"developer message",
		"thinking process",
		"analyze the request",
		"output format",
		"no bullet points",
		"no headings",
		"at most three sentences",
		"use only the facts given",
		"write the summary",
	}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(lowered, marker) {
			hits++
		}
	}
	return hits >= 2 || strings.Contains(lowered, "<think>")
}

func truncateSummaryLog(summary string) string {
	const limit = 160
	if len(summary) <= limit {
		return summary
	}
	return summary[:limit]
}

func deterministicDailySummary(incidents []models.Incident) string {
	open, resolved := 0, 0
	var highestOpen *models.Incident
	for i := range incidents {
		inc := &incidents[i]
		switch inc.Status {
		case "open", "investigating":
			open++
			if highestOpen == nil || severityRank(inc.Severity) > severityRank(highestOpen.Severity) {
				highestOpen = inc
			}
		case "resolved":
			resolved++
		}
	}
	summary := fmt.Sprintf(
		"%d incidents were recorded, %d remain open or under investigation and %d resolved.",
		len(incidents), open, resolved,
	)
	if highestOpen != nil {
		target := affectedLabel(*highestOpen)
		if target != "" {
			return fmt.Sprintf(
				"%s The highest-severity unresolved condition is %s affecting %s.",
				summary, highestOpen.Title, target,
			)
		}
		return fmt.Sprintf(
			"%s The highest-severity unresolved condition is %s.",
			summary, highestOpen.Title,
		)
	}
	return summary
}

// dailySummaryRequest is the fact sheet the investigator summarises. It carries
// structured facts only — no transcripts — so the summary cannot drift into
// inventing detail that was never observed.
type dailySummaryRequest struct {
	PeriodStart string                 `json:"period_start"`
	PeriodEnd   string                 `json:"period_end"`
	Timezone    string                 `json:"timezone"`
	Incidents   []dailySummaryIncident `json:"incidents"`
}

type dailySummaryIncident struct {
	Title      string `json:"title"`
	Severity   string `json:"severity"`
	Status     string `json:"status"`
	Affected   string `json:"affected"`
	StartedAt  string `json:"started_at"`
	ResolvedAt string `json:"resolved_at,omitempty"`
	Duration   string `json:"duration,omitempty"`
}
