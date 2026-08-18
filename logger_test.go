package main

import (
	"testing"

	"github.com/rs/zerolog"

	"github.com/Pamawas/pamawas-reporter/config"
)

func TestInitLoggerSetsConfiguredLevel(t *testing.T) {
	old := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(old) })
	initLogger(config.Config{LogLevel: "debug", Environment: "production"})
	if got := zerolog.GlobalLevel(); got != zerolog.DebugLevel {
		t.Fatalf("global level = %v", got)
	}
}

func TestInitLoggerFallsBackToInfo(t *testing.T) {
	old := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(old) })
	initLogger(config.Config{LogLevel: "invalid", Environment: "development"})
	if got := zerolog.GlobalLevel(); got != zerolog.InfoLevel {
		t.Fatalf("global level = %v", got)
	}
}
