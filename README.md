# wc2api

WebChat to API - A lightweight Go middleware that converts AI webchat interfaces into OpenAI-compatible API endpoints.

## Features

- 🔌 **OpenAI API compatible** - Drop-in replacement for OpenAI API
- 🌊 **Streaming support** - Server-Sent Events (SSE) for real-time responses
- 🔐 **Automated login** - Automatically authenticates with provider webchat
- 🔌 **Multi-provider support** - DeepSeek and Qwen providers included
- 🔧 **Tool calling** - DSML and `##TOOL_CALL##` markup parsing for function calls
- 📊 **Structured logging** - Debug tool call validation and retry metrics

## Quick Start

```bash
cp configs/config.example.json config.json
export DEEPSEEK_EMAIL="your-email@example.com"
export DEEPSEEK_PASSWORD="your-password"
export QWEN_EMAIL="your-email@example.com"
export QWEN_PASSWORD="your-password"
go mod tidy
go run ./cmd/wc2api
```

## Build & Run

```bash
go mod tidy
go run ./cmd/wc2api                        # dev
go build -o wc2api ./cmd/wc2api            # build binary
./wc2api -config config.json               # run binary with config
```

## Configuration

- Copy `configs/config.example.json` to `config.json` and edit.
- **DeepSeek Credentials**: Use env vars `DEEPSEEK_EMAIL` + `DEEPSEEK_PASSWORD` (not config file).
- **Qwen Credentials**: Use env vars `QWEN_EMAIL` + `QWEN_PASSWORD` (not config file).
- **Timeouts**: All values in `config.json` are **integers (seconds)**, not Go duration strings.
- **API keys**: Set `auth.api_keys` in config. If empty, auth middleware is bypassed (dev mode).
- `.gitignore` includes `config.json` and `.env`.

## API Endpoints

- `GET /healthz` — Health check (no auth)
- `GET /v1/models` — List models (auth required if API keys configured)
- `POST /v1/chat/completions` — Chat completion (streaming supported)

## Supported Models

### DeepSeek
- `deepseek-v4-flash` — Fast general chat model (model_type: default)
- `deepseek-v4-pro` — Expert model with enhanced capabilities (model_type: expert)
- `deepseek-v4-flash-nothinking` / `deepseek-v4-pro-nothinking` — Disable thinking/reasoning (suffix stripped before sending)

### Qwen
- `qwen3.5-flash` — Fast general chat model (model_type: default)
- `qwen3.6-plus` — Enhanced model with more capabilities (model_type: expert)
- `qwen3.5-flash-nothinking` / `qwen3.6-plus-nothinking` — Disable thinking/reasoning (suffix stripped before sending)

## Provider Support

Both DeepSeek and Qwen providers can be enabled:
- **Model routing**: Requests route based on model name prefix (`deepseek-*` → DeepSeek, `qwen-*` → Qwen)
- **Fallback**: Unrecognized model names use the first available provider

## Tool Calling

Implemented via tool schema injection in system prompt + parsing of response text:

**DeepSeek:**
- DSML markup (`` / `<invoke>`) parsing
- Tool schemas injected into system message as DSML instructions

**Qwen:**
- `##TOOL_CALL##` / `##END_CALL##` marker parsing
- Native function calling disabled (`function_calling: false, enable_tools: false`) to prevent upstream interception
- Supports `phase` field in streaming: "answer" (text), "think" (reasoning), "tool_call" (structured calls)

## Streaming Quirk

Both providers buffer ALL chunks before sending any to the client. Tool calls can only be detected after the full response text is available (appear as markup). Only after detecting no tool calls does it replay content chunks one-by-one.

## Architecture

```
Client (OpenAI SDK/CLI)
    ↓ OpenAI API Format
wc2api
    ├─ HTTP Server (chi router)
    ├─ Auth Middleware
    ├─ OpenAI API Handlers
    └─ Provider Interface
         ↓
    DeepSeek Provider / Qwen Provider
         ↓ Web Protocol
    chat.deepseek.com / chat.qwen.ai
```

## Project Structure

```
cmd/wc2api/                # Entry point (flag parsing, signal handling)
internal/
  server/                  # chi v5 router, middleware (auth, CORS, logging)
    middleware/
  handlers/                # OpenAI-compatible HTTP handlers (health, models, chat)
  providers/               # Provider interface + shared types
    deepseek/              # DeepSeek webchat implementation (DS2API-style)
    qwen/                  # Qwen webchat implementation (API-based auth)
  config/                  # JSON + env config loader
  toolcall/                # DSML/##TOOL_CALL## tool call parsing + prompt injection
configs/                   # Config templates (example only)
```

## Key Dependencies

- **chi v5** router
- **go-chi/cors** for CORS  
- **refraction-networking/utls** for TLS fingerprint spoofing (DeepSeek, Safari-like, HTTP/1.1-only to bypass WAF)

## Constraints

- No test suite or CI configuration present
- No linting or formatting tools configured  
- Auth token auto-refreshes every 30 minutes (configurable via `token_refresh_interval` in config)
- No Docker support

## License

MIT License

## Disclaimer

This project is for educational and research purposes only. Reverse-engineering web interfaces may violate Terms of Service. Use at your own risk.

The authors are not responsible for any account bans, data loss, or other issues arising from using this software.
