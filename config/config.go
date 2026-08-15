package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL        string
	Port               string
	DiscordWebhookURL  string
	TelegramBotToken   string
	TelegramChatID     string
	EmailSMTPHost      string
	EmailSMTPPort      int
	EmailUsername      string
	EmailPassword      string
	EmailFrom          string
	ReportTemplate     string
	ReportInterval     time.Duration
	Mode               string
}

func Load() Config {
	port, _ := strconv.Atoi(getEnv("EMAIL_SMTP_PORT", "587"))
	intervalStr := getEnv("REPORT_INTERVAL", "1h")
	interval, _ := time.ParseDuration(intervalStr)

	cfg := Config{
		DatabaseURL:        getEnv("DATABASE_URL", ""),
		Port:               getEnv("PORT", "8080"),
		DiscordWebhookURL:  getEnv("DISCORD_WEBHOOK_URL", ""),
		TelegramBotToken:   getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:     getEnv("TELEGRAM_CHAT_ID", ""),
		EmailSMTPHost:      getEnv("EMAIL_SMTP_HOST", ""),
		EmailSMTPPort:      port,
		EmailUsername:      getEnv("EMAIL_USERNAME", ""),
		EmailPassword:      getEnv("EMAIL_PASSWORD", ""),
		EmailFrom:          getEnv("EMAIL_FROM", ""),
		ReportTemplate:     getEnv("REPORT_TEMPLATE", ""),
		ReportInterval:     interval,
		Mode:               getEnv("REPORTER_MODE", "auto"),
	}

	if cfg.DatabaseURL == "" {
		panic("DATABASE_URL environment variable not set")
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}