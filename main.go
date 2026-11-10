package main

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/Pamawas/pamawas-reporter/config"
	"github.com/Pamawas/pamawas-reporter/handlers"
	"github.com/Pamawas/pamawas-reporter/metrics"
	"github.com/Pamawas/pamawas-reporter/middleware"
	"github.com/Pamawas/pamawas-reporter/otel"
)

func main() {
	cfg := config.Load()
	initLogger(cfg)
	printStartupBanner(loadVersion(), cfg.Port)

	// Initialize OpenTelemetry tracing
	otelShutdown, err := otel.InitTracer(otel.Config{
		ServiceName:  "pamawas-reporter",
		OTLPEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Insecure:     true,
		SampleRatio:  1.0,
		Enabled:      os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "",
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize OpenTelemetry")
	}
	defer func() {
		if shutdownErr := otelShutdown(context.Background()); shutdownErr != nil {
			log.Error().Err(shutdownErr).Msg("Error shutting down OpenTelemetry")
		}
	}()

	// Connect to database with retries
	log.Info().Msg("Opening database connection")
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Error opening database")
	}

	// Set up defer for database close after db is declared
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Error().Err(closeErr).Msg("Failed to close database connection")
		}
	}()

	log.Info().
		Str("port", cfg.Port).
		Str("environment", cfg.Environment).
		Str("log_level", cfg.LogLevel).
		Str("report_interval", cfg.ReportInterval.String()).
		Str("mode", cfg.Mode).
		Msg("Starting pamawas-reporter")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Info().Int("max_attempts", 30).Msg("Waiting for database to be ready")
	var pingErr error
	for i := 0; i < 30; i++ {
		if pingErr = db.PingContext(ctx); pingErr == nil {
			break
		}
		log.Info().Int("attempt", i+1).Err(pingErr).Msg("Database not ready, retrying...")
		time.Sleep(1 * time.Second)
	}

	if finalErr := db.PingContext(ctx); finalErr != nil {
		log.Fatal().Err(finalErr).Msg("Failed to connect to database after retries")
	}

	log.Info().Msg("Connected to database successfully")

	// Initialize metrics
	m := metrics.NewMetrics()

	// Initialize handlers (fetchers removed - evidence enrichment now in investigator)
	h := handlers.NewHandler(db, cfg, m)

	// Start background worker if not in manual mode
	if cfg.Mode != "manual" {
		go h.Reporter().StartWorker()
	}

	// Create router
	r := http.NewServeMux()

	// Health endpoints at root (for k8s probes)
	r.HandleFunc("/healthz", h.HealthHandler)
	r.HandleFunc("/ready", h.ReadyHandler)

	// V1 API endpoints
	r.HandleFunc("/api/v1/reports", h.ReportHandler)
	r.HandleFunc("/api/v1/status", h.StatusHandler)
	registerMetricsRoutes(r, h.MetricsHandler())

	// Wrap router with middleware
	var handler http.Handler = r
	handler = middleware.LoggingMiddleware("pamawas-reporter", handler)
	handler = middleware.ErrorLoggingMiddleware("pamawas-reporter", handler)

	// Create server
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		log.Info().Msg("Shutdown signal received, stopping server...")
		h.Reporter().Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("Server forced to shutdown")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("Starting server")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("Server failed")
	}

	log.Info().Msg("Server stopped gracefully")
}

func registerMetricsRoutes(r *http.ServeMux, metricsHandler http.Handler) {
	r.Handle("/metrics", metricsHandler)
}

func initLogger(cfg config.Config) {
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// Match the structured JSON format used by the other pamawas services:
	// human-readable console output in development, structured JSON in
	// production.
	if cfg.Environment == "development" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	} else {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
}