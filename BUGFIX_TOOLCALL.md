# Bug Fix: Qwen Tool Call Parameter Mapping Issue

## Problem

When Qwen LLM was instructed to call tools using the obfuscated tool names (e.g., `u_read` instead of `Read`), the parameter names were not being properly mapped to match OpenCode's expected schema format.

### Specific Issue

Qwen returned a tool call:
```json
{"name": "u_read", "input": {"file_path": "/path/to/file"}}
```

But OpenCode's `Read` tool schema requires parameter `filePath` (camelCase), not `file_path` (snake_case).

### Root Cause

The bug had two parts:

1. **Name De-obfuscation Order**: The tool name de-obfuscation (`fromQwenName`) was happening AFTER the parameter fixing step. This meant that when `fixToolCallArguments("u_read", input)` was called, the tool-specific parameter fixes (which have a case for `"read"`) were not being applied because `"u_read"` was not recognized.

2. **Missing Parameter Mapping**: There was no mapping from Qwen's `file_path` to OpenCode's `filePath` for tools where the schema uses camelCase parameters.

### Error in Logs

```
Validation result: tool=read valid=false error_count=1
errors="[[read] filePath: Required parameter 'filePath' is missing (expected: required, got: <nil>)]"
```

The validator found the `read` tool schema (which requires `filePath`), but the arguments only contained `file_path`, causing validation to fail. After max retries, the raw `##TOOL_CALL##` text was sent to the user instead of being executed.

## Solution

### Fix 1: De-obfuscate Tool Names Before Parameter Fixing

Changed the order of operations in `parseToolCallMarkers`, `parseToolCallFallback`, and `parseNativeToolCalls` functions:

**Before:**
```go
toolData.Input = fixToolCallArguments(toolData.Name, toolData.Input)  // toolData.Name = "u_read"
calls = append(calls, providers.ToolCall{
    Name: fromQwenName(toolData.Name),  // Now de-obfuscates to "read"
})
```

**After:**
```go
deName := fromQwenName(toolData.Name)  // De-obfuscate first: "u_read" → "read"
toolData.Input = fixToolCallArguments(deName, toolData.Input)  // Apply fixes with correct name
calls = append(calls, providers.ToolCall{
    Name: deName,
})
```

### Fix 2: Make Tool-Specific Fixes Case-Insensitive

Modified `applyToolSpecificFixes` to normalize tool names to lowercase before matching:

```go
func applyToolSpecificFixes(name string, fixed map[string]interface{}) map[string]interface{} {
    nameLower := strings.ToLower(name)
    
    switch nameLower {
    case "read":
        // Handle both snake_case (from Qwen) and camelCase (expected by OpenCode)
        if filePath, ok := fixed["file_path"]; ok && fixed["filePath"] == nil {
            fixed["filePath"] = filePath
            delete(fixed, "file_path")
        }
    // ... other cases ...
    }
}
```

This ensures that both `"Read"` and `"read"` tool names are handled consistently.

### Fix 3: Add Parameter Mapping `file_path` → `filePath`

For all file-related tools (`read`, `write`, `edit`), added mapping from `file_path` to `filePath`:

```go
case "read":
    // Handle both snake_case (from Qwen) and camelCase (expected by OpenCode)
    if path, ok := fixed["path"]; ok {
        fixed["file_path"] = path
        delete(fixed, "path")
    }
    // Map Qwen's file_path to OpenCode's filePath
    if filePath, ok := fixed["file_path"]; ok && fixed["filePath"] == nil {
        fixed["filePath"] = filePath
        delete(fixed, "file_path")
    }
```

### Fix 4: Correct Hardcoded Example in Tool Prompt

Updated the `BuildQwenToolCallInstructions` to use the correct parameter name in examples:

**Before:**
```go
b.WriteString(`{"name": "Read", "input": {"file_path": "/path/to/file"}}` + "\n")
```

**After:**
```go
b.WriteString(`{"name": "Read", "input": {"filePath": "/path/to/file"}}` + "\n")
```

This ensures Qwen learns to use the correct parameter names from the examples.

## Testing

Added comprehensive test cases to verify the fix:

1. `TestApplyToolSpecificFixes_CaseInsensitiveRead`: Verifies case-insensitive matching and `file_path` → `filePath` mapping for read tool
2. `TestApplyToolSpecificFixes_CaseInsensitiveBash`: Verifies case-insensitive matching for bash tool
3. `TestParseToolCallMarkers_QwenObfuscatedRead`: Full integration test that parses `u_read` with `file_path` and verifies it becomes `read` with `filePath`
4. `TestParseToolCallMarkers_WriteWithFilePathToFilePath`: Tests that write tool also maps parameters correctly

All existing tests continue to pass.

## Impact

- **Fixed**: Tool calls with obfuscated names (u_*, fs_*, shell_*, etc.) now have their parameters properly mapped to match OpenCode's schema
- **Improved**: More robust handling of case variations in tool names
- **Clarity**: Better prompt examples teach the LLM the correct parameter names

## Files Changed

1. `internal/providers/qwen/toolparse.go`: 
   - Changed order of de-obfuscation and parameter fixing
   - Made tool-specific fixes case-insensitive
   - Added parameter mappings for file_path → filePath

2. `internal/toolcall/tool_prompt.go`:
   - Updated hardcoded examples to use correct parameter names
   - Updated COMMON MISTAKES section to reference correct param names

3. `internal/providers/qwen/toolparse_test.go`:
   - Added 4 new test cases to verify the fix

## Validation

```
$ go test ./internal/providers/qwen ./internal/toolcall -v
All tests pass ✓
```

The fix ensures that Qwen's tool calls with obfuscated names and parameter variations are correctly handled and validated, preventing the "TOOL_CALL" text from being returned to the user instead of being executed.
