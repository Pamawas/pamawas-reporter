# pamawas-reporter

**Report Generation & Delivery** — Morning digest format, Discord/Telegram/Email adapters

Language: Go 1.26

## Purpose

Generates formatted reports from investigation findings and delivers them via multiple channels. Consumes incident data, evidence, and findings from PostgreSQL and renders them into the target morning digest format (MVP §9). Supports Discord, Telegram, and Email delivery.

## MVP Reference

- **MVP §10 Build Order #4**: Report generator — render incident + evidence into target report shape
- **MVP §10 Build Order #5**: Delivery adapters — Discord → Telegram → Email (in that order)
- **MVP §9 Delivery**: All channels consume same rendered report; only formatting/transport differs
- **MVP §9 Report Shape**: Morning digest format with healthy %, incident count, root cause, confidence, recommendations

## Responsibilities

- Query recent incidents and their evidence/findings from PostgreSQL
- Render reports using template engine (default + custom templates)
- Deliver via Discord webhook, Telegram Bot API, Email SMTP
- Background worker for scheduled daily reports
- Manual trigger endpoint for on-demand reports
- Health/metrics endpoints

## Report Format (MVP §9 Target)

```text
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

## Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/healthz` | GET | Health check with DB connectivity |
| `/ready` | GET | Readiness check |
| `/report` | POST | Manual report generation trigger |
| `/status` | GET | Last sent report info |
| `/metrics` | GET | Prometheus metrics |

## Delivery Channels (MVP §9)

| Channel | Mechanism | Setup Cost | Priority |
|---------|-----------|------------|----------|
| Discord | Webhook POST with embeds | Low — no auth flow | 1st |
| Telegram | Bot API POST | Low — bot token + chat ID | 2nd |
| Email | SMTP or transactional (SES/Postmark) | Higher — credentials + HTML | 3rd |

## Configuration (Environment Variables)

| Variable | Description | Default |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Required |
| `PORT` | HTTP server port | `8080` |
| `DISCORD_WEBHOOK_URL` | Discord webhook URL | Optional |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token | Optional |
| `TELEGRAM_CHAT_ID` | Telegram chat ID | Optional |
| `EMAIL_SMTP_HOST` | SMTP server host | Optional |
| `EMAIL_SMTP_PORT` | SMTP server port | `587` |
| `EMAIL_USERNAME` | SMTP username | Optional |
| `EMAIL_PASSWORD` | SMTP password | Optional |
| `EMAIL_FROM` | From email address | Optional |
| `REPORT_TEMPLATE` | Custom template string | Optional |
| `REPORT_INTERVAL` | Background worker interval | `1h` |
| `REPORTER_MODE` | `manual` to disable background worker | (auto) |
| `LOG_LEVEL` | Log level | `info` |

## Database Schema (from pamawas-schema)

```sql
-- Reports table (written by reporter)
CREATE TABLE IF NOT EXISTS reports (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    sent_at TIMESTAMPTZ,
    channels TEXT[]
);

-- Incidents, evidence (read by reporter)
-- Defined in pamawas-schema migrations
```

## Current Implementation Status

- ✅ Report generator with template engine (default + custom)
- ✅ Discord webhook delivery adapter
- ✅ Telegram Bot API delivery adapter
- ✅ Email SMTP delivery adapter (placeholder)
- ✅ Background worker for scheduled reports
- ✅ Manual trigger endpoint (`/report`)
- ✅ Health (`/healthz`), readiness (`/ready`), status (`/status`), metrics (`/metrics`)
- ✅ Concurrent delivery to all configured channels
- ✅ Multi-stage Dockerfile (Go 1.26-alpine builder, alpine runtime)
- ✅ GitHub Actions workflow (main + dev branches, GHCR publishing)
- ⬜ Proper email implementation (go-gomail or net/smtp)
- ⬜ HTML email template
- ⬜ Prometheus metrics with proper labels
- ⬜ Structured JSON logging
- ⬜ Configuration management (YAML + ENV)
- ⬜ Unit tests for generation and delivery (target 80%+ coverage)

## Kanban Tasks

- `t_84f79593` — Design report generation and rendering (architect)
- `t_1ba41fba` — Implement report generator with template engine (backend-dev)
- `t_a3da3a74` — Implement Discord delivery adapter (backend-dev)
- `t_0123337f` — Implement Telegram delivery adapter (backend-dev)
- `t_d90b483d` — Implement Email delivery adapter (backend-dev)
- `t_929d6b97` — Write unit tests for generation and delivery (qa-dev)

## Dependencies

- **PostgreSQL** — incidents, evidence, reports tables (via pamawas-schema)
- **pamawas-schema** — Shared types and migrations (parent: `t_d1cdd7a9`)
- **pamawas-investigator** — Produces evidence/findings to report on
- **pamawas-scheduler** — Triggers scheduled reports
- **Discord/Telegram/Email** — External delivery services

## Build & Run

```bash
# Local development
go run main.go

# Docker
docker build -t pamawas-reporter .
docker run -e DATABASE_URL="postgres://..." \
  -e DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/..." \
  -e TELEGRAM_BOT_TOKEN="..." \
  -e TELEGRAM_CHAT_ID="..." \
  -p 8080:8080 pamawas-reporter

# Manual report trigger
curl -X POST http://localhost:8080/report \
  -H "Content-Type: application/json" \
  -d '{"source": "manual"}'
```

## Template Variables

When using custom `REPORT_TEMPLATE`, these variables are available:

| Variable | Description |
|----------|-------------|
| `{{HealthyPercentage}}` | Infrastructure health % |
| `{{TotalIncidents}}` | Total incidents in period |
| `{{NeedsAttention}}` | Firing incidents count |
| `{{Recovered}}` | Resolved incidents count |
| `{{MostLikelyCause}}` | Root cause from investigation |
| `{{Confidence}}` | Confidence score (0-1) |
| `{{Timestamp}}` | Report generation time |

## Example Custom Template

```text
{{Timestamp}} - Infrastructure Report

Health: {{HealthyPercentage}}%
Incidents: {{TotalIncidents}} ({{NeedsAttention}} active, {{Recovered}} recovered)

{{if gt TotalIncidents 0}}
Root Cause: {{MostLikelyCause}}
Confidence: {{Confidence}}%
{{end}}
```