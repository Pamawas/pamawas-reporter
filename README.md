# pamawas-reporter

**Report Generation & Delivery** — Renders investigation findings into morning digest format and delivers via Discord, Telegram, and Email.

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker)](https://docker.com/)

---

## Purpose

Generates human-readable reports from correlated incidents and their investigation evidence, then delivers them through multiple channels with independent retry logic.

## Report Format (Morning Digest)

```
🌅 Good morning.

Infrastructure was 99.97% healthy overnight.
3 incidents occurred. 1 needs your attention. 2 recovered automatically.

Most likely root cause: database connection exhaustion following the 01:47 deployment.
Confidence: 87%.

Recommended actions:
1. Review connection-pool configuration.
2. Compare the deployment's DB connection behavior.
3. Add early-warning monitoring.

No critical outage occurred.
```

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

# Manual report trigger
curl -X POST http://localhost:8080/report \
  -H "Content-Type: application/json" \
  -d '{"source": "manual"}'
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
| `REPORT_TEMPLATE` | Custom template string | Optional |
| `REPORT_INTERVAL` | Background worker interval | `1h` |
| `REPORTER_MODE` | Set to `manual` to disable background worker | auto |
| `LOG_LEVEL` | debug, info, warn, error | `info` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Tempo OTLP gRPC endpoint | `tempo:4317` |

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Health check with DB connectivity |
| `/ready` | GET | Readiness probe |
| `/report` | POST | Manual report generation trigger |
| `/status` | GET | Last sent report info |
| `/metrics` | GET | Prometheus metrics |

## Custom Templates

Set `REPORT_TEMPLATE` to override the default. Available variables:

| Variable | Description |
|----------|-------------|
| `{{HealthyPercentage}}` | Infrastructure health % |
| `{{TotalIncidents}}` | Total incidents in period |
| `{{NeedsAttention}}` | Firing incidents count |
| `{{Recovered}}` | Resolved incidents count |
| `{{MostLikelyCause}}` | Root cause from investigation |
| `{{Confidence}}` | Confidence score (0-1) |
| `{{Timestamp}}` | Report generation time |
| `{{Incidents}}` | Full incident list with evidence |
| `{{Evidence}}` | Evidence map keyed by incident ID |

## Observability

| Feature | Endpoint |
|---------|----------|
| Prometheus Metrics | `/metrics` — `ReportsGeneratedTotal`, `DeliveryTotal`, `TemplateRenderDuration`, `LastSentTimestamp` |
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