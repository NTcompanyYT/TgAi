# TgAi 🤖

A lightweight, fast Telegram bot that works with any OpenAI-compatible AI provider.

Built by [NTcompanyYT](https://github.com/NTcompanyYT/TgAi)

## Features

- Works with Gemini, OpenAI, Claude, Groq, and any OpenAI-compatible provider
- Smart rate limiting per model (RPM + RPD tracking)
- Automatic fallback between models
- Image generation with `/image`
- Conversation memory
- Worker pool & graceful shutdown

---

## Setup

### 1. Prerequisites

- [Go 1.26.5+](https://go.dev/dl/)
- A bot token from [@BotFather](https://t.me/BotFather)
- An API key from your provider ([Gemini](https://aistudio.google.com) is free)

### 2. Clone & Build

```bash
git clone https://github.com/NTcompanyYT/TgAi
cd TgAi
go mod tidy
go build -o main .
```

### 3. Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `TELEGRAM_TOKEN` | Your bot token | ✅ |
| `WEBHOOK_URL` | Your public server URL | ✅ |
| `GEMINI_KEY` | Gemini API key | ✅ (if using Gemini) |
| `ADMIN_ID` | Your Telegram numeric ID | ❌ |

### 4. Run

**Linux / Mac:**
```bash
export TELEGRAM_TOKEN="..."
export WEBHOOK_URL="https://your-domain.com"
export GEMINI_KEY="..."
./main
```

**Windows:**
```powershell
$env:TELEGRAM_TOKEN="..."
$env:WEBHOOK_URL="https://your-domain.com"
$env:GEMINI_KEY="..."
./main
```

---

## Adding Providers

Edit `config.yaml`:

```yaml
providers:
  - endpoint: "https://generativelanguage.googleapis.com/v1beta/openai"
    key_env: "GEMINI_KEY"
    models:
      - name: "gemini-3.5-flash"
        rpm: 5
        rpd: 20
      - name: "gemini-3.1-flash-lite"
        rpm: 15
        rpd: 500
```

Any OpenAI-compatible API works. Set `rpm: 0` or `rpd: 0` to disable a model.

---

## Usage

| Action | How |
|--------|-----|
| Chat | Mention the bot or reply to it |
| Generate image | `/image a futuristic city` |

---

## Deployment

This project is completely free to run:

| Platform | Free Plan | Notes |
|----------|-----------|-------|
| [Render](https://render.com) | ✅ | Build: `go mod tidy && go build -o main .` |
| [Railway](https://railway.app) | ✅ | Direct deploy from GitHub |
| [Fly.io](https://fly.io) | ✅ | Via `flyctl` |
| [Koyeb](https://koyeb.com) | ✅ | Direct deploy from GitHub |
| Your own VPS | - | Any Linux server |

> Free plans may sleep after inactivity. The bot wakes up automatically on the first message.

---

## License

MIT