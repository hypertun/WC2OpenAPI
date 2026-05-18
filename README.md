# wc2api

WebChat to API - A lightweight Go middleware that converts AI webchat interfaces into OpenAI-compatible API endpoints.

## Features

- 🔌 **OpenAI API compatible** - Drop-in replacement for OpenAI API
- 🌊 **Streaming support** - Server-Sent Events (SSE) for real-time responses
- 🔐 **Automated login** - Automatically authenticates with provider webchat
- 🔌 **Multi-provider support** - Qwen, QwenCN, and MiMo providers included
- 🔧 **Tool calling** - DSML, MiMoML, and `##TOOL_CALL##` markup parsing for function calls with JSON schema validation and retry logic
- 📊 **Structured logging** - Debug tool call validation and retry metrics

## Quick Start

```bash
cp configs/config.example.json config.json
# Edit config.json with your provider credentials
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
- **Credentials**: All credentials (email/password, cookies, tokens) go in `config.json` under each provider section.
- **Timeouts**: All values in `config.json` are **integers (seconds)**, not Go duration strings.
- **API keys**: Set `auth.api_keys` to enforce API key auth; empty list = bypass (dev mode).
- `.gitignore` includes `config.json` and `.env`.

## API Endpoints

- `GET /healthz` — Health check (no auth)
- `GET /v1/models` — List models (auth required if API keys configured)
- `POST /v1/chat/completions` — Chat completion (streaming supported)

## Supported Models

### Qwen

- `qwen3.5-flash` — Fast general chat model
- `qwen3.6-plus` — Enhanced model with more capabilities
- `qwen3.5-flash-nothinking` / `qwen3.6-plus-nothinking` — Disable thinking/reasoning (suffix stripped before sending)
- Models are **fetched dynamically** from the Qwen API; above are fallbacks.

### QwenCN

- `qwen-cn-Qwen3` — Base model
- `qwen-cn-Qwen3-Max` — Maximum capability model
- `qwen-cn-Qwen3-Max-Thinking` — Max model with reasoning enabled
- `qwen-cn-Qwen3-Plus` — Enhanced model
- `qwen-cn-Qwen3.5-Plus` — Latest enhanced model
- `qwen-cn-Qwen3-Flash` — Fast chat model
- `qwen-cn-Qwen3-Coder` — Code-specialized model

### MiMo

- Models discovered dynamically from MiMo API
- Fallback: `mimo-v2-pro`, `mimo-v2-flash`, `mimo-v2-omni`

## Provider Support

All providers can be enabled simultaneously:
- **Model routing**: Requests route based on model name prefix (`qwen-*` → Qwen, `qwen-cn-*` → QwenCN, `mimo-*` → MiMo)
- **Longest prefix wins**: More specific prefixes (e.g. `qwen-cn-`) match before shorter ones (e.g. `qwen-`)
- **Fallback**: Unrecognized model names use the first available provider

| Prefix | Provider | Auth method |
|--------|----------|-------------|
| `qwen` | Qwen (chat.qwen.ai) | email/password (SHA256-hashed) or cookie |
| `qwen-cn` | QwenCN (chat2.qianwen.com) | tongyi_sso_ticket cookie |
| `mimo` | MiMo (aistudio.xiaomimimo.com) | service_token + user_id + xiaomichatbot_ph cookies |

## Tool Calling

All providers support function calling. Tool calls use `##TOOL_CALL##...##END_CALL##` markers (primary) and DSML/MiMoML XML formats.

### Extraction strategies (tried in order by `ParseAllToolCalls`)
1. `##TOOL_CALL##...##END_CALL##` markers
2. MiMoML / DSML XML with noise-tolerant prefix stripping (`stripMiMoMLNoise`)
3. `<tool_calls>` / `<function_calls>` XML wrapper with `<invoke>` children
4. `<tool_call>` singular format (MiMo native)
5. `TOOL_CALL: name(args)` text pattern with paren-balancing
6. Bare JSON `{"name":"x","arguments":{...}}` with balanced-brace finder
7. `<function_call>{"name":"x","arguments":{...}}</function_call>`

### StreamSieve (`sieve.go`)
Real-time character-by-character streaming separator. Detects start markers (`##TOOL_CALL##`, `<tool_call`, `<function_call`, `TOOL_CALL:`, `<|MiMoML|tool_calls>`, `<|DSML|tool_calls>`, etc.), switches to capture mode, and emits text vs. `tool_calls` events **without buffering the full response**.

### Validation
Two-phase: policy check (`requires_user_confirmation`), then JSON schema validation. Validation errors trigger retry (up to 3 by default).

## Streaming

- **Qwen**: Uses Phase-based streaming (`phase` field: `answer`, `think`, `tool_call`). Tool calls extracted after full response is available.
- **QwenCN / MiMo**: Streams text tokens as they arrive. `StreamSieve` enables real-time tool call detection without buffering.
- **Tool call detection**: `StreamSieve` works character-by-character — text is emitted immediately while tool call markers are captured and emitted as structured events when complete.

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
    Qwen / QwenCN / MiMo
         ↓ Web Protocol
    chat.qwen.ai / chat2.qianwen.com / aistudio.xiaomimimo.com
```

## Project Structure

```
cmd/wc2api/                # Entry point (flag parsing, signal handling)
internal/
  server/                  # chi v5 router, middleware (auth, CORS, logging)
    middleware/
  handlers/                # OpenAI-compatible HTTP handlers (health, models, chat)
  providers/               # Provider interface + shared types
    qwen/                  # Qwen webchat (API-based auth, SHA256 passwords)
    qwencn/                # QwenCN (qianwen.com via tongyi_sso_ticket cookie)
    mimo/                  # MiMo (aistudio.xiaomimimo.com via cookies)
  config/                  # JSON config loader
  toolcall/                # DSML/MiMoML/##TOOL_CALL## parsing, StreamSieve, schema validation, retry
configs/                   # Config templates (example only)
```

## Key Dependencies

- **chi v5** router
- **go-chi/cors** for CORS  

## Constraints

- No CI or Docker configuration present
- No linting or formatting tools configured
- Auth token auto-refreshes every 30 minutes (configurable via `token_refresh_interval`)
- Tests in `internal/toolcall/` (e2e, validator, sieve) and `internal/providers/qwen/` (chat, auth)
```bash
go test ./...
```

## License

MIT License

## Disclaimer

This project is for educational and research purposes only. Reverse-engineering web interfaces may violate Terms of Service. Use at your own risk.

The authors are not responsible for any account bans, data loss, or other issues arising from using this software.
