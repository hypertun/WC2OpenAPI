# Tool-Call Enhancements Plan

Integrate 5 features from MiMo2API into WC2OpenAPI's existing toolcall package.

## 1. StreamSieve (`internal/toolcall/sieve.go`) — NEW FILE
- Character-by-character streaming separator for text vs. tool calls
- `StreamSieve` struct with `Feed(chunk, toolNames) []SieveEvent` and `Flush(toolNames) []SieveEvent`
- Detects start markers: `TOOL_CALL:`, `<tool_call`, `<function_call`, `<function=`, `[调用工具:`, `<|MiMoML|tool_calls>`, `<|DSML|tool_calls>`, `<tool_calls>`, `<function_calls>`
- `_split_safe` equivalent: only holds back trailing chars that could be a marker prefix
- `_is_capture_complete`: checks closing tags/parens for each format
- `_extract_non_tool_parts`: prefix/suffix extraction

## 2. Additional Extraction Strategies (`internal/toolcall/parser.go`)
Add 3 new extraction functions alongside existing `parseToolCallMarkers` and `extractXMLToolCalls*`:

- `_extract_tool_call_pattern`: `TOOL_CALL: name(args)` with paren-balancing parser
- `_extract_json_tool_call`: bare `{"name":"x","arguments":{...}}` with balanced-brace finder
- `_extract_function_call_json`: `<function_call>{"name":"x","arguments":{...}}</function_call>`

Modify `ParseToolCallsFromText` to try these after the existing marker+XML strategies.

## 3. Enhanced `ResolveToolName` (`internal/toolcall/parser.go:878`)
Current: exact → case-insensitive → CamelToSnake → snake_case case-insensitive (3 levels)
New: add CamelToSnake then case-insensitive check on the snake result (the 4th level from MiMo2API — currently the code does CamelToSnake then checks case-insensitive against original toolNames, but doesn't check the snake result against snake_case variants of toolNames).

Actually reviewing the code, `ResolveToolName` already does:
1. Exact match
2. Case-insensitive match
3. CamelToSnake → exact match against toolNames
4. CamelToSnake → case-insensitive match against toolNames

This is already 4-level. No changes needed here.

## 4. Enhanced XML Noise Tolerance (`stripMiMoMLNoise` at line 914)
Current: simple regex `<(/?)[|｜]?[Mm][Ii][Mm][Mm][Oo][Mm][Ll][|｜]?` → `<$1`
New `stripMiMoMLNoise` should handle:
- Doubled `<<|MiMoML` → `<`
- `<MiMoML|tool_calls>` (missing leading `|`)
- `<|MiMoML tool_calls>` (space instead of `|`)
- `<｜MiMoML｜tool_calls>` (fullwidth pipes U+FF5C)
- `<MiMoMLtool_calls>` (no separator)
- `<|MiMoML|tool_calls|>` (trailing `|`)
- `<mimoml-tool_calls>` / `<mimoml-invoke>` (hyphenated variants)
- DSML variants (same patterns with "dsml")
- CDATA pass-through (don't strip inside `<![CDATA[...]]>`)
- Fenced code block skip (don't strip inside ` ``` ... ``` `)

## 5. Deepened Parameter Parsing (`extractXMLParameters` at line 919)
Current: flat `<parameter name="X">val</parameter>` → string or JSON parse
New `extractXMLParameters` should:
- Parse nested XML elements → structured objects/arrays
- `<item>` children → arrays
- CDATA fence-awareness (don't parse content inside fenced blocks as XML)
- HTML entity decode (`&lt;` → `<`, `&gt;` → `>`, `&amp;` → `&`, etc.)
- JSON repair for loose JSON values
- `<br>` → newline normalization
- Auto-type coercion (true/false/null/number)

## 6. Engine Integration (`internal/toolcall/engine.go`)
Update `Parse()` to try new strategies in order:
1. `##TOOL_CALL##` markers (existing)
2. Normalized text markers (existing)
3. MiMoML XML with enhanced noise tolerance (existing `ParseXMLToolCalls` → enhanced)
4. `TOOL_CALL: name(args)` pattern (new)
5. Bare JSON tool call (new)
6. `<function_call>` JSON+XML (new)

## 7. Tests
- `sieve_test.go`: unit tests for StreamSieve
- Update `toolcalls_parse_test.go` for new extraction strategies
- Verify existing tests still pass

## Files Modified
- `internal/toolcall/sieve.go` — NEW
- `internal/toolcall/sieve_test.go` — NEW
- `internal/toolcall/parser.go` — enhanced functions
- `internal/toolcall/engine.go` — updated Parse() strategy chain
