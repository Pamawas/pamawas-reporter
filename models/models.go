package models

import (
	"encoding/json"
	"time"
)

// Report status constants
const (
	ReportStatusGenerated         = "generated"
	ReportStatusPartiallyDelivered = "partially_delivered"
	ReportStatusDelivered         = "delivered"
	ReportStatusDeliveryFailed    = "delivery_failed"
)

// Delivery status constants
const (
	DeliveryStatusPending     = "pending"
	DeliveryStatusSending     = "sending"
	DeliveryStatusSent        = "sent"
	DeliveryStatusRetryable   = "retryable"
	DeliveryStatusFailedTerminal = "failed_terminal"
)

// Inclusion reason constants
const (
	InclusionReasonNewlyStarted        = "newly_started"
	InclusionReasonResolvedDuring      = "resolved_during"
	InclusionReasonOngoing             = "ongoing"
	InclusionReasonHighSeverityImmediate = "high_severity_immediate"
)

// Report represents a generated report
type Report struct {
	ID              string    `json:"id"`
	RequestID       string    `json:"request_id"`
	ReportType      string    `json:"report_type"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	Timezone        string    `json:"timezone"`
	TemplateVersion string    `json:"template_version"`
	Content         string    `json:"content"`
	GeneratedAt     time.Time `json:"generated_at"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	IncidentID      string    `json:"incident_id,omitempty"` // legacy compatibility
	Channels        []string  `json:"channels,omitempty"`     // legacy compatibility
	SentAt          time.Time `json:"sent_at,omitempty"`      // legacy compatibility
}

// ReportPayload represents the payload sent from scheduler to reporter
type ReportPayload struct {
	ContractVersion int      `json:"contract_version"`
	RequestID       string   `json:"request_id"`
	ReportType      string   `json:"report_type"`
	PeriodStart     string   `json:"period_start"` // RFC3339
	PeriodEnd       string   `json:"period_end"`   // RFC3339
	Timezone        string   `json:"timezone"`
	IncidentIDs     []string `json:"incident_ids"`
}

// ReportResponse represents the response from reporter to scheduler
type ReportResponse struct {
	ReportID string `json:"report_id"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

// Incident represents an incident
type Incident struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Status           string    `json:"status"`
	StartedAt        time.Time `json:"started_at"`
	LastEventAt      time.Time `json:"last_event_at"`
	ResolvedAt       time.Time `json:"resolved_at,omitempty"`
	Severity         string    `json:"severity"`
	Environment      string    `json:"environment"`
	AffectedServices []string  `json:"affected_services"`
	CorrelationPolicy string   `json:"correlation_policy"`
	CorrelationVersion int      `json:"correlation_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Evidence represents a piece of investigation evidence
type Evidence struct {
	ID                 string   `json:"id"`
	IncidentID         string   `json:"incident_id"`
	RunID              string   `json:"run_id"`
	Type               string   `json:"type"`
	Content            string   `json:"content"`
	Source             string   `json:"source"`
	Confidence         float64  `json:"confidence"`
	Supports           []string `json:"supports_evidence,omitempty"`
	Contradicts        []string `json:"contradicts_evidence,omitempty"`
	Ordinal            int      `json:"ordinal"`
	CreatedAt          time.Time `json:"created_at"`
}

// DeliveryAttempt represents a delivery attempt for a report
type DeliveryAttempt struct {
	ID              string     `json:"id"`
	ReportID        string     `json:"report_id"`
	Channel         string     `json:"channel"`
	DestinationKey  string     `json:"destination_key"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	ProviderMessageID string   `json:"provider_message_id,omitempty"`
	SafeErrorCode   string     `json:"safe_error_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	LastSent  time.Time `json:"last_sent,omitempty"`
	Running   bool      `json:"running,omitempty"`
	Version   string    `json:"version,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ReadyResponse represents the readiness check response
type ReadyResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// TriggerResponse represents the response to a manual trigger
type TriggerResponse struct {
	Message string `json:"message"`
}

// ErrorResponse represents the error envelope
type ErrorResponse struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details []ErrorDetail  `json:"details,omitempty"`
}

// ErrorDetail represents a single error detail
type ErrorDetail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ReportRequest represents a report request
type ReportRequest struct {
	ID              string     `json:"id"`
	RequestType     string     `json:"request_type"`
	PeriodStart     time.Time  `json:"period_start"`
	PeriodEnd       time.Time  `json:"period_end"`
	Timezone        string     `json:"timezone"`
	IdempotencyHash string     `json:"idempotency_hash"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	SafeErrorCode   string     `json:"safe_error_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ReportIncident represents the link between a report and an incident
type ReportIncident struct {
	ReportID       string `json:"report_id"`
	IncidentID     string `json:"incident_id"`
	InclusionReason string `json:"inclusion_reason"`
}

// InvestigationRun represents an investigation run
type InvestigationRun struct {
	ID               string     `json:"id"`
	IncidentID       string     `json:"incident_id"`
	RequestKeyHash   string     `json:"request_key_hash"`
	Status           string     `json:"status"`
	ModelProvider    string     `json:"model_provider"`
	ModelName        string     `json:"model_name"`
	PromptVersion    string     `json:"prompt_version"`
	ToolContract     int        `json:"tool_contract"`
	MaxToolCalls     int        `json:"max_tool_calls"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	SafeErrorCode    string     `json:"safe_error_code,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ToolExecution represents a tool execution within an investigation run
type ToolExecution struct {
	ID               string          `json:"id"`
	RunID            string          `json:"run_id"`
	SequenceNo       int             `json:"sequence_no"`
	ToolName         string          `json:"tool_name"`
	ArgumentsRedacted json.RawMessage `json:"arguments_redacted"`
	ResultSummary    json.RawMessage `json:"result_summary"`
	ResultHash       string          `json:"result_hash"`
	Status           string          `json:"status"`
	DurationMs       int             `json:"duration_ms"`
	CreatedAt        time.Time       `json:"created_at"`
}

// Event represents a normalized event
type Event struct {
	ID              string                 `json:"id"`
	Source          string                 `json:"source"`
	SourceEventID   string                 `json:"source_event_id,omitempty"`
	Fingerprint     string                 `json:"fingerprint,omitempty"`
	Type            string                 `json:"type"`
	OccurredAt      time.Time              `json:"occurred_at"`
	ReceivedAt      time.Time              `json:"received_at"`
	Service         string                 `json:"service,omitempty"`
	Environment     string                 `json:"environment"`
	Severity        string                 `json:"severity"`
	Title           string                 `json:"title"`
	Status          string                 `json:"status"`
	Labels          map[string]string      `json:"labels"`
	RawPayload      json.RawMessage        `json:"raw_payload,omitempty"`
	SchemaVersion   int                    `json:"schema_version"`
	CreatedAt       time.Time              `json:"created_at"`
}

// IdempotencyRecord represents an idempotency record
type IdempotencyRecord struct {
	Audience       string     `json:"audience"`
	Caller         string     `json:"caller"`
	KeyHash        string     `json:"key_hash"`
	RequestHash    string     `json:"request_hash"`
	Status         string     `json:"status"`
	ResultReference string    `json:"result_reference,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

// InvestigationOutbox represents an outbox record for investigation dispatch
type InvestigationOutbox struct {
	ID              string     `json:"id"`
	IncidentID      string     `json:"incident_id"`
	ContractVersion int        `json:"contract_version"`
	RequestKeyHash  string     `json:"request_key_hash"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	NextAttemptAt   *time.Time `json:"next_attempt_at,omitempty"`
	SafeErrorCode   string     `json:"safe_error_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ReportFeedback represents feedback on a report
type ReportFeedback struct {
	ID        string    `json:"id"`
	ReportID  string    `json:"report_id"`
	Rating    string    `json:"rating"`
	Comments  string    `json:"comments,omitempty"`
	Reviewer  string    `json:"reviewer"`
	CreatedAt time.Time `json:"created_at"`
}

// InvestigationReview represents a review of an investigation run
type InvestigationReview struct {
	ID             string     `json:"id"`
	IncidentID     string     `json:"incident_id"`
	RunID          string     `json:"run_id"`
	EvidenceID     string     `json:"evidence_id,omitempty"`
	Verdict        string     `json:"verdict"`
	CorrectedCause string     `json:"corrected_cause,omitempty"`
	Notes          string     `json:"notes,omitempty"`
	Reviewer       string     `json:"reviewer"`
	CreatedAt      time.Time  `json:"created_at"`
}