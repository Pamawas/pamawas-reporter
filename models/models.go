package models

import (
	"time"
)

// Incident represents a correlated incident from the database
type Incident struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Status           string    `json:"status"`
	StartedAt        time.Time `json:"started_at"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
	Severity         string    `json:"severity,omitempty"`
	AffectedServices []string  `json:"affected_services"`
}

// Evidence represents a piece of evidence from an investigation
type Evidence struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id"`
	Type       string    `json:"type"` // fact, likely_cause, hypothesis, unknown
	Content    string    `json:"content"`
	Source     string    `json:"source"`
	Confidence float64   `json:"confidence"`
}

// Report represents a generated incident report
type Report struct {
	ID        string    `json:"id"`
	IncidentID string   `json:"incident_id"`
	Content   string    `json:"content"`
	SentAt    time.Time `json:"sent_at"`
	Channels  []string  `json:"channels"`
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	LastSent  time.Time `json:"last_sent"`
	Running   bool      `json:"running"`
	Uptime    string    `json:"uptime,omitempty"`
	Version   string    `json:"version,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ReadyResponse represents the readiness check response
type ReadyResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// TriggerResponse represents the manual trigger response
type TriggerResponse struct {
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}