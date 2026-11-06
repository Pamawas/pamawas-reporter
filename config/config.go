package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	DatabaseURL         string
	Port                string
	LogLevel            string
	Environment         string
	DiscordWebhookURL   string
	TelegramBotToken    string
	TelegramChatID      string
	TelegramAPIBaseURL  string
	EmailSMTPHost       string
	EmailSMTPPort       int
	EmailUsername       string
	EmailPassword       string
	EmailFrom           string
	EmailTo             string
	ReportTemplate      string
	ReportInterval      time.Duration
	Mode                string
	PrometheusURL       string
	PrometheusQueryTemplate string
	PrometheusTimeout   int // in seconds
	LokiURL             string
	LokiQueryTemplate   string
	LokiTimeout         int // in seconds
	LokiMaxLines        int
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
	if err := v.BindEnv("report_template", "REPORT_TEMPLATE", "PAMAWAS_REPORTER_REPORT_TEMPLATE"); err != nil {
		panic(fmt.Sprintf("failed to bind REPORT_TEMPLATE env: %v", err))
	}
	if err := v.BindEnv("report_interval", "REPORT_INTERVAL", "PAMAWAS_REPORTER_REPORT_INTERVAL"); err != nil {
		panic(fmt.Sprintf("failed to bind REPORT_INTERVAL env: %v", err))
	}
	if err := v.BindEnv("mode", "REPORTER_MODE", "PAMAWAS_REPORTER_MODE"); err != nil {
		panic(fmt.Sprintf("failed to bind REPORTER_MODE env: %v", err))
	}
	// Prometheus configuration
	if err := v.BindEnv("prometheus_url", "PROMETHEUS_URL", "PAMAWAS_REPORTER_PROMETHEUS_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind PROMETHEUS_URL env: %v", err))
	}
	if err := v.BindEnv("prometheus_query_template", "PROMETHEUS_QUERY_TEMPLATE", "PAMAWAS_REPORTER_PROMETHEUS_QUERY_TEMPLATE"); err != nil {
		panic(fmt.Sprintf("failed to bind PROMETHEUS_QUERY_TEMPLATE env: %v", err))
	}
	if err := v.BindEnv("prometheus_timeout", "PROMETHEUS_TIMEOUT", "PAMAWAS_REPORTER_PROMETHEUS_TIMEOUT"); err != nil {
		panic(fmt.Sprintf("failed to bind PROMETHEUS_TIMEOUT env: %v", err))
	}
	// Loki configuration
	if err := v.BindEnv("loki_url", "LOKI_URL", "PAMAWAS_REPORTER_LOKI_URL"); err != nil {
		panic(fmt.Sprintf("failed to bind LOKI_URL env: %v", err))
	}
	if err := v.BindEnv("loki_query_template", "LOKI_QUERY_TEMPLATE", "PAMAWAS_REPORTER_LOKI_QUERY_TEMPLATE"); err != nil {
		panic(fmt.Sprintf("failed to bind LOKI_QUERY_TEMPLATE env: %v", err))
	}
	if err := v.BindEnv("loki_timeout", "LOKI_TIMEOUT", "PAMAWAS_REPORTER_LOKI_TIMEOUT"); err != nil {
		panic(fmt.Sprintf("failed to bind LOKI_TIMEOUT env: %v", err))
	}
	if err := v.BindEnv("loki_max_lines", "LOKI_MAX_LINES", "PAMAWAS_REPORTER_LOKI_MAX_LINES"); err != nil {
		panic(fmt.Sprintf("failed to bind LOKI_MAX_LINES env: %v", err))
	}

	// Defaults
	v.SetDefault("port", "8080")
	v.SetDefault("log_level", "info")
	v.SetDefault("environment", "development")
	v.SetDefault("email_smtp_port", 587)
	v.SetDefault("report_interval", "1h")
	v.SetDefault("mode", "auto")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			panic(fmt.Sprintf("failed to read config: %v", err))
		}
	}

	reportInterval, err := time.ParseDuration(v.GetString("report_interval"))
	if err != nil {
		panic(fmt.Sprintf("invalid report_interval: %v", err))
	}

	cfg := Config{
		DatabaseURL:             v.GetString("database_url"),
		Port:                    v.GetString("port"),
		LogLevel:                v.GetString("log_level"),
		Environment:             v.GetString("environment"),
		DiscordWebhookURL:       v.GetString("discord_webhook_url"),
		TelegramBotToken:        v.GetString("telegram_bot_token"),
		TelegramChatID:          v.GetString("telegram_chat_id"),
		TelegramAPIBaseURL:      v.GetString("telegram_api_base_url"),
		EmailSMTPHost:           v.GetString("email_smtp_host"),
		EmailSMTPPort:           v.GetInt("email_smtp_port"),
		EmailUsername:           v.GetString("email_username"),
		EmailPassword:           v.GetString("email_password"),
		EmailFrom:               v.GetString("email_from"),
		EmailTo:                 v.GetString("email_to"),
		ReportTemplate:          v.GetString("report_template"),
		ReportInterval:          reportInterval,
		Mode:                    v.GetString("mode"),
		PrometheusURL:           v.GetString("prometheus_url"),
		PrometheusQueryTemplate: v.GetString("prometheus_query_template"),
		PrometheusTimeout:       v.GetInt("prometheus_timeout"),
		LokiURL:                 v.GetString("loki_url"),
		LokiQueryTemplate:       v.GetString("loki_query_template"),
		LokiTimeout:             v.GetInt("loki_timeout"),
		LokiMaxLines:            v.GetInt("loki_max_lines"),
	}

	// Set defaults for new fields
	if cfg.PrometheusTimeout == 0 {
		cfg.PrometheusTimeout = 30 // default 30 seconds
	}
	if cfg.LokiTimeout == 0 {
		cfg.LokiTimeout = 30
	}
	if cfg.LokiMaxLines == 0 {
		cfg.LokiMaxLines = 100
	}

	if cfg.DatabaseURL == "" {
		panic("DATABASE_URL not set (config file or PAMAWAS_REPORTER_DATABASE_URL / DATABASE_URL env var)")
	}
	return cfg
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("database_url is required")
	}
	if c.Port == "" {
		return fmt.Errorf("port is required")
	}
	if c.ReportInterval <= 0 {
		return fmt.Errorf("report_interval must be positive")
	}
	return nil
}
