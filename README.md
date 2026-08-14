# pamawas-reporter

Report generation + Discord/Telegram/Email delivery adapters

Language: Go

## Purpose
Generates reports from investigation findings and delivers them via Discord, Telegram, and Email.

## Responsibilities
- Read evidence and findings from PostgreSQL
- Render report in the target format (text, markdown, etc.)
- Send to Discord via webhook
- Send to Telegram via bot API
- Send to Email via SMTP or transactional service
- Provide health/metrics endpoints

## TODO
- Implement Go service
- Define report templates
- Implement delivery adapters
- Connect to PostgreSQL
- Add logging and metrics

