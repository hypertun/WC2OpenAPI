# wc2api

WebChat to API - A lightweight Go middleware that converts AI webchat interfaces into OpenAI-compatible API endpoints.

## Features

- 🔌 **OpenAI API compatible** - Drop-in replacement for OpenAI API
- 🌊 **Streaming support** - Server-Sent Events (SSE) for real-time responses
- 🔐 **Automated login** - Automatically authenticates with provider webchat
- 🔌 **Multi-provider support** - Qwen and G365 providers included
- 🔧 **Tool calling** - DSML and `##TOOL_CALL##` markup parsing for function calls
- 📊 **Structured logging** - Debug tool call validation and retry metrics

## Quick Start

```bash
cp configs/config.example.json config.json
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
- **Qwen Credentials**: Use env vars `QWEN_EMAIL` + `QWEN_PASSWORD` (not config file).
- **Timeouts**: All values in `config.json` are **integers (seconds)**, not Go duration strings.
- **API keys**: Set `auth.api_keys` to enforce API key auth; empty list = bypass (dev mode).
- **G365 Browser**: Set `provider.g365.browser_executable_path` to point to a Chromium-based browser (Chrome, Edge, Brave, etc.). Leave empty to auto-detect.
- `.gitignore` includes `config.json` and `.env`.

## API Endpoints

- `GET /healthz` — Health check (no auth)
- `GET /v1/models` — List models (auth required if API keys configured)
- `POST /v1/chat/completions` — Chat completion (streaming supported)

## Supported Models

### Qwen

- `qwen3.5-flash` — Fast general chat model (model_type: default)
- `qwen3.6-plus` — Enhanced model with more capabilities (model_type: expert)
- `qwen3.5-flash-nothinking` / `qwen3.6-plus-nothinking` — Disable thinking/reasoning (suffix stripped before sending)

### Microsoft 365 Copilot (G365)

- `gpt-5.5-quick` — Fast chat model (tone: `Gpt_5_5_Chat`)
- `gpt-5.5-think-deeper` — Reasoning model with deeper thinking (tone: `Gpt_5_5_Reasoning`)
- `gpt-5.5-quick-nothinking` / `gpt-5.5-think-deeper-nothinking` — Disable thinking/reasoning variants

## Provider Support

All providers can be enabled simultaneously:
- **Model routing**: Requests route based on model name prefix (`qwen-*` → Qwen, `gpt-5.5-*` → G365)
- **Fallback**: Unrecognized model names use the first available provider

## Tool Calling

All providers support function calling with different formats:

**Qwen:**
- `##TOOL_CALL##` / `##END_CALL##` marker parsing
- Native function calling disabled to prevent upstream interception
- Supports `phase` field in streaming: "answer" (text), "think" (reasoning), "tool_call" (structured calls)
- Buffers full response before extracting tool calls

**G365 (Microsoft 365 Copilot):**
- DSML markup (`<|DSML|tool_calls><|DSML|invoke name="...">`) parsing from browser-rendered text
- Tool schemas injected into user message as DSML instructions (M365 respects instructions in user context)
- Streams tool call events as they appear; no full-response buffering

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
    Qwen Provider / G365 Provider
         ↓ Web Protocol
    chat.qwen.ai / (browser-based)
```

## Project Structure

```
cmd/wc2api/                # Entry point (flag parsing, signal handling)
internal/
  server/                  # chi v5 router, middleware (auth, CORS, logging)
    middleware/
  handlers/                # OpenAI-compatible HTTP handlers (health, models, chat)
  providers/               # Provider interface + shared types
    qwen/                  # Qwen webchat implementation (API-based auth)
    g365/                  # Microsoft 365 Copilot (browser automation via chromedp)
      browser.go           # Chromium context management, bridge injection
      bridge.go            # JS injected into M365 page (WebSocket interception, DSML extraction)
      client.go            # Provider client, WebSocket relay management
      server.go            # Internal WS server, tool message routing
      models.go            # G365 model definitions
  config/                  # JSON + env config loader
  toolcall/                # DSML/##TOOL_CALL## tool call parsing + prompt injection
configs/                   # Config templates (example only)
```

## Key Dependencies

- **chi v5** router
- **go-chi/cors** for CORS  

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
