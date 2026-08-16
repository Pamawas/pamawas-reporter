package config

import (
	"fmt"
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
	EmailSMTPHost      string
	EmailSMTPPort      int
	EmailUsername      string
	EmailPassword      string
	EmailFrom          string
	EmailTo            string
	ReportTemplate     string
	ReportInterval     time.Duration
	Mode               string
}

func Load() Config {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/pamawas/")
	v.SetEnvPrefix("PAMAWAS_REPORTER")
	v.AutomaticEnv()

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
		DatabaseURL:        v.GetString("database_url"),
		Port:               v.GetString("port"),
		LogLevel:           v.GetString("log_level"),
		Environment:        v.GetString("environment"),
		DiscordWebhookURL:  v.GetString("discord_webhook_url"),
		TelegramBotToken:   v.GetString("telegram_bot_token"),
		TelegramChatID:     v.GetString("telegram_chat_id"),
		EmailSMTPHost:      v.GetString("email_smtp_host"),
		EmailSMTPPort:      v.GetInt("email_smtp_port"),
		EmailUsername:      v.GetString("email_username"),
		EmailPassword:      v.GetString("email_password"),
		EmailFrom:          v.GetString("email_from"),
		EmailTo:            v.GetString("email_to"),
		ReportTemplate:     v.GetString("report_template"),
		ReportInterval:     reportInterval,
		Mode:               v.GetString("mode"),
	}

	if cfg.DatabaseURL == "" {
		panic("DATABASE_URL not set (config file or PAMAWAS_REPORTER_DATABASE_URL env var)")
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