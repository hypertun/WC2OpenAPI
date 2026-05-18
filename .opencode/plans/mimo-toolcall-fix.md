# Fix MiMo `<tool_call><function=NAME>` Tool Call Parsing

## Problem
MiMo v2.5 returns tool calls in a native XML format that the parser doesn't handle:
```xml
<tool_call>
<function=todowrite>
[{"content":"...","status":"pending","priority":"high"},...]
</function>
</tool_call>
```

The current `extractToolCallXMLSingular` in `mimo/toolcall.go:223` only looks for `<tool_name>NAME</tool_name>` inside `<tool_call>`, so it logs `"mimo: tc singular XML no tool_name, skipping"` and falls through to residual marker cleanup — the tool call is silently dropped and the raw XML is streamed to the user as plain text.

## Root Cause
Two independent parser copies need the fix:
1. **`mimo/toolcall.go:223`** — `extractToolCallXMLSingular()` — the one actually used by the MiMo provider (local copy)
2. **`internal/toolcall/parser.go:336`** — `extractXMLToolCallSingular()` — the shared parser (already patched but not wired into MiMi yet)

## Plan

### Step 1: Fix `mimo/toolcall.go` — add `<function=NAME>` branch to `extractToolCallXMLSingular`
- After the existing `<tool_name>` lookup fails (line 239), add an `else` branch
- Use regex `<function\s*=\s*(\w+)>` to extract the tool name
- Extract body between `<function=...>` and `</function>`
- Try parsing body as JSON (object → direct args, array → direct args, other → wrap)
- Fallback: wrap raw text as `{"input": body}`

### Step 2: Verify `internal/toolcall/parser.go` shared parser fix is correct
- Already patched in Step 1 of previous session — verify it handles both `<tool_name>` and `<function=NAME>` paths
- Ensure it compiles and tests pass

### Step 3: Build + test
- `go build ./...` — must pass
- `go test ./...` — must pass
- Manual verification: check debug logs show `"mimo: tc found via singular XML"` instead of `"skipping"`

### Step 4: Revert the premature `parser.go` patch if it causes issues
- The shared `parser.go` fix is defensive (not yet wired into MiMo) — keep it, it's correct

## Files Changed
- `internal/providers/mimo/toolcall.go` — `extractToolCallXMLSingular()` (line 239: add `<function=NAME>` fallback)
- `internal/toolcall/parser.go` — already patched, verify only

## Not Changed
- `mimo/chat.go` — no changes needed, it already calls `extractToolCall` correctly
- `internal/toolcall/engine.go` — no changes needed
- `internal/toolcall/obfuscate.go` — no changes needed
