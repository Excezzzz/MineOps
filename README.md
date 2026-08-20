# MineOps

Self-hosted multi-tenant Telegram bot for managing Minecraft servers on [Aternos](https://aternos.org): auto-start, live dashboard, queue auto-confirm, RBAC access control.

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-AGPL--3.0-blue)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)

## Overview

Aternos shuts down idle Minecraft servers and places launches in a queue. MineOps lets you and your friends start and stop servers directly from Telegram — no shared passwords, no visiting the Aternos website.

## Features

- Server control: start/stop Aternos servers via inline buttons or commands
- Live dashboard: a single pinned message per chat, refreshed every 30s (offline -> starting -> online), with player list and IP
- Auto queue confirm: background watcher confirms the Aternos launch queue
- RBAC: owner + approved users; access requests with approve/deny buttons
- Emergency lockdown: `/emergency` instantly revokes all access and blocks server actions
- Hot session update: refresh cookies after Cloudflare invalidation without restarting (`/set_session`)
- Multi-user: self-onboarding via `/start`, each user manages their own servers
- Fernet encryption: Aternos session cookies are encrypted at rest (compatible with the Python `cryptography` library)
- Smart polling: status comes from mcsrvstat.us API + Minecraft SLP protocol; the Aternos panel is only contacted for actions (start/stop/confirm) to avoid bans
- Multi-language: English and Russian UI, auto-detected from the Telegram language
- Scheduled start: `/schedule 18:00` for daily or one-time auto-start
- Statistics: `/stats` shows launch counts from the audit log
- Player notifications: join/leave alerts in group chats (auto-deleted)

## Architecture

```
cmd/bot
  main.go — entry point: wiring, scheduler, graceful shutdown
internal
  telegram — handlers, FSM, inline keyboards
    bot.go         — dispatcher, middleware, error/panic notify
    admin.go       — owner panel (/panel)
    group.go       — group logic, /link, access requests
    onboarding.go  — /start wizard (cookie + server selection)
    commands.go    — /run, /status, /players, /schedule, ...
    fsm.go         — per-user conversation state
    keyboards.go   — inline keyboard builders
  dashboard      — pinned status message, 30s refresh, status fallbacks
  queuewatcher   — background Aternos queue confirmer (15s/45s polls)
  aternos        — HTTP client for the Aternos panel
    client.go       — JS token eval (goja), SEC generation, actions
    manager.go      — per-owner clients, session cache
    interceptor.go  — Cloudflare / auth-failure detection
  mcsrvstat      — status via mcsrvstat.us API, SLP ping, legacy ping
  database       — SQLite (modernc.org), auto-migrations v1..v5
  config         — .env loading, validation
  crypto         — Fernet encryption for cookies
  util           — shared helpers
```

Status resolution order (per server): panel cache (60s TTL) -> mcsrvstat.us API -> SLP ping -> legacy ping. The Aternos panel is queried only for actions and for confirming the queue.

## Tech stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.26 |
| Telegram framework | [telebot.v3](https://github.com/tucnak/telebot) |
| Database | SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go, CGO_ENABLED=0) |
| JS engine | [goja](https://github.com/dop251/goja) — executes Aternos AJAX_TOKEN JavaScript |
| Encryption | [fernet-go](https://github.com/fernet/fernet-go) — AES-128-CBC, compatible with Python `cryptography` |
| Scheduler | [gocron](https://github.com/go-co-op/gocron) |
| Container | Docker multi-stage (Alpine 3.21) |

## Quick start

### Prerequisites

- Go 1.26+ (local dev) or Docker + Docker Compose
- Telegram bot token from [@BotFather](https://t.me/BotFather)
- Aternos account

### 1. Clone and configure

```bash
git clone https://github.com/Excezzzz/MineOps.git
cd MineOps
cp .env.example .env
# Edit .env: set BOT_TOKEN and SUPER_ADMIN_ID
```

### 2. Run with Docker

```bash
docker compose up -d --build
```

### 3. Run locally (dev)

```bash
go run ./cmd/bot
```

### 4. Deploy to a VPS

```bash
# From Windows:
.\scripts\deploy.ps1 user@your-server-ip

# From Linux/macOS:
./scripts/deploy.sh user@your-server-ip
```

## Bot commands

| Command | Where | Description |
|---------|-------|-------------|
| `/start` | DM | Onboarding — enter Aternos cookie, select servers |
| `/panel` | DM | Owner panel — servers, chats, settings, audit log |
| `/set_session` | DM | Update the Aternos session cookie |
| `/run` | DM/Group | Start server(s) |
| `/confirm` | DM/Group | Manually confirm the Aternos queue |
| `/status` | DM/Group | Show server status |
| `/ping` | DM/Group | Check bot latency |
| `/players` | DM/Group | List online players |
| `/info` | DM/Group | Server info card |
| `/grant` | Group | Grant server access to a user |
| `/revoke` | Group | Revoke server access from a user |
| `/stats` | DM | Launch statistics from the audit log |
| `/schedule` | DM | Schedule automatic server start |
| `/link` | Group | Link the group chat to your account |
| `/unlink` | Group | Unlink the group chat |
| `/emergency` | DM | Toggle lockdown — revoke all access |
| `/help` | DM/Group | Show available commands |

## Database schema

SQLite with auto-migrations (v1 to v5):

- `owners` — registered users (encrypted Aternos cookies)
- `servers` — Aternos servers per owner
- `chats` — linked Telegram group chats
- `chat_servers` — many-to-many: which servers appear in which chats
- `users` — group chat members and their access rights
- `audit_log` — action journal
- `server_meta` — cached Minecraft ports
- `db_version` — migration tracking

## License

[AGPL-3.0](LICENSE) (c) 2025 Excezzzz