# wc2api

WebChat to API - A lightweight Go middleware that converts AI webchat interfaces into OpenAI-compatible API endpoints.

## Features

- 🔌 **OpenAI API compatible** - Drop-in replacement for OpenAI API
- 🌊 **Streaming support** - Server-Sent Events (SSE) for real-time responses
- 🔐 **Automated login** - Automatically authenticates with DeepSeek webchat
- 🔌 **Provider interface** - Easy to add more AI providers

## Quick Start

### Prerequisites

- Go 1.21+ (for building from source)
- DeepSeek account credentials

### Configuration

Create a `config.json` file based on the example:

```bash
cp configs/config.example.json config.json
```

Edit `config.json`:

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": "5001"
  },
  "auth": {
    "api_keys": ["your-api-key-here"]
  },
  "provider": {
    "deepseek": {
      "enabled": true,
      "email": "your-email@example.com",
      "base_url": "https://chat.deepseek.com"
    }
  }
}
```

**Security Note**: Set the password via environment variable instead of config file:

```bash
export DEEPSEEK_PASSWORD="your-password"
```

### Building and Running Binary

```bash
# Install dependencies
go mod tidy

# Build binary
go build -o wc2api ./cmd/wc2api

# Run
./wc2api -config config.json
```

### Running from Source

```bash
# Install dependencies
go mod tidy

# Run directly
go run ./cmd/wc2api
```

## API Endpoints

### Health Check
```
GET /healthz
```

### List Models
```
GET /v1/models
Authorization: Bearer your-api-key
```

### Chat Completions
```
POST /v1/chat/completions
Authorization: Bearer your-api-key
Content-Type: application/json

{
  "model": "deepseek-chat",
  "messages": [
    {"role": "user", "content": "Hello!"}
  ],
  "stream": false
}
```

#### Streaming Request
```
POST /v1/chat/completions
Authorization: Bearer your-api-key
Content-Type: application/json

{
  "model": "deepseek-chat",
  "messages": [
    {"role": "user", "content": "Tell me a story"}
  ],
  "stream": true
}
```

## Supported Models

- `deepseek-chat` - DeepSeek V3 general chat model
- `deepseek-reasoner` - DeepSeek R1 reasoning model
- `deepseek-coder` - DeepSeek Coder model

## Configuration Options

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `DEEPSEEK_EMAIL` | DeepSeek account email | Yes |
| `DEEPSEEK_PASSWORD` | DeepSeek account password | Yes |
| `WC2API_PORT` | Server port (default: 5001) | No |
| `WC2API_HOST` | Server host (default: 0.0.0.0) | No |
| `WC2API_API_KEYS` | Comma-separated API keys for client auth | No |

### Config File Options

See `configs/config.example.json` for all available options.

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
    DeepSeek Provider
         ↓ DeepSeek Web Protocol
    chat.deepseek.com
```

## Development

### Project Structure

```
wc2api/
├── cmd/wc2api/          # Entry point
├── internal/
│   ├── server/          # HTTP server & middleware
│   ├── handlers/        # API handlers
│   ├── providers/       # Provider interface & implementations
│   │   └── deepseek/    # DeepSeek provider
│   └── config/          # Configuration
└── configs/             # Configuration examples
```

### Adding a New Provider

1. Implement the `Provider` interface in `internal/providers/yourprovider/`
2. Add provider configuration to `config.Config`
3. Initialize provider in `server.New()`
4. Register models in the provider

## License

MIT License - See LICENSE file

## Disclaimer

This project is for educational and research purposes only. Reverse-engineering web interfaces may violate Terms of Service. Use at your own risk.

The authors are not responsible for any account bans, data loss, or other issues arising from using this software.
