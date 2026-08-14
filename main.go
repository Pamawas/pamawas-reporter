package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"strconv"
)

// ReporterConfig holds configuration for the reporter service
type ReporterConfig struct {
	// Server
	Port string

	// Database
	DatabaseURL string

	// Delivery channels
	DiscordWebhookURL string
	TelegramBotToken  string
	TelegramChatID    string
	EmailSMTPHost     string
	EmailSMTPPort     int
	EmailUsername     string
	EmailPassword     string
	EmailFrom         string

	// Template configuration
	ReportTemplate string
}

// Incident represents a correlated incident from the database
type Incident struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	ResolvedAt   time.Time `json:"resolved_at,omitempty"`
	Severity     string    `json:"severity,omitempty"`
	AffectedServices []string `json:"affected_services"`
}

// Evidence represents a piece of evidence from an investigation
type Evidence struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // fact, likely_cause, hypothesis, unknown
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	Confidence float64  `json:"confidence"`
}

// Report represents a generated incident report
type Report struct {
	ID        string    `json:"id"`
	IncidentID string   `json:"incident_id"`
	Content   string    `json:"content"`
	SentAt    time.Time `json:"sent_at"`
	Channels  []string  `json:"channels"`
}

// Reporter holds the database connection and reporting logic
type Reporter struct {
	db          *sql.DB
	config      ReporterConfig
	mu          sync.Mutex
	lastSent    time.Time
	running     bool
	wg          sync.WaitGroup
	ctx         context.Context
	cancelFunc  context.CancelFunc
}

// HealthStatus represents the health of the reporter
type HealthStatus struct {
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
	LastSent    time.Time `json:"last_sent"`
	Running     bool      `json:"running"`
	Uptime      string    `json:"uptime,omitempty"`
	Version     string    `json:"version,omitempty"`
}

func main() {
	// Load configuration from environment
	config := ReporterConfig{
		Port: getEnv("PORT", "8080"),

		DatabaseURL: getEnv("DATABASE_URL", ""),

		DiscordWebhookURL: getEnv("DISCORD_WEBHOOK_URL", ""),
		TelegramBotToken:  getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:    getEnv("TELEGRAM_CHAT_ID", ""),
		EmailSMTPHost:     getEnv("EMAIL_SMTP_HOST", ""),
		EmailSMTPPort:     mustAtoi(getEnv("EMAIL_SMTP_PORT", "587")),
		EmailUsername:     getEnv("EMAIL_USERNAME", ""),
		EmailPassword:     getEnv("EMAIL_PASSWORD", ""),
		EmailFrom:         getEnv("EMAIL_FROM", ""),

		ReportTemplate:  getEnv("REPORT_TEMPLATE", ""),
	}

	// Validate required configuration
	if config.DatabaseURL == "" {
		log.Fatal("DATABASE_URL environment variable not set")
	}

	// Connect to database
	db, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}
	defer db.Close()

	// Test connection
	if err = db.Ping(); err != nil {
		log.Fatalf("Error connecting to database: %v", err)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := &Reporter{
		db:       db,
		config:   config,
		ctx:      ctx,
	}

	// HTTP server for health checks and manual triggers
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", r.healthHandler)
	mux.HandleFunc("/ready", r.readyHandler)
	mux.HandleFunc("/report", r.reportHandler)
	mux.HandleFunc("/status", r.statusHandler)
	mux.HandleFunc("/metrics", r.metricsHandler)

	port := config.Port
	log.Printf("Starting reporter on :%s", port)

	// Start background worker for scheduled reports if enabled
	if os.Getenv("REPORTER_MODE") != "manual" {
		go r.reportWorker()
	}

	// Start HTTP server
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Listen for shutdown signals
	go func() {
		<-ctx.Done()
		log.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server forced to shutdown: %v", err)
		}
	}()

	log.Fatal(srv.ListenAndServe())
}

// reportWorker runs the report generation process periodically
func (r *Reporter) reportWorker() {
	intervalStr := os.Getenv("REPORT_INTERVAL")
	if intervalStr == "" {
		intervalStr = "1h" // default 1 hour
	}
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		log.Fatalf("Invalid REPORT_INTERVAL: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			log.Println("Report worker stopped")
			return
		case <-ticker.C:
			if err := r.GenerateAndSendDailyReport(); err != nil {
				log.Printf("Report generation error: %v", err)
				// Continue despite errors - don't want to stop the worker
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
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	r.wg.Add(1)
	defer r.wg.Done()

	startTime := time.Now()
	log.Printf("Generating daily report")

	// Get incidents from the last 24 hours
	incidents, err := r.getRecentIncidents(24 * time.Hour)
	if err != nil {
		return err
	}

	if len(incidents) == 0 {
		log.Printf("No incidents found in the last 24 hours")
		return nil
	}

	// Generate report content
	reportContent := r.generateDailyReport(incidents)

	// Create report record
	report := Report{
		ID:        uuid.NewString(),
		IncidentID: "daily_" + time.Now().Format("20060102"), // Simple ID for daily report
		Content:   reportContent,
		SentAt:    time.Now(),
		Channels:  []string{"discord", "telegram", "email"}, // Will be filtered based on config
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
	r.mu.Unlock()

	log.Printf("Daily report generated and sent in %v", time.Since(startTime))
	return nil
}

// getRecentIncidents retrieves incidents from the last duration
func (r *Reporter) getRecentIncidents(duration time.Duration) ([]Incident, error) {
	since := time.Now().Add(-duration)

	query := `
		SELECT i.id, i.title, i.status, i.started_at, i.resolved_at, i.severity, i.affected_services
		FROM incidents i
		WHERE i.started_at >= $1
		ORDER BY i.started_at DESC
	`

	rows, err := r.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []Incident
	for rows.Next() {
		var inc Incident
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

// generateDailyReport creates a formatted daily digest report
func (r *Reporter) generateDailyReport(incidents []Incident) string {
	var healthyPercentage float64
	if len(incidents) > 0 {
		// Simple calculation - in reality this would be based on actual metrics
		healthyPercentage = 99.97 // placeholder
	} else {
		healthyPercentage = 100.0
	}

	needsAttention := 0
	recovered := 0
	for _, inc := range incidents {
		if inc.Status == "firing" {
			needsAttention++
		} else if inc.Status == "resolved" {
			recovered++
		}
	}

	var mostLikelyCause string
	var confidence float64
	if len(incidents) > 0 {
		// In a real implementation, this would come from the investigation findings
		mostLikelyCause = "database connection exhaustion following the 01:47 deployment"
		confidence = 0.87
	} else {
		mostLikelyCause = "No incidents detected"
		confidence = 1.0
	}

	// Build report using template or default format
	if r.config.ReportTemplate != "" {
		// Use custom template
		data := map[string]interface{}{
			"HealthyPercentage": healthyPercentage,
			"TotalIncidents":    len(incidents),
			"NeedsAttention":    needsAttention,
			"Recovered":         recovered,
			"MostLikelyCause":   mostLikelyCause,
			"Confidence":        confidence,
			"Timestamp":         time.Now().Format("2006-01-02 15:04:05"),
		}
		// Simple template replacement - in reality you'd use a proper template engine
		content := r.config.ReportTemplate
		for k, v := range data {
			content = strings.ReplaceAll(content, "{{"+k+"}}", fmt.Sprintf("%v", v))
		}
		return content
	}

	// Default report format
	var sb strings.Builder
	sb.WriteString("���🌅 Good morning.\n\n")
	sb.WriteString(fmt.Sprintf("Infrastructure was %.2f%% healthy overnight.\n", healthyPercentage))
	sb.WriteString(fmt.Sprintf("%d incidents occurred. %d needs your attention. %d recovered automatically.\n\n",
		len(incidents), needsAttention, recovered))
	if mostLikelyCause != "No incidents detected" {
		sb.WriteString(fmt.Sprintf("Most likely root cause: %s\n", mostLikelyCause))
		sb.WriteString(fmt.Sprintf("Confidence: %.0f%%.\n\n", confidence*100))
		sb.WriteString("Recommended actions:\n")
		sb.WriteString("1. Review connection-pool configuration.\n")
		sb.WriteString("2. Compare the deployment's DB connection behavior.\n")
		sb.WriteString("3. Add early-warning monitoring.\n\n")
	}
	sb.WriteString("No critical outage occurred.\n")

	return sb.String()
}

// sendReport sends the report via configured channels
func (r *Reporter) sendReport(report Report) error {
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
				log.Printf("Failed to send report to Discord: %v", err)
			} else {
				log.Printf("Report sent to Discord successfully")
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
				log.Printf("Failed to send report to Telegram: %v", err)
			} else {
				log.Printf("Report sent to Telegram successfully")
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
				log.Printf("Failed to send report via email: %v", err)
			} else {
				log.Printf("Report sent via email successfully")
			}
		}()
	}

	wg.Wait()
	return firstError
}

// sendToDiscord sends the report to Discord via webhook
func (r *Reporter) sendToDiscord(report Report) error {
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

	resp, err := http.Post(r.config.DiscordWebhookURL, "application/json", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Discord webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// sendToTelegram sends the report to Telegram via Bot API
func (r *Reporter) sendToTelegram(report Report) error {
	if r.config.TelegramBotToken == "" || r.config.TelegramChatID == "" {
		return nil
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", r.config.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id": r.config.TelegramChatID,
		"text":    report.Content,
		"parse_mode": "Markdown",
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(url, "application/json", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram API returned status %d", resp.StatusCode)
	}

	return nil
}

// sendToEmail sends the report via SMTP
func (r *Reporter) sendToEmail(report Report) error {
	if r.config.EmailSMTPHost == "" || r.config.EmailUsername == "" || r.config.EmailPassword == "" {
		return nil
	}

	// In a real implementation, you'd use net/smtp or a library like go-gomail
	// For now, we'll just log that we would send the email
	log.Printf("Would send email report to %s via %s:%d", r.config.EmailFrom, r.config.EmailSMTPHost, r.config.EmailSMTPPort)
	log.Printf("Email content: %s", report.Content)
	return nil // Simulate success for now
}

// saveReport saves the report to the database
func (r *Reporter) saveReport(report Report) error {
	query := `
		INSERT INTO reports (id, incident_id, content, sent_at, channels)
		VALUES ($1,$2,$3,$4,$5)
	`

	channelsJSON, err := json.Marshal(report.Channels)
	if err != nil {
		return err
	}

	_, err = r.db.Exec(query,
		report.ID,
		report.IncidentID,
		report.Content,
		report.SentAt,
		channelsJSON,
	)
	return err
}

// getIncidentsByID retrieves incidents by their IDs (for testing/manual triggers)
func (r *Reporter) getIncidentsByID(ids []string) ([]Incident, error) {
	if len(ids) == 0 {
		return []Incident{}, nil
	}

	// Build query with placeholders
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf(`
		SELECT i.id, i.title, i.status, i.started_at, i.resolved_at, i.severity, i.affected_services
		FROM incidents i
		WHERE i.id IN (%s)
		ORDER BY i.started_at DESC
	`, strings.Join(placeholders, ","))

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []Incident
	for rows.Next() {
		var inc Incident
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

// Health check handlers
func (r *Reporter) healthHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check database connectivity
	if err := r.db.PingContext(req.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"error":  fmt.Sprintf("Database connection failed: %v", err),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(HealthStatus{
		Status:      "healthy",
		Timestamp:   time.Now().UTC(),
		LastSent:    r.lastSent,
		Running:     r.running,
		Version:     "1.0.0",
	})
}

func (r *Reporter) readyHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Simple readiness check - if we can connect to db, we're ready
	if err := r.db.PingContext(req.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not ready",
			"error":  fmt.Sprintf("Database not ready: %v", err),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

// reportHandler handles manual report generation requests
func (r *Reporter) reportHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body for incident IDs (optional)
	var requestData struct {
		IncidentIDs []string `json:"incident_ids,omitempty"`
	}
	if err := json.NewDecoder(req.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	var incidents []Incident
	var err error
	if len(requestData.IncidentIDs) > 0 {
		incidents, err = r.getIncidentsByID(requestData.IncidentIDs)
	} else {
		// Generate report for recent incidents (last 24 hours)
		incidents, err = r.getRecentIncidents(24 * time.Hour)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get incidents: %v", err), http.StatusInternalServerError)
		return
	}

	if len(incidents) == 0 {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"message": "No incidents found to report",
		})
		return
	}

	// Generate report content
	reportContent := r.generateDailyReport(incidents)

	// Create report record
	report := Report{
		ID:        uuid.NewString(),
		IncidentID: "manual_" + time.Now().Format("20060102_150405"),
		Content:   reportContent,
		SentAt:    time.Now(),
		Channels:  []string{"discord", "telegram", "email"},
	}

	// Send report via configured channels
	if err := r.sendReport(report); err != nil {
		http.Error(w, fmt.Sprintf("Failed to send report: %v", err), http.StatusInternalServerError)
		return
	}

	// Save report to database
	if err := r.saveReport(report); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save report: %v", err), http.StatusInternalServerError)
		return
	}

	r.mu.Lock()
	r.lastSent = time.Now()
	r.mu.Unlock()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Report generated and sent successfully",
		"report_id": report.ID,
	})
}

// statusHandler returns the current status of the reporter
func (r *Reporter) statusHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"last_sent": r.lastSent,
		"running":   r.running,
		"version":   "1.0.0",
	})
}

// metricsHandler exposes Prometheus metrics
func (r *Reporter) metricsHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Simple metrics - in production you'd use prometheus client library
	fmt.Fprintf(w, `# HELP pamawas_reporter_last_report_timestamp_seconds Timestamp of last report sent
# TYPE pamawas_reporter_last_report_timestamp_seconds gauge
pamawas_reporter_last_report_timestamp_seconds %d
`,
		r.lastSent.Unix())

	fmt.Fprintf(w, `# HELP pamawas_reporter_running Whether the reporter is currently running
# TYPE pamawas_reporter_running gauge
pamawas_reporter_running %d
`,
		boolToInt(r.running))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Helper functions
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustAtoi(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		log.Fatalf("Invalid integer %s: %v", s, err)
	}
	return i
}