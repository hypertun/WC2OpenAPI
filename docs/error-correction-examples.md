# Error Correction Examples

This document shows real-world examples of how WC2OpenAPI automatically corrects invalid tool call parameters.

## Example 1: Missing Required Parameter

### Invalid Tool Call
```xml
<invoke name="Read">
  <parameter name="file_path">/home/user/notes.txt</parameter>
</invoke>
```

**Problem**: The `Read` tool requires both `file_path` AND `offset` parameter.

### Validation Error
```
Read:
  - `offset`: Required parameter is missing (expected: number, got: <nil>)
```

### Correction Applied
The system checks the tool schema for `Read` and finds `offset` has a default value. It inserts:
```json
{
  "file_path": "/home/user/notes.txt",
  "offset": 0
}
```

### Result
Tool call proceeds with the corrected parameters.

---

## Example 2: Type Coercion (String to Number)

### Invalid Tool Call
```xml
<invoke name="Bash">
  <parameter name="command">ls -la</parameter>
  <parameter name="timeout">"30000"</parameter>
</invoke>
```

**Problem**: `timeout` is passed as string `"30000"` but schema expects number.

### Correction Applied
`internal/toolcall/param_fixer.go` function `CoerceValue()` (line 33) converts:
- Input: `"30000"` (string)
- Output: `30000` (float64)

### Result
```json
{
  "command": "ls -la",
  "timeout": 30000
}
```

---

## Example 3: Parameter Name Alias Mapping

### Invalid Tool Call
```xml
<invoke name="Write">
  <parameter name="path">/home/user/file.txt</parameter>
  <parameter name="content">Hello World</parameter>
</invoke>
```

**Problem**: Parameter is named `path` but schema expects `file_path`.

### Correction Applied
`internal/toolcall/param_fixer.go` function `ApplyNameMappings()` (line 74) applies:
- `path` → `file_path` (from `paramNameAliases` map, line 10)

### Result
```json
{
  "file_path": "/home/user/file.txt",
  "content": "Hello World"
}
```

### Supported Alias Mappings
| Alias | Canonical |
|-------|-----------|
| `path` | `file_path` |
| `filename` | `file_path` |
| `cmd` | `command` |
| `script` | `command` |
| `text` | `content` |
| `url` | `uri` |

---

## Example 4: Boolean String Coercion

### Invalid Tool Call
```xml
<invoke name="Search">
  <parameter name="query">golang tutorials</parameter>
  <parameter name="case_sensitive">"yes"</parameter>
</invoke>
```

**Problem**: `case_sensitive` is string `"yes"` but schema expects boolean.

### Correction Applied
`CoerceValue()` (line 33) converts:
- `"yes"` → `true`
- `"no"` → `false`
- `"y"` → `true`
- `"n"` → `false`

**Note**: `"1"` and `"0"` are NOT converted to avoid ambiguity with numeric values.

### Result
```json
{
  "query": "golang tutorials",
  "case_sensitive": true
}
```

---

## Example 5: Array Structure Correction

### Invalid Tool Call
```xml
<invoke name="ProcessFiles">
  <parameter name="files">/home/user/file1.txt</parameter>
</invoke>
```

**Problem**: `files` parameter expects an array but got a single string.

### Correction Applied
`FixStructure()` (line 94) checks `arrayParams` map (line 19):
- `files` is registered as array parameter
- Single value wrapped into array

### Result
```json
{
  "files": ["/home/user/file1.txt"]
}
```

### Parameters Treated as Arrays
- `questions`
- `options`
- `files`
- `items`
- `messages`

---

## Example 6: Truncated Error Feedback

When there are many validation errors (>10), the feedback is truncated:

### Error Feedback Message
```
Tool call error correction required. The following tool calls had parameter errors and were NOT executed:

unknown:
  - `file_path`: Required parameter is missing (expected: string, got: <nil>)
  - `command`: Required parameter is missing (expected: string, got: <nil>)
  - `timeout`: Invalid type (expected: number, got: string)
  ... (6 more errors)

... and 5 more errors (truncated for brevity)

Please retry with corrected parameters. Ensure all required fields are provided with correct types.
```

The `maxFeedbackErrors` constant (line 5 in `error_feedback.go`) limits feedback to 10 errors.

---

## Example 7: Retry with Error Feedback

### Attempt 1: Invalid Tool Call
```json
{
  "model": "deepseek-v4-flash",
  "messages": [{"role": "user", "content": "Run ls command"}],
  "tools": [{"type": "function", "function": {"name": "Bash", "parameters": {...}}}]
}
```

LLM responds with invalid tool call (missing `command` parameter).

### Error Feedback Generated
```
Tool call error correction required. The following tool calls had parameter errors and were NOT executed:

Bash:
  - `command`: Required parameter is missing (expected: string, got: <nil>)

Please retry with corrected parameters. Ensure all required fields are provided with correct types.
```

### Attempt 2: Retry Request (Auto-Generated)
```json
{
  "model": "deepseek-v4-flash",
  "messages": [
    {"role": "system", "content": "Tool call error correction required..."},
    {"role": "user", "content": "Run ls command"}
  ],
  "tools": [...]
}
```

Backoff: 100ms before retry.

### Attempt 2 Result
LLM corrects the tool call, includes `command` parameter. Tool executes successfully.
