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
	t.Setenv("PAMAWAS_REPORTER_REPORT_INTERVAL", "15m")
	t.Setenv("PAMAWAS_REPORTER_MODE", "manual")

	cfg := Load()
	if cfg.DatabaseURL != "postgres://example/test" || cfg.Port != "9090" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.ReportInterval != 15*time.Minute || cfg.Mode != "manual" {
		t.Fatalf("environment overrides not applied: %+v", cfg)
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
		if !ok || !strings.Contains(msg, "DATABASE_URL not set") {
			t.Fatalf("unexpected panic: %v", got)
		}
	}()
	_ = Load()
}

func TestLoadPanicsForInvalidInterval(t *testing.T) {
	inTempDir(t)
	t.Setenv("PAMAWAS_REPORTER_DATABASE_URL", "postgres://example/test")
	t.Setenv("PAMAWAS_REPORTER_REPORT_INTERVAL", "sometimes")
	defer func() {
		got := recover()
		if got == nil {
			t.Fatalf("expected panic, got nil")
			return
		}
		msg, ok := got.(string)
		if !ok || !strings.Contains(msg, "invalid report_interval") {
			t.Fatalf("unexpected panic: %v", got)
		}
	}()
	_ = Load()
}

func TestValidate(t *testing.T) {
	valid := Config{DatabaseURL: "db", Port: "8080", ReportInterval: time.Minute}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"database", Config{Port: "8080", ReportInterval: time.Minute}, "database_url"},
		{"port", Config{DatabaseURL: "db", ReportInterval: time.Minute}, "port"},
		{"interval", Config{DatabaseURL: "db", Port: "8080"}, "report_interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
