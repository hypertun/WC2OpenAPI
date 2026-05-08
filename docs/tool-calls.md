# Tool Calls Documentation

WC2OpenAPI supports tool/function calling for both DeepSeek and Qwen providers through prompt injection and response parsing. This document describes how tool calls work, including the new error correction system (Phases 1-7).

## Overview

Tool calls are implemented via:
1. **Prompt Injection**: Tool schemas are injected into the system prompt using provider-specific formats
2. **Response Parsing**: The LLM response is parsed for tool call markup
3. **Validation & Correction**: Parameters are validated and automatically corrected if needed
4. **Error Feedback Loop**: Invalid tool calls trigger automatic retries with error feedback

## Provider-Specific Implementations

### DeepSeek Provider

**Tool Prompt Format**: DSML (DeepSeek Markup Language)

- File: `internal/providers/deepseek/toolcall.go`
- Function: `injectToolPrompt()`
- Uses shared `toolcall.BuildToolCallInstructions()` from `internal/toolcall/tool_prompt.go`

**Tool Call Parsing**: DSML `<invoke>` markup

- File: `internal/providers/deepseek/toolparse.go`
- Function: `parseToolCallsFromText()` (line 23)
- Parses `<invoke name="tool_name">` and `<parameter name="param">value</parameter>` tags
- Validates tool calls via `validateToolCall()` after parsing

**Retry Logic**:

- File: `internal/providers/deepseek/chat.go`
- Function: `streamWithRetry()` (line 245)
- Implements retry loop with error feedback for invalid tool calls
- Buffers all content before sending to client when tool calls are present

### Qwen Provider

**Tool Prompt Format**: `##TOOL_CALL##` markers

- File: `internal/providers/qwen/toolcall.go`
- Function: `injectToolPrompt()`
- Uses `##TOOL_CALL##` and `##END_CALL##` markers for tool schema injection
- Native function calling is disabled (`function_calling: false, enable_tools: false`)

**Tool Call Parsing**: `##TOOL_CALL##` / `##END_CALL##` markers

- File: `internal/providers/qwen/toolparse.go`
- Function: `parseToolCallsFromText()` (line 26)
- Supports multiple parsing strategies:
  1. Primary: `##TOOL_CALL##...##END_CALL##` markers
  2. Fallback: Various text formats following qwen2API approach
  3. Normalization: Handles fragmented tool calls
- Validates tool calls via `validateToolCalls()` after parsing

**Retry Logic**:

- File: `internal/providers/qwen/chat.go`
- Buffers ALL streamed content when tools are present
- This prevents sending partial content before validation completes
- Implements retry loop similar to DeepSeek provider

## Validation Pipeline

When tool calls are parsed, they go through a validation pipeline:

### Step 1: Schema Validation (`internal/toolcall/validator.go`)

- Function: `ValidateToolCallsWithErrors()` (line 649)
- Checks:
  - Required parameters are present
  - Parameter types match schema (string, number, boolean, array, object)
  - Format validation (email, URI patterns)
  - Enum value constraints
  - Numeric min/max bounds
  - String min/max length

### Step 2: Parameter Correction (`internal/toolcall/param_fixer.go`)

- Function: `FixToolCallArguments()`
- Automatic corrections applied:
  - **Type Coercion**: String "123" → number 123, string "true" → boolean true
  - **Name Mapping**: `path` → `file_path`, `cmd` → `command`, etc.
  - **Default Values**: Missing optional parameters get defaults (e.g., Bash timeout=30000ms)
  - **Structure Fixes**: Single values converted to arrays when schema expects array

### Step 3: Error Feedback (`internal/toolcall/error_feedback.go`)

- Function: `GenerateToolCallErrorFeedback()` (line 7)
- If validation fails after correction, generates human-readable error message
- **Max 10 errors** displayed (truncated with "and X more errors" message)
- Error message is injected as system prompt for retry

## Retry Behavior

### Configuration (Hardcoded)

| Parameter | Value | Location |
|-----------|-------|----------|
| Max Retries | 3 | `internal/toolcall/retry.go:12` |
| Initial Backoff | 100ms | `internal/toolcall/retry.go:13` |
| Max Backoff | 2 seconds | `internal/toolcall/retry.go:14` |
| Jitter Fraction | 0.25 | `internal/toolcall/retry.go:15` |

### Retry Logic (`internal/toolcall/retry.go`)

- Function: `ShouldRetry()` (line 18) - Determines if retry should occur
- Function: `CalculateBackoff()` (line 25) - Exponential backoff with jitter
- Function: `BuildRetryRequest()` (line 37) - Creates new request with error feedback

### Retry Request Format

The retry request includes:
1. Original messages
2. New system message with error feedback at the beginning
3. Same tools, model, and parameters

```
[System: Error Feedback Message]
[Original Messages...]
```

## Logging & Metrics

### Structured Logging

- Validation results are logged with tool name, parameter, expected/actual values
- Correction summaries log counts per category (not every individual fix)
- Retry attempts log: attempt number, backoff duration, request_id
- Performance metrics include: `first_attempt_success` flag

### Privacy

- Corrected argument **values** are NOT logged in production
- Only parameter names and types are logged for corrections

### Request ID Propagation

- Each chat request gets a unique `request_id`
- Logged in all retry-related messages for traceability

## End-to-End Flow

```
1. Client sends POST /v1/chat/completions with tools
   ↓
2. Tool schemas injected into system prompt (provider-specific format)
   ↓
3. LLM generates response with tool call markup
   ↓
4. Response parsed for tool call markup
   ↓
5. Tool calls validated against schema
   ├─ Valid → Execute tool calls, return to client
   └─ Invalid →
       ├─ Apply automatic corrections (type coercion, name mapping)
       ├─ Re-validate
       ├─ Still invalid?
       │   ├─ Generate error feedback (max 10 errors)
       │   ├─ Build retry request with feedback as system message
       │   ├─ Wait for backoff (100ms → 200ms → 400ms, max 2s, ±25% jitter)
       │   └─ Retry (up to 3 attempts)
       └─ Valid after correction → Execute tool calls
```
