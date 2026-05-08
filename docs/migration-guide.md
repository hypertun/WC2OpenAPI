# Migration Guide

This guide helps you migrate to the new version of WC2OpenAPI with tool call error correction (Phases 1-7).

## Overview of Changes

The latest version includes a comprehensive tool call error correction system:
- Automatic parameter validation and correction
- Retry loop with error feedback to LLM
- Enhanced prompt engineering with parameter hints
- Structured logging and metrics

**Good news**: There are NO breaking changes. Your existing API integrations will continue to work.

---

## Pre-Migration Checklist

- [ ] Backup your current `config.json`
- [ ] Note your current WC2OpenAPI version/build date
- [ ] List any custom modifications you've made to the source code
- [ ] Identify workloads that use tool calling (if any)

---

## Step 1: Update the Binary

### From Source
```bash
cd /path/to/WC2OpenAPI
git pull origin main
go mod tidy
go build -o wc2api ./cmd/wc2api
```

### Verify New Build
```bash
./wc2api --version
# Should show build with tool call error correction (Phases 1-7)
```

---

## Step 2: Review Configuration (No Changes Required)

The new version does NOT require any configuration changes. All new features are enabled by default.

### Current Configuration Options

Your existing `config.json` will work as-is:

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
    },
    "qwen": {
      "enabled": false
    }
  }
}
```

### Optional: New Configuration Fields

If you want to customize retry behavior, you can add these fields to `config.json` (NOT required):

```json
{
  "tool_call": {
    "max_retries": 3,
    "retry_initial_backoff_ms": 100,
    "retry_max_backoff_ms": 2000,
    "retry_jitter_fraction": 0.25
  }
}
```

**Note**: As of now, these fields are hardcoded in `internal/toolcall/retry.go`. To use config values, you would need to modify the code to read from config.

---

## Step 3: Test Tool Call Functionality

If you use tool calling, verify it still works:

### Test Request
```bash
curl -X POST http://localhost:5001/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-v4-flash",
    "messages": [{"role": "user", "content": "Run ls -la"}],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "Bash",
          "description": "Run bash command",
          "parameters": {
            "type": "object",
            "properties": {
              "command": {"type": "string", "description": "Command to run"}
            },
            "required": ["command"]
          }
        }
      }
    ],
    "stream": false
  }'
```

### Expected Behavior
1. Tool call should execute correctly
2. If LLM generates invalid parameters, they should be auto-corrected
3. Check logs for any validation/correction messages

---

## Step 4: Monitor Logs

Enable debug logging to see the new features in action:

```bash
# If running from source, you can set this in your code
# Or check the default slog output
```

Look for these new log messages:
- `ValidateToolCallsWithErrors` - Validation results
- `Correction summary` - Auto-corrections applied
- `Retry attempt X/3` - Retry loop progress
- `first_attempt_success` - Metrics flag

### Sample Log Output (Success with Correction)
```
DEBUG Tool call validation failed retry_attempt=0/3 tool=Bash errors=1
DEBUG Applied param name mapping from=cmd to=command
DEBUG Correction summary: type_coercion=0, name_mapping=1, defaults=0
DEBUG Tool call validation passed after correction tool=Bash
```

### Sample Log Output (Max Retries Exhausted)
```
DEBUG Tool call validation failed retry_attempt=0/3 tool=Bash errors=1
DEBUG Retry attempt 1/3 backoff=100ms request_id=abc123
DEBUG Tool call validation failed retry_attempt=1/3 tool=Bash errors=1
DEBUG Retry attempt 2/3 backoff=200ms request_id=abc123
DEBUG Tool call validation failed retry_attempt=2/3 tool=Bash errors=1
DEBUG Max retries (3) exhausted, returning error request_id=abc123
```

---

## Step 5: Verify Metrics

The new version includes performance metrics for monitoring:

### Key Metrics
- `first_attempt_success`: Boolean indicating if tool call succeeded on first try
- `retry_count`: Number of retries performed
- `correction_counts`: Breakdown of corrections applied

### Calculate Retry Rate
```bash
# From your logs
grep "first_attempt_success" logs.txt | jq .first_attempt_success | sort | uniq -c
```

A high retry rate (>50%) suggests the LLM needs better tool instructions.

---

## Breaking Changes

**None.** This release maintains full backward compatibility:

| Area | Change | Breaking? |
|------|--------|-----------|
| API Endpoints | No changes | ❌ |
| Request Format | No changes | ❌ |
| Response Format | No changes (errors returned in content) | ❌ |
| Config File | No new required fields | ❌ |
| Environment Variables | No new requirements | ❌ |
| Tool Schema Format | No changes | ❌ |

---

## New Features Summary

### Automatic Parameter Correction
- Type coercion (string→number, string→boolean)
- Parameter name alias mapping
- Default value insertion

### Retry Loop
- Up to 3 retry attempts
- Exponential backoff (100ms → 200ms → 400ms, capped at 2s)
- Error feedback injected as system message

### Enhanced Prompts
- Dynamic parameter hints from tool schema
- Validation checklist in system prompt
- Improved tool call instructions

### Logging & Metrics
- Structured validation results
- Correction summaries (counts, not values)
- Request ID propagation for tracing
- Performance metrics (`first_attempt_success`)

---

## Rollback Plan

If you encounter issues:

### Option 1: Revert Binary
```bash
# Restore your backed-up binary
cp /backup/wc2api ./wc2api
./wc2api -config config.json
```

### Option 2: Disable Retry in Code
If the retry behavior causes issues, you can disable it by modifying `internal/toolcall/retry.go`:

```go
func ShouldRetry(validationErrors []*ValidationError, retryCount, maxRetries int) bool {
    return false  // Always return false to disable retries
}
```

Then rebuild:
```bash
go build -o wc2api ./cmd/wc2api
```

---

## FAQ

### Q: Do I need to update my client code?
**A**: No. The API remains OpenAI-compatible. All changes are internal.

### Q: Will this slow down my requests?
**A**: Slightly, if tool calls require retries. Successful first-attempt requests have minimal overhead (<1ms for validation).

### Q: Can I use this with existing tool definitions?
**A**: Yes. The validation uses standard JSON Schema from your tool definitions.

### Q: What if my tool calls stop working after upgrade?
**A**: Check the response content for validation errors. The new system is stricter about parameter validation.

---

## Support

If you encounter migration issues:
1. Check `docs/troubleshooting.md`
2. Review `docs/error-correction-examples.md`
3. Open an issue: https://github.com/anomalyco/opencode/issues
