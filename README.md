# ⛏️ MineOps

> Self-hosted multi-tenant Telegram bot for managing Minecraft servers on [Aternos](https://aternos.org) — auto-start, live dashboard, queue auto-confirm, RBAC access control.

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-AGPL--3.0-blue)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)

## What it does

Aternos shuts down Minecraft servers when no players are online and puts them
in a launch queue. MineOps lets you and your friends start/stop the server
directly from Telegram — no need to share passwords or visit the Aternos
website.

## ✨ Features

- **⚡ Server control** — Start/stop Aternos servers via inline buttons or commands
- **📊 Live dashboard** — A single pinned message per group, updated every 30s (🔴 Offline → 🟡 Starting → 🟢 Online), with player list and IP
- **🤖 Auto queue confirm** — Bot automatically confirms the Aternos launch queue in the background
- **🔐 RBAC** — Owner + approved users system; access requests with approve/deny buttons
- **🚨 Emergency lockdown** — `/emergency` instantly revokes all permissions and blocks server actions
- **🔄 Hot session update** — When Cloudflare invalidates cookies, update them without restarting: `/set_session`
- **👤 Multi-user** — Anyone can self-onboard via `/start`, add their own Aternos servers, and manage them independently
- **🔒 Fernet encryption** — Aternos session cookies are encrypted at rest (compatible with Python cryptography library)
- **📡 Smart polling** — Dashboard uses mcsrvstat.us API + Minecraft SLP protocol; Aternos panel is only contacted for actions (start/stop/confirm) to avoid bans
- **🌐 Multi-language** — English and Russian UI, auto-detected from Telegram language
- **📅 Scheduled start** — `/schedule 18:00` to auto-start servers daily or once
- **📊 Statistics** — `/stats` shows launch counts and recent activity
- **🔔 Player notifications** — join/leave alerts in group chats (auto-deleted)

## 🏗 Architecture

```
┌─────────────────────────────────────────────────┐
│                  Telegram Bot                    │
│  ┌───────────┐  ┌──────────┐  ┌──────────────┐ │
│  │ Handlers  │  │   FSM    │  │  Middleware   │ │
│  │ (admin,   │  │(onboard, │  │  (firewall,  │ │
│  │  group)   │  │ session) │  │  register)   │ │
│  └─────┬─────┘  └────┬─────┘  └──────────────┘ │
│        │              │                          │
│  ┌─────▼──────────────▼─────┐                   │
│  │      Dashboard (30s)     │◄── gocron          │
│  └─────────────┬────────────┘                   │
│                │                                 │
│  ┌─────────────▼────────────┐                   │
│  │    Smart Status Layer    │                   │
│  │  mcsrvstat.us → SLP →   │                   │
│  │  legacy ping → Panel    │                   │
│  └─────────────┬────────────┘                   │
│                │                                 │
│  ┌─────────────▼────────────┐  ┌─────────────┐ │
│  │   Aternos Client (goja) │  │Queue Watcher │ │
│  │   JS token + SEC gen    │  │ auto-confirm │ │
│  └──────────────────────────┘  └─────────────┘ │
│                                                  │
│  ┌──────────────────────────────────────────────┐│
│  │  SQLite (modernc.org, pure Go, CGO_ENABLED=0)││
│  │  owners │ servers │ chats │ users │ audit_log ││
│  └──────────────────────────────────────────────┘│
└─────────────────────────────────────────────────┘
```

## 🛠 Tech stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.26 |
| Telegram framework | [telebot.v3](https://github.com/tucnak/telebot) |
| Database | SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go, CGO_ENABLED=0) |
| JS engine | [goja](https://github.com/dop251/goja) — executes Aternos AJAX_TOKEN JavaScript |
| Encryption | [fernet-go](https://github.com/fernet/fernet-go) — AES-128-CBC, compatible with Python `cryptography` |
| Scheduler | [gocron](https://github.com/go-co-op/gocron) |
| Container | Docker multi-stage (Alpine 3.21) |

## 📦 Quick start

### Prerequisites
- Go 1.26+
- Docker + Docker Compose
- Telegram bot token ([@BotFather](https://t.me/BotFather))
- Aternos account

### 1. Clone & configure

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

### 4. Deploy to VPS

```bash
# From Windows:
.\scripts\deploy.ps1 user@your-server-ip

# From Linux/macOS:
./scripts/deploy.sh user@your-server-ip
```

## 🤖 Bot commands

| Command | Where | Description |
|---------|-------|-------------|
| `/start` | DM | Onboarding — enter Aternos cookie, select servers |
| `/panel` | DM | Owner panel — servers, chats, settings, audit log |
| `/set_session` | DM | Update Aternos session cookie |
| `/run` | DM/Group | Start server(s) |
| `/confirm` | DM/Group | Manually confirm Aternos queue |
| `/status` | DM/Group | Show server status |
| `/ping` | DM/Group | Check bot latency |
| `/players` | DM/Group | List online players |
| `/info` | DM/Group | Server info card |
| `/grant` | Group | Grant server access to user |
| `/revoke` | Group | Revoke server access from user |
| `/stats` | DM | Launch statistics from audit log |
| `/schedule` | DM | Schedule automatic server start |
| `/link` | Group | Link group chat to your account |
| `/unlink` | Group | Unlink group chat |
| `/emergency` | DM | Toggle lockdown — revoke all access |
| `/help` | DM/Group | Show available commands |

## 🗄 Database schema

SQLite with auto-migrations (v1 → v5):

- `owners` — registered users (encrypted Aternos cookies)
- `servers` — Aternos servers per owner
- `chats` — linked Telegram group chats
- `chat_servers` — many-to-many: which servers appear in which chats
- `users` — group chat members and their access rights
- `audit_log` — action journal
- `server_meta` — cached Minecraft ports
- `db_version` — migration tracking

## 📄 License

[AGPL-3.0](LICENSE) © 2025 Excezzzz