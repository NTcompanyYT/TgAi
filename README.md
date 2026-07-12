# TgAi 🤖

A lightweight, fast Telegram bot that works with any OpenAI-compatible AI provider.

Built by [NTcompanyYT](https://github.com/NTcompanyYT/TgAi)

## Features

- Works with Gemini, OpenAI, Claude, Groq, and any OpenAI-compatible provider
- Automatic fallback between models
- Image generation with `/image`
- Conversation memory
- Rate limiting & worker pool

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
| `ADMIN_ID` | Your Telegram numeric ID for notifications | ❌ |

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

Edit `config.yaml` to add any provider you want:

```yaml
providers:
  # Gemini (free)
  - endpoint: "https://generativelanguage.googleapis.com/v1beta/openai"
    key_env: "GEMINI_KEY"
    model: "gemini-2.0-flash-exp"
    free: true

  # OpenAI
  # - endpoint: "https://api.openai.com"
  #   key_env: "OPENAI_KEY"
  #   model: "gpt-4o-mini"
  #   free: false

  # Groq (free)
  # - endpoint: "https://api.groq.com/openai"
  #   key_env: "GROQ_KEY"
  #   model: "llama-3.1-8b-instant"
  #   free: true
```

Any OpenAI-compatible API works. Just set the endpoint, key, and model.

---

## Usage

| Action | How |
|--------|-----|
| Chat | Mention the bot or reply to it |
| Generate image | `/image a futuristic city` |

---

## Deployment

This project is completely free to run. Deploy anywhere:

| Platform | Free Plan | Notes |
|----------|-----------|-------|
| [Render](https://render.com) | ✅ | Build: `go mod tidy && go build -o main .` |
| [Railway](https://railway.app) | ✅ | Direct deploy from GitHub |
| [Fly.io](https://fly.io) | ✅ | Via `flyctl` |
| [Koyeb](https://koyeb.com) | ✅ | Direct deploy from GitHub |
| Your own VPS | - | Any Linux server |

> **Note:** Free plans may sleep after inactivity. Since the bot uses webhooks, it wakes up automatically on the first message.

---

## License

MIT