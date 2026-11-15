package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	DatabaseURL        string
	Port               string
	LogLevel           string
	Environment        string
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
	// Minimum severity (inclusive) that triggers resolve notifications ("high" sends for high/critical only).
	HighSeverityThreshold string
	// How long to wait for investigation to complete before generating high-severity report. Zero disables the wait.
	HighSeverityInvestigationTimeout time.Duration
	// Base URL of Prometheus server for querying availability metrics.
	PrometheusURL string
	// Prometheus metric name used to compute availability (e.g. "up").
	AvailabilityMetric string
	// Report rendering template: "v1" (legacy plain text) or "v2" (Uptime Kuma-style card layout).
	ReportTemplate string
	// Base URL of the investigator service, which owns all LLM usage. The daily
	// report asks it for a prose summary; when unset or unreachable the digest
	// renders the deterministic incident lists instead.
	InvestigatorURL string
	// Hard cap on the summary call. Delivery must not wait on a slow model.
	SummaryTimeout time.Duration
	// Delivery-channel circuit breaker (centralized; was hard-coded 3 / 30s in
	// service.NewReporter).
	CircuitBreakerFailureThreshold int
	CircuitBreakerResetTimeout     time.Duration

	// Inbound HTTP rate limiting (design review P5). Applies per route;
	// /healthz, /ready and /metrics are exempt.
	RateLimitRPS   float64
	RateLimitBurst int
}

func Load() Config {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/pamawas/")
	v.SetEnvPrefix("PAMAWAS_REPORTER")
	// Don't use AutomaticEnv() - Kubernetes injects PAMAWAS_REPORTER_PORT=tcp://IP:PORT
	// which would override our config. Explicitly bind only what we need.
	if err := v.BindEnv("database_url", "DATABASE_URL", "PAMAWAS_REPORTER_DATABASE_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind DATABASE_URL env: %v", err))
	}
	if err := v.BindEnv("port", "PORT", "PAMAWAS_REPORTER_PORT"); err != nil {
		panic(fmt.Sprintf("failed to bind PORT env: %v", err))
	}
	if err := v.BindEnv("log_level", "LOG_LEVEL", "PAMAWAS_REPORTER_LOG_LEVEL"); err != nil {
		panic(fmt.Sprintf("failed to bind LOG_LEVEL env: %v", err))
	}
	if err := v.BindEnv("environment", "ENVIRONMENT", "PAMAWAS_REPORTER_ENVIRONMENT"); err != nil {
		panic(fmt.Sprintf("failed to bind ENVIRONMENT env: %v", err))
	}
	if err := v.BindEnv("discord_webhook_url", "DISCORD_WEBHOOK_URL", "PAMAWAS_REPORTER_DISCORD_WEBHOOK_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind DISCORD_WEBHOOK_URL env: %v", err))
	}
	if err := v.BindEnv("telegram_bot_token", "TELEGRAM_BOT_TOKEN", "PAMAWAS_REPORTER_TELEGRAM_BOT_TOKEN"); err != nil {
		panic(fmt.Sprintf("failed to bind TELEGRAM_BOT_TOKEN env: %v", err))
	}
	if err := v.BindEnv("telegram_chat_id", "TELEGRAM_CHAT_ID", "PAMAWAS_REPORTER_TELEGRAM_CHAT_ID"); err != nil {
		panic(fmt.Sprintf("failed to bind TELEGRAM_CHAT_ID env: %v", err))
	}
	if err := v.BindEnv("telegram_api_base_url", "TELEGRAM_API_BASE_URL", "PAMAWAS_REPORTER_TELEGRAM_API_BASE_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind TELEGRAM_API_BASE_URL env: %v", err))
	}
	if err := v.BindEnv("email_smtp_host", "EMAIL_SMTP_HOST", "PAMAWAS_REPORTER_EMAIL_SMTP_HOST"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_SMTP_HOST env: %v", err))
	}
	if err := v.BindEnv("email_smtp_port", "EMAIL_SMTP_PORT", "PAMAWAS_REPORTER_EMAIL_SMTP_PORT"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_SMTP_PORT env: %v", err))
	}
	if err := v.BindEnv("email_username", "EMAIL_USERNAME", "PAMAWAS_REPORTER_EMAIL_USERNAME"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_USERNAME env: %v", err))
	}
	if err := v.BindEnv("email_password", "EMAIL_PASSWORD", "PAMAWAS_REPORTER_EMAIL_PASSWORD"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_PASSWORD env: %v", err))
	}
	if err := v.BindEnv("email_from", "EMAIL_FROM", "PAMAWAS_REPORTER_EMAIL_FROM"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_FROM env: %v", err))
	}
	if err := v.BindEnv("email_to", "EMAIL_TO", "PAMAWAS_REPORTER_EMAIL_TO"); err != nil {
		panic(fmt.Sprintf("failed to bind EMAIL_TO env: %v", err))
	}
	if err := v.BindEnv("high_severity_threshold", "HIGH_SEVERITY_THRESHOLD", "PAMAWAS_REPORTER_HIGH_SEVERITY_THRESHOLD"); err != nil {
		panic(fmt.Sprintf("failed to bind HIGH_SEVERITY_THRESHOLD env: %v", err))
	}
	if err := v.BindEnv("high_severity_investigation_timeout", "HighSeverityInvestigationTimeout", "PAMAWAS_REPORTER_HIGH_SEVERITY_INVESTIGATION_TIMEOUT"); err != nil {
		panic(fmt.Sprintf("failed to bind HighSeverityInvestigationTimeout env: %v", err))
	}
	if err := v.BindEnv("prometheus_url", "PROMETHEUS_URL", "PAMAWAS_REPORTER_PROMETHEUS_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind PROMETHEUS_URL env: %v", err))
	}
	if err := v.BindEnv("availability_metric", "AVAILABILITY_METRIC", "PAMAWAS_REPORTER_AVAILABILITY_METRIC"); err != nil {
		panic(fmt.Sprintf("failed to bind AVAILABILITY_METRIC env: %v", err))
	}
	if err := v.BindEnv("report_template", "REPORT_TEMPLATE", "PAMAWAS_REPORTER_REPORT_TEMPLATE"); err != nil {
		panic(fmt.Sprintf("failed to bind REPORT_TEMPLATE env: %v", err))
	}
	if err := v.BindEnv("circuit_breaker_failure_threshold", "CIRCUIT_BREAKER_FAILURE_THRESHOLD"); err != nil {
		panic(fmt.Sprintf("failed to bind CIRCUIT_BREAKER_FAILURE_THRESHOLD env: %v", err))
	}
	if err := v.BindEnv("circuit_breaker_reset_timeout", "CIRCUIT_BREAKER_RESET_TIMEOUT"); err != nil {
		panic(fmt.Sprintf("failed to bind CIRCUIT_BREAKER_RESET_TIMEOUT env: %v", err))
	}
	if err := v.BindEnv("investigator_url", "INVESTIGATOR_URL", "PAMAWAS_REPORTER_INVESTIGATOR_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind INVESTIGATOR_URL env: %v", err))
	}
	if err := v.BindEnv("summary_timeout", "SUMMARY_TIMEOUT"); err != nil {
		panic(fmt.Sprintf("failed to bind SUMMARY_TIMEOUT env: %v", err))
	}
	if err := v.BindEnv("rate_limit_rps", "RATE_LIMIT_RPS"); err != nil {
		panic(fmt.Sprintf("failed to bind RATE_LIMIT_RPS env: %v", err))
	}
	if err := v.BindEnv("rate_limit_burst", "RATE_LIMIT_BURST"); err != nil {
		panic(fmt.Sprintf("failed to bind RATE_LIMIT_BURST env: %v", err))
	}

	// Defaults
	v.SetDefault("port", "8080")
	v.SetDefault("log_level", "info")
	v.SetDefault("environment", "development")
	v.SetDefault("email_smtp_port", 587)
	v.SetDefault("high_severity_threshold", "high")
	v.SetDefault("high_severity_investigation_timeout", "5m")
	v.SetDefault("report_template", "v1")
	v.SetDefault("summary_timeout", "20s")
	v.SetDefault("circuit_breaker_failure_threshold", 3)
	v.SetDefault("circuit_breaker_reset_timeout", "30s")
	v.SetDefault("rate_limit_rps", 10)
	v.SetDefault("rate_limit_burst", 20)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			panic(fmt.Sprintf("failed to read config: %v", err))
		}
	}

	cfg := Config{
		DatabaseURL:                      v.GetString("database_url"),
		Port:                             v.GetString("port"),
		LogLevel:                         v.GetString("log_level"),
		Environment:                      v.GetString("environment"),
		DiscordWebhookURL:                v.GetString("discord_webhook_url"),
		TelegramBotToken:                 v.GetString("telegram_bot_token"),
		TelegramChatID:                   v.GetString("telegram_chat_id"),
		TelegramAPIBaseURL:               v.GetString("telegram_api_base_url"),
		EmailSMTPHost:                    v.GetString("email_smtp_host"),
		EmailSMTPPort:                    v.GetInt("email_smtp_port"),
		EmailUsername:                    v.GetString("email_username"),
		EmailPassword:                    v.GetString("email_password"),
		EmailFrom:                        v.GetString("email_from"),
		EmailTo:                          v.GetString("email_to"),
		HighSeverityThreshold:            v.GetString("high_severity_threshold"),
		HighSeverityInvestigationTimeout: v.GetDuration("high_severity_investigation_timeout"),
		PrometheusURL:                    v.GetString("prometheus_url"),
		AvailabilityMetric:               v.GetString("availability_metric"),
		ReportTemplate:                   v.GetString("report_template"),
		InvestigatorURL:                  v.GetString("investigator_url"),
		SummaryTimeout:                   v.GetDuration("summary_timeout"),
		CircuitBreakerFailureThreshold:   v.GetInt("circuit_breaker_failure_threshold"),
		CircuitBreakerResetTimeout:       v.GetDuration("circuit_breaker_reset_timeout"),
		RateLimitRPS:                     v.GetFloat64("rate_limit_rps"),
		RateLimitBurst:                   v.GetInt("rate_limit_burst"),
	}

	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("invalid configuration: %v", err))
	}
	return cfg
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("database_url is required (config file or PAMAWAS_REPORTER_DATABASE_URL / DATABASE_URL env var)")
	}
	if c.Port == "" {
		return fmt.Errorf("port is required")
	}
	if c.CircuitBreakerFailureThreshold < 1 {
		return fmt.Errorf("circuit_breaker_failure_threshold must be >= 1, got %d", c.CircuitBreakerFailureThreshold)
	}
	if c.CircuitBreakerResetTimeout <= 0 {
		return fmt.Errorf("circuit_breaker_reset_timeout must be > 0, got %s", c.CircuitBreakerResetTimeout)
	}
	switch strings.ToLower(c.HighSeverityThreshold) {
	case "debug", "info", "warning", "high", "critical":
	default:
		return fmt.Errorf("high_severity_threshold must be one of debug|info|warning|high|critical, got %q", c.HighSeverityThreshold)
	}
	switch c.ReportTemplate {
	case "v1", "v2":
	default:
		return fmt.Errorf("report_template must be v1 or v2, got %q", c.ReportTemplate)
	}
	if c.EmailSMTPPort < 1 || c.EmailSMTPPort > 65535 {
		return fmt.Errorf("email_smtp_port must be 1..65535, got %d", c.EmailSMTPPort)
	}
	if c.RateLimitRPS <= 0 {
		return fmt.Errorf("rate_limit_rps must be positive, got %v", c.RateLimitRPS)
	}
	if c.RateLimitBurst < 1 {
		return fmt.Errorf("rate_limit_burst must be >= 1, got %d", c.RateLimitBurst)
	}
	return nil
}
