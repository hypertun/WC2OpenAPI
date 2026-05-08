# Troubleshooting Guide

This guide helps you diagnose and resolve common issues with WC2OpenAPI, particularly related to tool call error correction.

## Enable Debug Logging

First, enable debug logging to see detailed information about tool call validation and retries.

### Using slog
The application uses Go's standard `log/slog` package. To enable debug logging:

```go
// In your code or via environment variable
slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelDebug,
})))
```

Debug logs include:
- `ValidateToolCallsWithErrors` results
- Parameter corrections applied
- Retry attempts with backoff duration
- `request_id` for tracing
- `first_attempt_success` flag

---

## Common Issues

### Issue 1: Tool Calls Not Executing (Max Retries Exhausted)

**Symptoms**
- Response contains error message: "Tool call error correction required"
- `finish_reason: "stop"` instead of tool call execution
- Debug log shows 3 retry attempts

**Diagnosis**
```bash
# Check logs for validation errors
grep "validation errors" <log-file>
grep "Retry attempt" <log-file>
```

**Possible Causes**
1. LLM consistently generates invalid tool calls
2. Tool schema is incorrect or incomplete
3. Required parameters missing in schema

**Solutions**
1. **Check tool schema**: Ensure all required parameters are correctly defined
2. **Review error feedback**: The response includes specific validation errors
3. **Simplify prompt**: Make tool usage instructions clearer
4. **Check parameter types**: LLM may need examples of correct parameter formats

**Example Log Output**
```
DEBUG Tool call validation failed retry_attempt=1/3 tool=Bash errors=1
DEBUG Correction summary: type_coercion=1, name_mapping=0, defaults=0
DEBUG Backing off before retry backoff=100ms request_id=abc123
```

---

### Issue 2: Persistent Type Mismatch Errors

**Symptoms**
- Error: `Invalid type (expected: number, got: string)`
- Error persists across retries

**Cause**
LLM is generating parameters in wrong format (e.g., passing string "123" instead of number 123).

**Solution**
The system should auto-correct this via `CoerceValue()` in `param_fixer.go`. If it doesn't:

1. Check if the parameter is in a special format that can't be coerced
2. Add examples in your tool description showing correct parameter formats
3. Verify the schema `type` is correctly set

**Example Fix in Tool Schema**
```json
{
  "name": "Bash",
  "parameters": {
    "properties": {
      "timeout": {
        "type": "number",
        "description": "Timeout in milliseconds (e.g., 30000, not \"30000\")"
      }
    }
  }
}
```

---

### Issue 3: Unknown Parameter Name Errors

**Symptoms**
- Error: `Unrecognized parameter: 'path'`
- Tool expects `file_path` but LLM uses `path`

**Solution**
The system should auto-map aliases via `paramNameAliases` in `param_fixer.go`. If the alias is not mapped:

1. Add the mapping to `paramNameAliases` map (line 10 in `param_fixer.go`)
2. Or update your prompt to use the correct parameter names

**Current Alias Mappings**
| LLM Might Use | Schema Expects |
|---------------|----------------|
| `path` | `file_path` |
| `filename` | `file_path` |
| `cmd` | `command` |
| `script` | `command` |
| `text` | `content` |
| `url` | `uri` |

---

### Issue 4: Streaming Stops Unexpectedly with Tools

**Symptoms**
- Client receives incomplete streaming response
- Tool calls not detected in streamed content

**Cause**
Qwen provider buffers ALL content when tools are present. If the connection drops during buffering, content is lost.

**Solution**
1. Check network stability
2. Increase timeout settings in config
3. Consider using non-streaming mode for tool-heavy workloads

**Verification**
```bash
# Check if buffering is happening
grep "Buffering content" <log-file>
grep "Tool calls detected" <log-file>
```

---

### Issue 5: High Retry Rate (Performance Issue)

**Symptoms**
- Most requests require 2+ retries
- High latency for tool-calling requests

**Diagnosis**
Check the `first_attempt_success` flag in logs:
```bash
grep "first_attempt_success" <log-file> | jq .first_attempt_success | sort | uniq -c
```

**Solutions**
1. **Improve tool descriptions**: Add examples of correct tool calls
2. **Use dynamic parameter hints**: Enabled by default in `tool_prompt.go`
3. **Add validation checklist**: Injected into prompt automatically
4. **Check prompt engineering**: Ensure tool instructions are clear

---

## FAQ

### Q: Why was my tool call not executed?
**A**: Check the response content. If it contains "Tool call error correction required", the parameters were invalid and max retries were exhausted. The response includes specific validation errors.

### Q: Can I disable the retry behavior?
**A**: Currently, retry is always enabled when tool calls are present. To disable, you would need to modify `internal/toolcall/retry.go:ShouldRetry()` to always return false.

### Q: How do I know if error correction is working?
**A**: Enable debug logging and look for "Correction summary" log messages:
```
DEBUG Correction summary: type_coercion=2, name_mapping=1, defaults=1
```

### Q: Will retries cause duplicate tool executions?
**A**: No. Tool calls are only executed after validation passes. If validation fails, the tool is NOT executed and the request is retried.

### Q: Are corrected parameter values logged?
**A**: No, for privacy reasons. Only parameter names and types are logged, not the actual corrected values.

### Q: What happens if there are more than 10 validation errors?
**A**: The error feedback is truncated to 10 errors with a message "and X more errors (truncated for brevity)". This prevents overly long prompts.

---

## Debug Checklist

When tool calls aren't working as expected:

- [ ] Enable debug logging
- [ ] Check tool schema for correctness (required fields, parameter types)
- [ ] Review error feedback in response
- [ ] Check logs for correction summary
- [ ] Verify retry attempts in logs (should be ≤ 3)
- [ ] Ensure `request_id` is consistent across retry logs
- [ ] Check if `first_attempt_success: false` appears in logs
- [ ] Verify tool descriptions include examples if needed

---

## Getting Help

If you've tried the above and still have issues:

1. Collect debug logs showing the issue
2. Note the tool schema you're using
3. Note the LLM response that's failing validation
4. Open an issue at: https://github.com/anomalyco/opencode/issues
