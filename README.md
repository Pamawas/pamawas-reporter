# pamawas-reporter

**Report Generation & Delivery** — Processes scheduler-created report requests and delivers them via Discord, Telegram, and Email.

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker)](https://docker.com/)

---

## Purpose

Generates human-readable reports from correlated incidents and their investigation evidence, then delivers them through multiple channels with independent retry logic.

Reports are generated only in response to scheduler requests. The reporter does not run a periodic report-generation worker.

## Delivery Channels

| Channel | Mechanism | Setup |
|---------|-----------|-------|
| **Discord** | Webhook POST with embeds | Low — webhook URL only |
| **Telegram** | Bot API POST | Low — bot token + chat ID |
| **Email** | SMTP with STARTTLS, HTML + text | Medium — SMTP credentials |

All channels consume the same rendered report; only formatting/transport differs. Deliveries retry independently without duplicating successful sends.

## Quick Start

```bash
# Docker
docker run -e DATABASE_URL="postgres://user:pass@host:5432/db" \
  -e DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/..." \
  -e TELEGRAM_BOT_TOKEN="..." \
  -e TELEGRAM_CHAT_ID="..." \
  -e EMAIL_SMTP_HOST="smtp.example.com" \
  -e EMAIL_USERNAME="..." \
  -e EMAIL_PASSWORD="..." \
  -e EMAIL_FROM="reporter@example.com" \
  -e EMAIL_TO="team@example.com" \
  -p 8080:8080 ghcr.io/yoganovvaindra/pamawas-reporter:latest

# Local development
go run main.go

```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string | **Required** |
| `PORT` | HTTP server port | `8080` |
| `DISCORD_WEBHOOK_URL` | Discord webhook URL | Optional |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token | Optional |
| `TELEGRAM_CHAT_ID` | Telegram chat ID | Optional |
| `EMAIL_SMTP_HOST` | SMTP server host | Optional |
| `EMAIL_SMTP_PORT` | SMTP server port | `587` |
| `EMAIL_USERNAME` | SMTP username | Optional |
| `EMAIL_PASSWORD` | SMTP password | Optional |
| `EMAIL_FROM` | From email address | Optional |
| `EMAIL_TO` | To email address | Optional |
| `LOG_LEVEL` | debug, info, warn, error | `info` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Tempo OTLP gRPC endpoint | `tempo:4317` |

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Health check with DB connectivity |
| `/ready` | GET | Readiness probe |
| `/api/v1/reports` | POST | Process a scheduler-created report request |
| `/status` | GET | Reporter uptime and version |
| `/metrics` | GET | Prometheus metrics |

## Observability

| Feature | Endpoint |
|---------|----------|
| Prometheus Metrics | `/metrics` — `ReportsGeneratedTotal`, `DeliveryTotal`, `DBConnectionErrors` |
| JSON Logging | stdout — trace_id, span_id, service, method, path, status_code, duration_ms |
| OpenTelemetry | OTLP gRPC → Tempo:4317 |

## Building

```bash
docker build -t pamawas-reporter .
go build -o pamawas-reporter main.go
```

## Related

- **Root README**: [../README.md](../README.md)
- **Investigator**: [../pamawas-investigator/README.md](../pamawas-investigator/README.md)
- **Scheduler**: [../pamawas-scheduler/README.md](../pamawas-scheduler/README.md)
- **Database Schema**: [../pamawas-schema/README.md](../pamawas-schema/README.md)