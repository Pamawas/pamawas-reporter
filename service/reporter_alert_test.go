package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Pamawas/pamawas-reporter/models"
)

// newAlertTestReporter wires a reporter against a fake Discord webhook so the
// delivery path is exercised end to end without real credentials.
func newAlertTestReporter(t *testing.T, threshold string) (*Reporter, sqlmock.Sqlmock, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Logf("failed to drain body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	r, mock := newMockReporter(t, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		HighSeverityThreshold: threshold,
	})
	return r, mock, &calls
}

func TestSendAlertSkipsBelowThreshold(t *testing.T) {
	r, _, calls := newAlertTestReporter(t, "high")

	resp, err := r.SendAlert(context.Background(), models.AlertPayload{
		Title:       "Warning alert",
		Severity:    "warning",
		Environment: "production",
		DedupKey:    "grafana:warn1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "severity_below_threshold" {
		t.Fatalf("expected below-threshold skip, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("delivery attempted %d times for below-threshold severity", got)
	}
}

func TestSendAlertOneShotClaim(t *testing.T) {
	r, mock, calls := newAlertTestReporter(t, "high")

	// Alert details come from the event, so no incident lookup is needed.
	payload := models.AlertPayload{
		Title:       "Critical alert",
		Severity:    "critical",
		Environment: "production",
		StartedAt:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		DedupKey:    "grafana:abc123",
	}

	// First fire claims the condition.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))

	resp, err := r.SendAlert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Sent {
		t.Fatalf("expected alert sent, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected exactly 1 delivery, got %d", got)
	}

	// A Grafana re-fire finds the claim already taken.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnError(sql.ErrNoRows)

	resp, err = r.SendAlert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error on re-fire: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "already_notified" {
		t.Fatalf("expected already_notified skip, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("re-fire produced a duplicate delivery (calls=%d)", got)
	}
}

func TestSendResolveAlertGatesOnSameThreshold(t *testing.T) {
	r, _, calls := newAlertTestReporter(t, "high")

	resp, err := r.SendResolveAlert(context.Background(), models.AlertPayload{
		Title:       "Warning alert",
		Severity:    "warning",
		Environment: "production",
		DedupKey:    "grafana:warn1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "severity_below_threshold" {
		t.Fatalf("expected resolve skip on low severity, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("resolve delivery attempted %d times for below-threshold severity", got)
	}
}

// TestAlertPathsUseEmbeds verifies both the firing alert and the resolved
// alert render as bordered embeds, not plain text, and carry the right bar
// color (severity-colored when firing, green once resolved).
func TestAlertPathsUseEmbeds(t *testing.T) {
	var payloads []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		payloads = append(payloads, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r, mock := newMockReporter(t, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		HighSeverityThreshold: "high",
	})

	payload := models.AlertPayload{
		Title:       "Monitoring Target Down",
		Severity:    "critical",
		Environment: "production",
		StartedAt:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		DedupKey:    "grafana:down1",
	}

	// Firing alert: claim succeeds, then delivers.
	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))
	resp, err := r.SendAlert(context.Background(), payload)
	if err != nil || !resp.Sent {
		t.Fatalf("alert failed: resp=%+v err=%v", resp, err)
	}

	// Resolved alert: releases the claim, then delivers.
	mock.ExpectExec("DELETE FROM idempotency_records").
		WillReturnResult(sqlmock.NewResult(0, 1))
	resp, err = r.SendResolveAlert(context.Background(), payload)
	if err != nil || !resp.Sent {
		t.Fatalf("resolved alert failed: resp=%+v err=%v", resp, err)
	}

	if len(payloads) != 2 {
		t.Fatalf("expected 2 webhook calls, got %d", len(payloads))
	}
	for i, p := range payloads {
		if _, hasContent := p["content"]; hasContent {
			t.Errorf("payload %d used plain content instead of embeds", i)
		}
		embeds, ok := p["embeds"].([]interface{})
		if !ok || len(embeds) != 1 {
			t.Fatalf("payload %d: expected exactly 1 embed, got %#v", i, p["embeds"])
		}
		embed := embeds[0].(map[string]interface{})
		if i == 0 {
			if embed["color"] != float64(0xE74C3C) {
				t.Errorf("firing alert bar should be critical red, got %v", embed["color"])
			}
			if !strings.Contains(embed["title"].(string), "🚨") {
				t.Errorf("firing alert title should carry the alarm marker: %v", embed["title"])
			}
		} else {
			if embed["color"] != float64(0x2ECC71) {
				t.Errorf("resolved alert bar should be green, got %v", embed["color"])
			}
			if !strings.Contains(embed["title"].(string), "✅") {
				t.Errorf("resolved alert title should carry the check marker: %v", embed["title"])
			}
		}
	}
}

// TestAlertNamesTheTargetNotTheSender is the regression test for ambiguous
// alerts: a Grafana alert names no service, and ingest used to fill the gap
// with the literal "grafana" — the sender — so every card read
// "Services: grafana" and never said what was actually down. The card must
// name the target (Grafana's `instance`) and the job instead.
func TestAlertNamesTheTargetNotTheSender(t *testing.T) {
	var payloads []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		payloads = append(payloads, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r, mock := newMockReporter(t, ReporterConfig{
		DiscordWebhookURL:     server.URL,
		HighSeverityThreshold: "high",
	})

	payload := models.AlertPayload{
		Title:       "Monitoring Target Down",
		Severity:    "critical",
		Environment: "production",
		Target:      "last-labs",
		Job:         "docker_cadvisor",
		StartedAt:   time.Now().Add(-time.Minute).Format(time.RFC3339),
		DedupKey:    "grafana:24188862b3fad691",
	}

	mock.ExpectQuery("INSERT INTO idempotency_records").
		WillReturnRows(sqlmock.NewRows([]string{"key_hash"}).AddRow("hash"))
	if _, err := r.SendAlert(context.Background(), payload); err != nil {
		t.Fatalf("alert failed: %v", err)
	}

	if len(payloads) != 1 {
		t.Fatalf("expected 1 webhook call, got %d", len(payloads))
	}
	embeds, ok := payloads[0]["embeds"].([]interface{})
	if !ok || len(embeds) != 1 {
		t.Fatalf("expected exactly 1 embed, got %#v", payloads[0]["embeds"])
	}
	embed, ok := embeds[0].(map[string]interface{})
	if !ok {
		t.Fatalf("embed is not an object: %#v", embeds[0])
	}

	got := map[string]string{}
	fields, ok := embed["fields"].([]interface{})
	if !ok {
		t.Fatalf("embed has no fields: %#v", embed)
	}
	for _, f := range fields {
		field, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		name, hasName := field["name"].(string)
		value, hasValue := field["value"].(string)
		if hasName && hasValue {
			got[name] = value
		}
	}

	if got["Target"] != "last-labs" {
		t.Errorf("card must name the target, got %q (fields: %v)", got["Target"], got)
	}
	// Escaped: `docker_cadvisor` carries one underscore, which Discord reads as
	// an italic delimiter and consumes.
	if got["Job"] != `docker\_cadvisor` {
		t.Errorf("card must name the scrape job, got %q", got["Job"])
	}
	if _, hasServices := got["Services"]; hasServices {
		t.Errorf("card must not repeat a derived service, got %q", got["Services"])
	}
}

// TestFindingsCiteTheirSourceAndSeparateActions verifies the card answers two
// questions a reader asks about every finding: where did this come from
// (metrics, logs, inference) and what should I do about it. Recommendations are
// rendered as actions, not mixed into the evidence as more claims.
func TestFindingsCiteTheirSourceAndSeparateActions(t *testing.T) {
	evList := []models.Evidence{
		{Type: "fact", Content: "node_load1 hit 16.37", Source: "prometheus",
			SourceDetail: `node_load1{instance="koba-labs"}`, Confidence: 0.95},
		{Type: "fact", Content: "netbird-agent logged stream disconnects", Source: "loki",
			SourceDetail: "kubernetes_container_name=netbird-agent", Confidence: 0.95},
		{Type: "likely_cause", Content: "netbird-agent reconnects caused the load", Source: "llm",
			Confidence: 0.85},
		{Type: "recommendation", Content: "Pin netbird-agent to a stable relay", Source: "llm",
			Confidence: 0.8},
	}

	out := formatFindingsForEmbed(evList, discordMaxDescription)

	// Underscores are escaped: Discord treats a bare one as an italic
	// delimiter, which deletes the character and italicises the rest of the
	// line. `node_load1` reached the reader as "node" followed by italic "load1".
	if !strings.Contains(out, `Prometheus: node\_load1{instance="koba-labs"}`) {
		t.Errorf("metric finding must cite Prometheus and the query, got:\n%s", out)
	}
	if !strings.Contains(out, `Loki: kubernetes\_container\_name=netbird-agent`) {
		t.Errorf("log finding must cite Loki and the stream, got:\n%s", out)
	}
	if !strings.Contains(out, "_[inferred]_") {
		t.Errorf("synthesized finding must be marked as inference, got:\n%s", out)
	}
	if !strings.Contains(out, "**🛠 Suggested Actions**") {
		t.Errorf("recommendations must have their own section, got:\n%s", out)
	}
	if !strings.Contains(out, "1. Pin netbird-agent to a stable relay") {
		t.Errorf("recommendation text missing, got:\n%s", out)
	}
	// The action must not appear as an evidence line as well.
	if strings.Contains(out, "**RECOMMENDATION**") {
		t.Errorf("recommendation leaked into the findings list:\n%s", out)
	}
}

// TestTriageOrderPutsTheAnswerFirst pins the reading order of a single-incident
// card. An on-call engineer opens this while something is broken: the likely
// cause and the actions come before the supporting evidence, and the
// highest-confidence cause is the one surfaced.
func TestTriageOrderPutsTheAnswerFirst(t *testing.T) {
	evList := []models.Evidence{
		{ID: "e1", Type: "fact", Content: "pod restarted", Source: "loki", Confidence: 0.9},
		{ID: "e2", Type: "likely_cause", Content: "rolling deployment replaced the pod", Source: "llm", Confidence: 0.95},
		{ID: "e3", Type: "likely_cause", Content: "weaker theory", Source: "llm", Confidence: 0.55},
		{ID: "e4", Type: "recommendation", Content: "Verify the new pod is serving", Source: "llm", Confidence: 0.8},
	}

	cause, actions, evidence := triageFindings(evList)

	if cause == nil || cause.ID != "e2" {
		t.Fatalf("expected the highest-confidence likely cause (e2), got %+v", cause)
	}
	if len(actions) != 1 || actions[0].ID != "e4" {
		t.Fatalf("expected the recommendation to be split out, got %+v", actions)
	}
	// The surfaced cause must not be repeated in the evidence list, and the
	// weaker cause must still be shown.
	if len(evidence) != 2 {
		t.Fatalf("expected 2 supporting items, got %d: %+v", len(evidence), evidence)
	}
	for _, ev := range evidence {
		if ev.ID == "e2" {
			t.Error("surfaced cause was duplicated in supporting evidence")
		}
	}
}

// TestSingleIncidentCardReadsCauseThenActionsThenEvidence verifies the rendered
// order, since that order is the whole point of the card.
func TestSingleIncidentCardReadsCauseThenActionsThenEvidence(t *testing.T) {
	evList := []models.Evidence{
		{ID: "e1", Type: "fact", Content: "FACT BODY", Source: "loki", Confidence: 0.9},
		{ID: "e2", Type: "likely_cause", Content: "CAUSE BODY", Source: "llm", Confidence: 0.95},
		{ID: "e4", Type: "recommendation", Content: "ACTION BODY", Source: "llm", Confidence: 0.8},
	}

	embed := buildSingleIncidentEmbedForTest(models.Incident{
		ID: "inc_1", Title: "Monitoring Target Down", Status: "open", Severity: "critical",
		StartedAt: time.Now().Add(-time.Hour),
	}, evList)

	desc := embed.Description
	causeAt := strings.Index(desc, "Likely cause")
	actionAt := strings.Index(desc, "Suggested actions")
	evidenceAt := strings.Index(desc, "Supporting evidence")
	if causeAt < 0 || actionAt < 0 || evidenceAt < 0 {
		t.Fatalf("missing a section; got:\n%s", desc)
	}
	if !(causeAt < actionAt && actionAt < evidenceAt) {
		t.Fatalf("sections out of triage order (cause=%d actions=%d evidence=%d):\n%s",
			causeAt, actionAt, evidenceAt, desc)
	}
}

// buildSingleIncidentEmbedForTest adapts the method for tests that do not need
// a database-backed reporter.
func buildSingleIncidentEmbedForTest(inc models.Incident, evList []models.Evidence) discordEmbed {
	r := &Reporter{config: ReporterConfig{HighSeverityThreshold: "high"}}
	report := models.Report{TemplateVersion: "pamawas-report-v2", GeneratedAt: time.Now()}
	return r.buildSingleIncidentEmbed(report, inc, evList, models.InclusionReasonHighSeverityImmediate)
}

// TestRepeatedResolveNotifiesOnce is the regression test for duplicate
// "alert resolved" cards. Grafana re-sends `resolved` every evaluation
// interval, exactly as it re-sends `firing`. The resolve consumes the firing
// claim, so the first one notifies and every repeat finds nothing outstanding.
func TestRepeatedResolveNotifiesOnce(t *testing.T) {
	r, mock, calls := newAlertTestReporter(t, "high")

	payload := models.AlertPayload{
		Title:       "Monitoring Target Down",
		Severity:    "critical",
		Environment: "production",
		Target:      "last-labs",
		Job:         "docker_cadvisor",
		StartedAt:   time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
		DedupKey:    "grafana:24188862b3fad691",
	}

	// The firing alert's claim is still outstanding: this resolve owns it.
	mock.ExpectExec("DELETE FROM idempotency_records").
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := r.SendResolveAlert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Sent {
		t.Fatalf("expected the recovery to be announced, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 delivery, got %d", got)
	}

	// Grafana repeats the resolved notification; the claim is already consumed.
	mock.ExpectExec("DELETE FROM idempotency_records").
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err = r.SendResolveAlert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error on repeat: %v", err)
	}
	if resp.Sent || !resp.Skipped || resp.Reason != "no_outstanding_alert" {
		t.Fatalf("expected no_outstanding_alert skip, got %+v", resp)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("repeat resolve produced a duplicate card (calls=%d)", got)
	}
}

// TestResolvedCardFoldsDowntimeIntoTheSummary verifies the card keeps a clean
// three-column grid: Downtime as a seventh field wrapped alone onto its own row.
func TestResolvedCardFoldsDowntimeIntoTheSummary(t *testing.T) {
	started := time.Date(2026, 11, 15, 13, 37, 20, 0, time.UTC)
	embed := buildResolvedAlertEmbed(alertSubject{
		Title: "Monitoring Target Down", Severity: "critical",
		Environment: "production", Target: "last-labs", Job: "docker_cadvisor",
		StartedAt: started,
	}, started.Add(5*time.Minute+34*time.Second), "inc_abc123")

	if !strings.Contains(embed.Description, "5m34s") {
		t.Errorf("downtime must be stated in the summary, got %q", embed.Description)
	}
	if strings.Contains(embed.Description, "no longer firing") {
		t.Errorf("description repeats the title and Resolved field: %q", embed.Description)
	}
	for _, f := range embed.Fields {
		if f.Name == "Downtime" {
			t.Error("downtime must not be a field; it wraps alone onto its own row")
		}
	}
	// 5 identity fields + Resolved = two full rows of three.
	if len(embed.Fields) != 6 {
		t.Errorf("expected 6 fields for a clean grid, got %d: %+v", len(embed.Fields), embed.Fields)
	}
}

// TestFiringCardStatesFiringDuration verifies the card carries the urgency
// signal an on-call reader needs: a 30-second blip and a 40-minute outage read
// identically from a start timestamp alone.
func TestFiringCardStatesFiringDuration(t *testing.T) {
	embed := buildAlertEmbed(alertSubject{
		Title: "Monitoring Target Down", Severity: "critical", Environment: "production",
		Target: "last-labs", Job: "docker_cadvisor",
		StartedAt: time.Now().Add(-12 * time.Minute),
	})

	var firingFor string
	for _, f := range embed.Fields {
		if f.Name == "Firing for" {
			firingFor = f.Value
		}
	}
	if !strings.HasPrefix(firingFor, "12m") {
		t.Errorf("expected a firing duration near 12m, got %q", firingFor)
	}
	// Six inline fields fill two rows of three; the duration must not dangle
	// alone on a third row.
	if len(embed.Fields)%3 != 0 {
		t.Errorf("expected a full grid of inline fields, got %d", len(embed.Fields))
	}
}

// TestRecurrenceCountsPriorOccurrences verifies the counting rule: prior only,
// within 24h, for the same environment + affected entity + alert name. Counting
// from `started_at < this alert's start` is what makes it correct on both the
// firing path (no incident exists yet) and the resolved path (the current
// incident started exactly then, so it is excluded).
func TestRecurrenceCountsPriorOccurrences(t *testing.T) {
	r, mock := newMockReporter(t, ReporterConfig{HighSeverityThreshold: "high"})
	started := time.Date(2026, 11, 15, 13, 37, 20, 0, time.UTC)
	subject := alertSubject{
		Title: "Monitoring Target Down", Environment: "production",
		Target: "last-labs", StartedAt: started,
	}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM incidents").
		WithArgs("production", "last-labs", "Monitoring Target Down", started).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	if got := r.countPriorOccurrences(context.Background(), subject); got != 3 {
		t.Fatalf("expected 3 prior occurrences, got %d", got)
	}
}

func TestRecurrenceSkipsWithoutAnIdentity(t *testing.T) {
	r, _ := newMockReporter(t, ReporterConfig{HighSeverityThreshold: "high"})
	// No target and no service: there is nothing to count, and the query must
	// not run at all (no sqlmock expectation is set).
	if got := r.countPriorOccurrences(context.Background(), alertSubject{
		Title: "Kube Job Failed", Environment: "production", StartedAt: time.Now(),
	}); got != 0 {
		t.Fatalf("expected no count without an affected entity, got %d", got)
	}
}

// TestRecurrenceRendersOnBothCards verifies the count reaches the reader on the
// firing page and on the recovery, and that a first occurrence stays quiet.
func TestRecurrenceRendersOnBothCards(t *testing.T) {
	started := time.Now().Add(-3 * time.Minute)
	base := alertSubject{
		Title: "Monitoring Target Down", Severity: "critical",
		Environment: "production", Target: "last-labs", Job: "docker_cadvisor",
		StartedAt: started, AlertKey: "grafana:abc",
	}

	first := buildAlertEmbed(base)
	if strings.Contains(first.Description, "occurrence") {
		t.Errorf("a first occurrence should not be labelled as recurring: %q", first.Description)
	}

	base.PriorOccurrences = 3
	recurring := buildAlertEmbed(base)
	if !strings.Contains(recurring.Description, "4th occurrence") {
		t.Errorf("expected '4th occurrence', got %q", recurring.Description)
	}

	resolved := buildResolvedAlertEmbed(base, started.Add(5*time.Minute), "inc_1")
	if !strings.Contains(resolved.Description, "4th occurrence") {
		t.Errorf("recovery must carry the recurrence too, got %q", resolved.Description)
	}
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd", 24: "24th", 101: "101st",
	}
	for in, want := range cases {
		if got := ordinal(in); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestReportCardsNameTheirReport pins provenance in the title.
//
// A bare "✅ RAM" card was indistinguishable from an immediate alert card, so a
// daily report containing a `warning` incident read like a warning alert that
// had bypassed HIGH_SEVERITY_THRESHOLD. The gate was working: those cards come
// from the scheduled report, which covers every incident regardless of
// severity. The title now says which it is.
func TestReportCardsNameTheirReport(t *testing.T) {
	inc := models.Incident{
		ID: "inc_1", Title: "CPU", Status: "resolved", Severity: "warning",
		Environment: "production", AffectedServices: []string{"kube-2"},
		StartedAt: time.Now().Add(-5 * time.Minute), ResolvedAt: time.Now(),
	}

	daily := buildIncidentEmbed(models.Report{ReportType: "daily", Timezone: "UTC"}, inc, nil, "newly_started")
	if !strings.HasPrefix(daily.Title, "📅 Daily Report") {
		t.Errorf("daily card must name the daily report, got %q", daily.Title)
	}
	if !strings.Contains(daily.Title, "CPU") {
		t.Errorf("title must still identify the incident, got %q", daily.Title)
	}

	high := buildIncidentEmbed(models.Report{ReportType: "high_severity", Timezone: "UTC"}, inc, nil, "newly_started")
	if !strings.HasPrefix(high.Title, "🚨 High-Severity Report") {
		t.Errorf("high-severity card must name its report, got %q", high.Title)
	}
}

// TestSummarizeDayCallsTheInvestigator verifies the reporter asks the
// investigator for the prose summary. All LLM access lives there; the reporter
// holds no model credentials.
func TestSummarizeDayCallsTheInvestigator(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"summary":"One target briefly down; all recovered."}`))
	}))
	defer server.Close()

	r, _ := newMockReporter(t, ReporterConfig{
		InvestigatorURL:       server.URL,
		SummaryTimeout:        2 * time.Second,
		HighSeverityThreshold: "high",
	})

	summary := r.summarizeDay(context.Background(), models.Report{
		PeriodStart: time.Now().Add(-24 * time.Hour), PeriodEnd: time.Now(), Timezone: "UTC",
	}, []models.Incident{{Title: "RAM", Severity: "warning", Status: "resolved",
		AffectedServices: []string{"koba-labs"}, StartedAt: time.Now().Add(-time.Hour),
		ResolvedAt: time.Now()}})

	if gotPath != "/api/v1/summaries" {
		t.Errorf("expected the investigator summary endpoint, got %q", gotPath)
	}
	if summary != "One target briefly down; all recovered." {
		t.Errorf("unexpected summary %q", summary)
	}
}

// TestSummarizeDayDegradesQuietly verifies the digest never depends on the
// summary: an unreachable or failing investigator yields "", and the report
// still goes out with its deterministic lists.
func TestSummarizeDayDegradesQuietly(t *testing.T) {
	incidents := []models.Incident{{Title: "RAM", Severity: "warning", Status: "resolved"}}
	report := models.Report{Timezone: "UTC"}

	// Not configured at all.
	r, _ := newMockReporter(t, ReporterConfig{HighSeverityThreshold: "high"})
	if got := r.summarizeDay(context.Background(), report, incidents); got != "" {
		t.Errorf("expected no summary when unconfigured, got %q", got)
	}

	// Configured but unreachable.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	r2, _ := newMockReporter(t, ReporterConfig{
		InvestigatorURL: failing.URL, SummaryTimeout: 2 * time.Second,
	})
	if got := r2.summarizeDay(context.Background(), report, incidents); got != "" {
		t.Errorf("expected no summary on a 5xx, got %q", got)
	}

	// No incidents: nothing to summarise, and no call should be made.
	r3, _ := newMockReporter(t, ReporterConfig{InvestigatorURL: failing.URL})
	if got := r3.summarizeDay(context.Background(), report, nil); got != "" {
		t.Errorf("expected no summary without incidents, got %q", got)
	}
}

// TestHypothesisIsNotPresentedAsLikelyCause pins the honesty of the top line.
// A hypothesis is the investigator admitting it did not converge; showing it
// under "Likely cause" with no qualifier tells a paged engineer the opposite
// of what the evidence supports.
func TestHypothesisIsNotPresentedAsLikelyCause(t *testing.T) {
	inc := models.Incident{
		ID: "inc_1", Title: "Monitoring Target Down", Status: "open", Severity: "critical",
		Environment: "production", AffectedServices: []string{"last-labs"},
		StartedAt: time.Now().Add(-10 * time.Minute),
	}
	r := &Reporter{}

	hypothesisCard := r.buildSingleIncidentEmbed(
		models.Report{ReportType: "high_severity", Timezone: "UTC"},
		inc,
		[]models.Evidence{{Type: "hypothesis", Content: "The node may be overloaded.", Confidence: 0.3}},
		"newly_started",
	)
	if strings.Contains(hypothesisCard.Description, "Likely cause") {
		t.Errorf("a hypothesis must not be labelled a likely cause:\n%s", hypothesisCard.Description)
	}
	if !strings.Contains(hypothesisCard.Description, "Possible cause") {
		t.Errorf("hypothesis card should say possible cause:\n%s", hypothesisCard.Description)
	}
	if !strings.Contains(hypothesisCard.Description, "30%") {
		t.Errorf("hypothesis card should state its confidence:\n%s", hypothesisCard.Description)
	}

	factCard := r.buildSingleIncidentEmbed(
		models.Report{ReportType: "high_severity", Timezone: "UTC"},
		inc,
		[]models.Evidence{{Type: "likely_cause", Content: "The node ran out of memory.", Confidence: 0.9}},
		"newly_started",
	)
	if !strings.Contains(factCard.Description, "Likely cause") {
		t.Errorf("a likely_cause should keep its label:\n%s", factCard.Description)
	}
}

// TestEvidenceUnderscoresAreEscaped pins the rendering bug a reader spotted in
// a delivered card: "_[incident history: get_related_incidents(...)]" showed a
// literal leading underscore, and "docker_cluster" arrived as "dockercluster".
//
// Discord uses a single underscore as its italic delimiter. In machine text —
// metric names, LogQL selectors, tool names — every one of them silently
// disappeared and toggled italics for the remainder of the line.
func TestEvidenceUnderscoresAreEscaped(t *testing.T) {
	ev := models.Evidence{
		Type:         "fact",
		Content:      "All targets on instance last-labs dropped to up=0",
		Source:       "incidents",
		SourceDetail: "get_related_incidents(service='last-labs', symptom_keywords=['Monitoring Target Down'])",
		Confidence:   1.0,
	}

	out := formatFindingsForEmbed([]models.Evidence{ev}, discordMaxDescription)

	if strings.Contains(out, "symptom_keywords") {
		t.Errorf("an unescaped underscore survived and would be eaten by Discord:\n%s", out)
	}
	if !strings.Contains(out, `symptom\_keywords`) {
		t.Errorf("underscore must be escaped:\n%s", out)
	}
	// The italic marker this package emits must survive so the suffix still
	// renders as a citation rather than as literal punctuation.
	if !strings.Contains(out, " _[incident history:") || !strings.HasSuffix(strings.TrimSpace(out), "]_") {
		t.Errorf("source citation must keep its italic markers:\n%s", out)
	}
}

// TestSingleIncidentCardLeadsWithWhatIsDown pins the reading order an on-call
// engineer asked for: what is broken, then the findings.
//
// Discord renders the description above the embed's fields, so the incident
// identity has to lead the description. Putting it in fields alone placed
// "Affected: last-labs" below a paragraph of analysis.
func TestSingleIncidentCardLeadsWithWhatIsDown(t *testing.T) {
	started := time.Now().Add(-52 * time.Minute)
	inc := models.Incident{
		ID: "inc_1", Title: "Monitoring Target Down", Status: "open", Severity: "critical",
		Environment: "production", AffectedServices: []string{"last-labs"}, StartedAt: started,
	}
	r := &Reporter{}

	card := r.buildSingleIncidentEmbed(
		models.Report{ReportType: "high_severity", Timezone: "UTC", GeneratedAt: time.Now()},
		inc,
		[]models.Evidence{{Type: "likely_cause", Content: "netbird tunnel dropped", Source: "llm", Confidence: 0.85}},
		"newly_started",
	)

	lines := strings.Split(card.Description, "\n")
	if len(lines) < 2 {
		t.Fatalf("card description too short:\n%s", card.Description)
	}
	// First line: what and where.
	if !strings.Contains(lines[0], "Monitoring Target Down") || !strings.Contains(lines[0], "last-labs") {
		t.Errorf("first line must name what is down and where, got %q", lines[0])
	}
	if !strings.Contains(strings.ToUpper(lines[0]), "CRITICAL") {
		t.Errorf("first line must state severity, got %q", lines[0])
	}
	// Second line: still down, and for how long.
	if !strings.Contains(lines[1], "Still down") || !strings.Contains(lines[1], "52m") {
		t.Errorf("second line must state that it is still down and for how long, got %q", lines[1])
	}
	// Findings follow.
	headlineIdx := strings.Index(card.Description, "Monitoring Target Down")
	causeIdx := strings.Index(card.Description, "Likely cause")
	if causeIdx < headlineIdx {
		t.Errorf("findings must come after the status headline:\n%s", card.Description)
	}

	// The duplicated status fields are gone; the operational extras remain.
	for _, f := range card.Fields {
		switch f.Name {
		case "Status", "Severity", "Environment", "Affected":
			t.Errorf("field %q duplicates the headline", f.Name)
		}
	}
	names := make([]string, 0, len(card.Fields))
	for _, f := range card.Fields {
		names = append(names, f.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "Incident ID") {
		t.Errorf("incident ID must survive for follow-up, got %v", names)
	}
}

func TestFormatElapsedReadsLikeATriageClock(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Second:                "5s",
		4*time.Minute + 12*time.Second: "4m12s",
		52 * time.Minute:               "52m", // not "52m00s": a timer, not a duration
		2*time.Hour + 5*time.Minute:    "2h05m",
		3 * time.Hour:                  "3h",
		26 * time.Hour:                 "1d2h",
		-5 * time.Second:               "0s",
	}
	for in, want := range cases {
		if got := formatElapsed(in); got != want {
			t.Errorf("formatElapsed(%v) = %q, want %q", in, got, want)
		}
	}
}
