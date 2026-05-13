# DuckAI → Go Migration Plan

Migrate the DuckAI TypeScript/Bun project (`/Users/ivanyeo/Downloads/duckai`) into WC2OpenAPI as a new `duckai` provider following existing patterns (deepseek, qwen).

---

## Overview

DuckAI proxies DuckDuckGo's free AI chat API (`duckduckgo.com/duckchat/v1`). No login required — auth uses a **VQD token** system (obfuscated JS eval + SHA-256 hashing). Models are hardcoded (gpt-4o-mini, claude-3-haiku, llama-4, etc.). Rate-limited to ~20 req/min sliding window.

**Migration difficulty:** VQD hash eval (JS sandbox in TS → `goja` or reimplement in Go).

---

## Step 1: Config — Add DuckAI provider config

**File:** `internal/config/config.go`

Add to `ProviderConfig`:
```go
type DuckAIConfig struct {
    Enabled bool   `json:"enabled"`
    // No email/password needed — DuckDuckGo is free, no login
    BaseURL string `json:"base_url"`  // default "https://duckduckgo.com"
    Timeout int    `json:"timeout"`   // seconds, default 60
}
```

Add `DuckAI DuckAIConfig \`json:"duckai"\`` to `ProviderConfig`.

Add defaults in `Load()`:
- `Enabled: false`
- `BaseURL: "https://duckduckgo.com"`
- `Timeout: 60`

Add validation in `Validate()`: if DuckAI enabled, no extra requirements (no email/password needed).

**Update** `configs/config.example.json` with `"duckai": {"enabled": false, "base_url": "https://duckduckgo.com", "timeout": 60}`.

---

## Step 2: Provider scaffold — `internal/providers/duckai/`

Create directory `internal/providers/duckai/` with these files:

### `client.go` — Client struct + VQD token flow

```go
type Client struct {
    config     config.DuckAIConfig
    httpClient *http.Client
    baseURL    *url.URL

    mu         sync.Mutex
    lastVQD    string
    vqdExpiry  time.Time
}
```

Implement:
- `New(cfg config.DuckAIConfig) (*Client, error)` — std `http.Client`, no uTLS needed (DDG doesn't TLS fingerprint)
- `Name() string` — returns `"duckai"`
- `Close() error` — no-op
- `ListModels() []providers.Model` — return hardcoded models (see Step 7)

### `vqd.go` — VQD token computation (hardest part)

The TypeScript `getEncodedVqdHash()`:
1. Takes base64-encoded JS from `x-Vqd-hash-1` response header
2. Decodes it
3. Runs it in JSDOM sandbox — script returns `{ client_hashes: string[], ... }`
4. Sets `client_hashes[0]` to Chrome 138 UA
5. SHA-256 hashes each client hash
6. Returns base64(JSON.stringify(result))

**Two options** (pick one, try Option A first):

**Option A — `goja` JS VM** (recommended for fidelity):
- Add `github.com/dop251/goja` dependency
- Create a sandbox-like JS runtime
- Execute the decoded script, extract the result object
- Downside: adds dependency, slower startup

**Option B — Reverse-engineer the JS** (if `goja` fails or script is simple enough):
- The obfuscated JS is DuckDuckGo's fingerprinting/hash script
- Inspect it, reimplement the algorithm in pure Go
- More fragile — DDG can change the script at any time

Implementation for `getVQD(ctx) (string, error)`:
1. GET `{baseURL}/duckchat/v1/status` with header `x-vqd-accept: 1`
2. Extract `x-Vqd-hash-1` response header (base64-encoded obfuscated JS)
3. Call `getEncodedVqdHash()` to process it
4. Return the hash

**Note:** The hash has a `vqd` field in the TypeScript types but it's not actually used. The `VQDResponse` type has both `vqd: string` and `hash: string` but only `hash` is used as `x-vqd-hash-1`. We can simplify to just return the hash string.

### `chat.go` — Chat completion + streaming

Methods:
- `CreateChatCompletion(ctx, req) (*providers.ChatResponse, error)`
- `CreateChatCompletionStream(ctx, req) (<-chan providers.StreamResponse, error)`

**Non-streaming (`chat` method):**
1. Call `getVQD(ctx)` to get VQD hash
2. POST `{baseURL}/duckchat/v1/chat` with:
   - Headers: `content-type: application/json`, `x-vqd-hash-1: <hash>`, `x-fe-version: serp_20250401_100419_ET-19d438eb199b2bf7c300`, random User-Agent
   - Body: `{"model": "...", "messages": [...]}`
3. Handle 429 → return rate limit error
4. Parse SSE response — lines starting `data: ` contain JSON with `message` field
5. Concatenate all `message` values, trim, return as content

**Streaming (`chatStream` method):**
Same flow but stream each `json.message` as a `StreamResponse` chunk in real-time.

**Tool calling support:**
- DuckAI doesn't natively support tool calling
- Follow the Qwen/DeepSeek pattern: inject tool instructions as a user/system message, then parse the response for JSON `{"tool_calls": [...]}`
- Use existing `toolcall` package (`tool_prompt.go` etc.)
- Buffer all chunks for tool-enabled requests (same as deepseek/qwen)

### `models.go` — Hardcoded model list

```go
func (c *Client) ListModels() []providers.Model {
    ids := []string{
        "gpt-4o-mini",
        "gpt-5-mini",
        "claude-3-5-haiku-latest",
        "meta-llama/Llama-4-Scout-17B-16E-Instruct",
        "mistralai/Mistral-Small-24B-Instruct-2501",
        "openai/gpt-oss-120b",
    }
    // ... convert to []providers.Model
}
```

### `rate_limit.go` — Sliding window rate limiter

Port from `duckai.ts`:
- 20 requests per minute sliding window
- 1 second minimum interval between requests
- Track timestamps in-memory (no shared file store needed — single process in Go)
- `shouldWait()` check before each request

---

## Step 3: Update server registration — `internal/server/server.go`

1. Import duckai provider
2. In `New()`, add initialization:
```go
if cfg.Provider.DuckAI.Enabled {
    prov, err := duckai.New(cfg.Provider.DuckAI)
    if err != nil {
        return nil, fmt.Errorf("duckai: %w", err)
    }
    s.providers = append(s.providers, prov)
}
```
3. No router changes needed — prefix `duckai-` will route to duckai provider automatically via `createRouter()`.

---

## Step 4: Add `goja` dependency

```bash
go get github.com/dop251/goja
go mod tidy
```

If Option A (goja) is chosen for VQD hash computation.

---

## Step 5: Wire up tool calling

DuckAI's tool calling approach (from TS code):
- Inject system prompt instructing the model to emit `{"tool_calls": [...]}`
- Parse JSON from response text
- Reuse existing `toolcall` package for prompt generation / parsing / validation / retry

In `chat.go`:
- If `req.Tools` is non-empty: add tool prompt to messages (using `BuildToolCallInstructions`), buffer response, parse for tool calls
- Use the same retry logic from `toolcall/retry.go`

---

## Step 6: Tests

Create `internal/providers/duckai/` test files:

### `client_test.go`
- Test VQD computation (mock the JS script response)
- Test rate limiter logic

### `chat_test.go`
- Test request conversion
- Test SSE response parsing
- Mock HTTP server for streaming/non-streaming

---

## Step 7: Build & verify

```bash
go build ./...
go test ./internal/providers/duckai/...
```

Enable in `config.json`:
```json
"duckai": {
    "enabled": true,
    "base_url": "https://duckduckgo.com",
    "timeout": 60
}
```

Test with:
```bash
curl -X POST http://localhost:5001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"duckai-gpt-4o-mini","messages":[{"role":"user","content":"Hello"}]}'
```

---

## Execution order (for agents)

| Step | File(s) | Description | Dependencies |
|------|---------|-------------|-------------|
| 1 | `internal/config/config.go`, `configs/config.example.json` | Add config | None |
| 2a | `internal/providers/duckai/client.go` | Client struct + VQD | Step 1 |
| 2b | `internal/providers/duckai/vqd.go` | VQD hash computation (goja) | goja dep |
| 2c | `internal/providers/duckai/chat.go` | Chat + streaming + tools | Step 2a, 2b |
| 2d | `internal/providers/duckai/models.go` | Hardcoded models | None |
| 2e | `internal/providers/duckai/rate_limit.go` | Sliding window rate limiter | None |
| 3 | `internal/server/server.go` | Register duckai provider | Step 1, 2a |
| 4 | `go.mod` | Add goja dep | Step 2b |
| 5 | (toolcall package reuse) | Wire tool calling | Step 2c |
| 6 | Test files | Tests | All above |
| 7 | | Build + verify | All above |

---

## Key design decisions

1. **VQD via `goja`** — The obfuscated JS changes frequently. A JS VM is more resilient than reverse-engineering.
2. **No shared rate limit store** — Go runs as a single process; in-memory is sufficient. Unlike the Bun version which supported multiple processes.
3. **Prefix `duckai-`** — Models accessed as `duckai-gpt-4o-mini` via existing prefix router.
4. **Random User-Agent per request** — Port `new UserAgent().toString()` from TS. Use a rotating list of common UAs.
5. **No uTLS** — DuckDuckGo doesn't seem to require TLS fingerprint spoofing.
6. **Tool calling via injected prompt** — Same pattern as Qwen (since DDG's API doesn't natively support tools).
