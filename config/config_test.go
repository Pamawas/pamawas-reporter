package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func inTempDir(t *testing.T) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(old); chdirErr != nil {
			t.Logf("Failed to restore directory: %v", chdirErr)
		}
	})
}

func TestLoadDefaultsAndEnvironmentOverrides(t *testing.T) {
	inTempDir(t)
	t.Setenv("PAMAWAS_REPORTER_DATABASE_URL", "postgres://example/test")
	t.Setenv("PAMAWAS_REPORTER_PORT", "9090")
	t.Setenv("PAMAWAS_SERVICE_TOKEN", "test-service-token")

	cfg := Load()
	if cfg.DatabaseURL != "postgres://example/test" || cfg.Port != "9090" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.LogLevel != "info" || cfg.Environment != "development" || cfg.EmailSMTPPort != 587 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadPanicsWithoutDatabaseURL(t *testing.T) {
	inTempDir(t)
	t.Setenv("PAMAWAS_REPORTER_DATABASE_URL", "")
	defer func() {
		got := recover()
		if got == nil {
			t.Fatalf("expected panic, got nil")
			return
		}
		msg, ok := got.(string)
		if !ok || !strings.Contains(msg, "database_url is required") {
			t.Fatalf("unexpected panic: %v", got)
		}
	}()
	_ = Load()
}

func TestValidate(t *testing.T) {
	valid := Config{
		DatabaseURL:                    "db",
		Port:                           "8080",
		ServiceToken:                   "token",
		HighSeverityThreshold:          "high",
		ReportTemplate:                 "v1",
		EmailSMTPPort:                  587,
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerResetTimeout:     30 * time.Second,
		RateLimitRPS:                   10,
		RateLimitBurst:                 20,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"database", func() Config { c := valid; c.DatabaseURL = ""; return c }(), "database_url"},
		{"port", func() Config { c := valid; c.Port = ""; return c }(), "port"},
		{"service_token", func() Config { c := valid; c.ServiceToken = ""; return c }(), "service_token"},
		{"cb_threshold", func() Config { c := valid; c.CircuitBreakerFailureThreshold = 0; return c }(), "circuit_breaker_failure_threshold"},
		{"cb_reset", func() Config { c := valid; c.CircuitBreakerResetTimeout = 0; return c }(), "circuit_breaker_reset_timeout"},
		{"threshold", func() Config { c := valid; c.HighSeverityThreshold = "urgent"; return c }(), "high_severity_threshold"},
		{"template", func() Config { c := valid; c.ReportTemplate = "v3"; return c }(), "report_template"},
		{"smtp_port", func() Config { c := valid; c.EmailSMTPPort = 0; return c }(), "email_smtp_port"},
		{"rate_rps", func() Config { c := valid; c.RateLimitRPS = 0; return c }(), "rate_limit_rps"},
		{"rate_burst", func() Config { c := valid; c.RateLimitBurst = 0; return c }(), "rate_limit_burst"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestUnknownThresholdAdmitsEverySeverity pins the "no gate" meaning of
// HIGH_SEVERITY_THRESHOLD=unknown.
//
// It ranks below debug, so a threshold of unknown admits events that carry no
// severity at all as well as every real one. Operators use it when every alert
// must flow through to an investigation and a report.
func TestUnknownThresholdAdmitsEverySeverity(t *testing.T) {
	c := Config{
		DatabaseURL:                    "db",
		Port:                           "8080",
		ServiceToken:                   "token",
		HighSeverityThreshold:          "unknown",
		ReportTemplate:                 "v1",
		EmailSMTPPort:                  587,
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerResetTimeout:     30 * time.Second,
		RateLimitRPS:                   10,
		RateLimitBurst:                 20,
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("threshold \"unknown\" must be accepted: %v", err)
	}
}
