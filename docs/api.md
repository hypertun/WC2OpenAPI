# API Documentation

WC2OpenAPI provides OpenAI-compatible API endpoints with enhanced tool call error correction.

## Base URL
```
http://localhost:5001
```

## Authentication
```
Authorization: Bearer <your-api-key>
```

Set API keys in `config.json` under `auth.api_keys`. If empty, auth is bypassed (dev mode).

---

## Endpoints

### GET /healthz
Health check endpoint (no auth required).

**Response**
```json
{
  "status": "ok"
}
```

---

### GET /v1/models
List available models.

**Headers**
```
Authorization: Bearer <your-api-key>
```

**Response**
```json
{
  "object": "list",
  "data": [
    {
      "id": "deepseek-v4-flash",
      "object": "model",
      "created": 1234567890,
      "owned_by": "deepseek"
    },
    {
      "id": "deepseek-v4-pro",
      "object": "model",
      "created": 1234567890,
      "owned_by": "deepseek"
    },
    {
      "id": "qwen3.5-flash",
      "object": "model",
      "created": 1234567890,
      "owned_by": "qwen"
    }
  ]
}
```

### POST /v1/chat/completions
Chat completion endpoint with streaming and tool calling support.

**Headers**
```
Authorization: Bearer <your-api-key>
Content-Type: application/json
```

**Request Body**
```json
{
  "model": "deepseek-v4-flash",
  "messages": [
    {"role": "user", "content": "What's the weather?"}
  ],
  "stream": false,
  "tools": [...],
  "tool_choice": "auto",
  "temperature": 0.7,
  "max_tokens": 4096
}
```

**Tool Calling**

Tools are defined in OpenAI format:
```json
{
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather for a location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "City name"
            }
          },
          "required": ["location"]
        }
      }
    }
  ]
}
```

---

## Tool Call Error Correction Behavior

WC2OpenAPI includes an automatic error correction system for tool calls (Phases 1-7).

### How It Works

1. **Validation**: Tool call parameters are validated against the schema
2. **Auto-Correction**: Common errors are automatically fixed:
   - Type coercion (string "123" → number 123)
   - Parameter name aliases (`path` → `file_path`)
   - Missing optional parameters get defaults
3. **Retry Loop**: If validation fails after correction, the system:
   - Generates error feedback message (max 10 errors shown)
   - Injects feedback as system message
   - Retries the request (up to 3 attempts)
   - Uses exponential backoff between retries

### Retry Configuration

| Parameter | Value | Description |
|-----------|-------|-------------|
| Max Retries | 3 | Maximum retry attempts for tool call correction |
| Initial Backoff | 100ms | Wait time before first retry |
| Max Backoff | 2 seconds | Maximum wait time between retries |
| Jitter | ±25% | Random jitter added to backoff to prevent thundering herd |

### Retry Backoff Timeline
```
Attempt 1: Execute → Validation fails
           ↓ (wait 100ms ± 25%)
Attempt 2: Retry with error feedback → Validation fails
           ↓ (wait 200ms ± 25%)
Attempt 3: Retry with error feedback → Validation fails
           ↓ (wait 400ms ± 25%, capped at 2s)
Attempt 4: Would retry, but max retries (3) reached → Return error
```

### Streaming Behavior with Tools

When streaming is enabled AND tools are present:

**Qwen Provider**: Buffers ALL content before sending to client
- Reason: Tool calls can only be detected after full response
- Prevents sending partial content before validation completes
- If tool calls are detected and valid, buffered content is replayed as SSE chunks

**DeepSeek Provider**: Similar buffering behavior
- Full response text is collected before parsing tool calls
- Content is streamed to client only after validation passes

### Error Response Format

When max retries are exhausted, the response includes the validation errors:

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1234567890,
  "model": "deepseek-v4-flash",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Tool call error correction required. The following tool calls had parameter errors and were NOT executed:\n\nBash:\n  - `command`: Required parameter is missing (expected: string, got: <nil>)\n\nPlease retry with corrected parameters."
      },
      "finish_reason": "stop"
    }
  ]
}
```

### Logging & Metrics

Enable debug logging to see retry behavior:
```bash
# In your code or config, set slog level to debug
```

Logged information includes:
- Validation errors with parameter details
- Correction summaries (counts per category, not individual values for privacy)
- Retry attempts with backoff duration
- `request_id` for tracing retries
- `first_attempt_success` flag for metrics calculation

---

## Model Routing

Requests are routed based on model name prefix:
- `deepseek-*` → DeepSeek provider
- `qwen-*` → Qwen provider
- Unrecognized → First available provider (fallback)

### Model Variants

**DeepSeek**
- `deepseek-v4-flash` → `deepseek_v4_flash` (model_type: default)
- `deepseek-v4-pro` → `deepseek_v4_pro` (model_type: expert)
- `deepseek-v4-flash-nothinking` / `deepseek-v4-pro-nothinking` → Strip `_nothinking` suffix

**Qwen**
- `qwen3.5-flash` → `qwen3.5-flash` (model_type: default)
- `qwen3.6-plus` → `qwen3.6-plus` (model_type: expert)
- `qwen3.5-flash-nothinking` / `qwen3.6-plus-nothinking` → Strip `-nothinking` suffix

---

## Rate Limiting

The server includes rate limiting middleware. Configure in `config.json`:
```json
{
  "server": {
    "rate_limit": {
      "requests_per_minute": 60
    }
  }
}
```

---

## CORS

CORS is handled by `go-chi/cors` middleware. Configure allowed origins in server setup if needed.
