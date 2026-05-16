package qwen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

// truncate shortens a string for logging
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parseToolCallsFromText detects and parses tool calls from ##TOOL_CALL## markers in text
// Follows qwen2API approach with normalization for fragmented tool calls
// Returns OpenAI-compatible ToolCall objects and any parse-level errors
func parseToolCallsFromText(text string, tools []providers.Tool) ([]providers.ToolCall, []*toolcall.ValidationError) {
	if text == "" {
		return nil, nil
	}

	sawMarkers := strings.Contains(text, "##TOOL_CALL##") || strings.Contains(text, "##END_CALL##")

	var allParseErrors []*toolcall.ValidationError

	// First, try parsing directly from original text (preserves multiple tool calls)
	// before normalization strips anything
	if sawMarkers {
		if calls, errs := parseToolCallMarkers(text); len(calls) > 0 {
			slog.Debug("Found tool calls via markers before normalization", "count", len(calls))
			validateToolCalls(calls, tools)
			return calls, errs
		} else {
			allParseErrors = append(allParseErrors, errs...)
		}
		if calls, errs := parseToolCallFallback(text); len(calls) > 0 {
			slog.Debug("Found tool calls via fallback before normalization", "count", len(calls))
			validateToolCalls(calls, tools)
			return calls, errs
		} else {
			allParseErrors = append(allParseErrors, errs...)
		}
	}

	// Normalize fragmented tool calls (following qwen2API _normalize_fragmented_tool_call)
	normalizedText := normalizeFragmentedToolCall(text)

	// Check for tool call markers
	hasNormalizedMarkers := strings.Contains(normalizedText, "##TOOL_CALL##") || strings.Contains(normalizedText, "##END_CALL##")
	if !hasNormalizedMarkers {
		if len(allParseErrors) > 0 {
			return nil, allParseErrors
		}
		return nil, nil
	}
	if !sawMarkers {
		sawMarkers = true
	}

	slog.Debug("Parsing tool calls from text", "text_preview", truncate(normalizedText, 300))

	// Try primary format: ##TOOL_CALL##...##END_CALL## markers
	toolCalls, errs := parseToolCallMarkers(normalizedText)
	allParseErrors = append(allParseErrors, errs...)
	if len(toolCalls) > 0 {
		slog.Debug("Found tool calls via markers", "count", len(toolCalls))
		validateToolCalls(toolCalls, tools)
		return toolCalls, allParseErrors
	}

	// Try fallback formats (following qwen2API approach)
	toolCalls, errs = parseToolCallFallback(normalizedText)
	allParseErrors = append(allParseErrors, errs...)
	if len(toolCalls) > 0 {
		slog.Debug("Found tool calls via fallback", "count", len(toolCalls))
		validateToolCalls(toolCalls, tools)
		return toolCalls, allParseErrors
	}

	return nil, allParseErrors
}

// normalizeFragmentedToolCall normalizes fragmented/incomplete tool calls
// Following qwen2API _normalize_fragmented_tool_call approach
func normalizeFragmentedToolCall(text string) string {
	text = strings.TrimSpace(text)

	// Already has both markers
	if strings.Contains(text, "##TOOL_CALL##") && strings.Contains(text, "##END_CALL##") {
		return text
	}

	// Try to extract tool call from XML or JSON format
	extracted := extractFirstXMLToolCall(text)
	if extracted != "" {
		return extracted
	}

	extracted = extractFirstJSONToolCall(text)
	if extracted != "" {
		return extracted
	}

	// Clean up thinking markers and other noise (following qwen2API)
	text = regexp.MustCompile(`\[.*?\]`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`<\/?think>`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(?i)Tool\s+[A-Za-z0-9_.\-]+(\s+does not exist|\s+not available)?\.?`).ReplaceAllString(text, "")
	text = regexp.MustCompile("```[\\s\\S]*?```").ReplaceAllString(text, "")

	// Try extraction again after cleanup
	extracted = extractFirstXMLToolCall(text)
	if extracted != "" {
		return extracted
	}

	extracted = extractFirstJSONToolCall(text)
	if extracted != "" {
		return extracted
	}

	// Normalize line by line
	lines := []string{}
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		line = regexp.MustCompile(`^[•●·\-\*]+\s*`).ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, "END_CALL##", "##END_CALL##")
		if line != "" {
			lines = append(lines, line)
		}
	}

	normalized := strings.Join(lines, "\n")

	// Fix common marker typos (normalize BEFORE checking for markers)
	// Missing leading ##
	if strings.Contains(normalized, "TOOL_CALL##") && !strings.Contains(normalized, "##TOOL_CALL##") {
		normalized = strings.ReplaceAll(normalized, "TOOL_CALL##", "##TOOL_CALL##")
	}
	// Missing trailing #
	if strings.Contains(normalized, "##END_CALL") && !strings.Contains(normalized, "##END_CALL##") {
		normalized = strings.ReplaceAll(normalized, "##END_CALL", "##END_CALL##")
	}
	// Fix ##END CALL## (space instead of underscore)
	if strings.Contains(normalized, "##END CALL##") {
		normalized = strings.ReplaceAll(normalized, "##END CALL##", "##END_CALL##")
	}
	// Ensure balanced markers
	if strings.Contains(normalized, "##END_CALL##") && !strings.Contains(normalized, "##TOOL_CALL##") && strings.Contains(normalized, `"name"`) {
		normalized = "##TOOL_CALL##\n" + normalized
	}

	return normalized
}

// extractFirstXMLToolCall extracts tool call from XML format
// Following qwen2API _extract_first_xml_tool_call
func extractFirstXMLToolCall(text string) string {
	// Match <tool_calls> wrapper
	wrappedPattern := regexp.MustCompile(`(?is)<tool_calls>\s*(<invoke[\s\S]*?<\/invoke>)\s*<\/tool_calls>`)
	if matches := wrappedPattern.FindStringSubmatch(text); len(matches) > 1 {
		return matches[1]
	}

	// Match direct <invoke> tags
	invokePattern := regexp.MustCompile(`(?is)<invoke\s+name="([^"]+)"[^>]*>([\s\S]*?)<\/invoke>`)
	if matches := invokePattern.FindStringSubmatch(text); len(matches) > 0 {
		// Reconstruct as JSON format
		name := matches[1]
		body := matches[2]
		input := make(map[string]any)
		paramPattern := regexp.MustCompile(`(?is)<parameter\s+name="([^"]+)"[^>]*>([\s\S]*?)<\/parameter>`)
		for _, pm := range paramPattern.FindAllStringSubmatch(body, -1) {
			if len(pm) >= 3 {
				input[pm[1]] = strings.TrimSpace(pm[2])
			}
		}
		result := map[string]any{"name": name, "input": input}
		if b, err := json.Marshal(result); err == nil {
			return "##TOOL_CALL##\n" + string(b) + "\n##END_CALL##"
		}
	}

	return ""
}

// extractFirstJSONToolCall extracts tool call from JSON format
// Following qwen2API _extract_first_json_tool_call
func extractFirstJSONToolCall(text string) string {
	markers := []string{
		`{"name":`,
		`"name":`,
		`{"tool_calls"`,
		`{"type":"tool_use"`,
	}

	startPos := -1
	for _, marker := range markers {
		pos := strings.Index(text, marker)
		if pos >= 0 && (startPos < 0 || pos < startPos) {
			startPos = pos
		}
	}

	if startPos < 0 {
		return ""
	}

	// Find matching JSON
	depth := 0
	inString := false
	escape := false
	for i := startPos; i < len(text); i++ {
		ch := text[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString {
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					jsonStr := text[startPos : i+1]
					// Validate it's a tool call
					var obj map[string]any
					err := json.Unmarshal([]byte(jsonStr), &obj)
					if err != nil {
						repaired := repairJSONStringValues(jsonStr)
						err = json.Unmarshal([]byte(repaired), &obj)
					}
					if err == nil {
						if _, ok := obj["name"]; ok {
							return "##TOOL_CALL##\n" + jsonStr + "\n##END_CALL##"
						}
					}
					return jsonStr
				}
			}
		}
	}

	return ""
}

// repairJSONStringValues attempts to fix unescaped double quotes inside JSON string values.
// The Qwen model sometimes generates Go source code with literal double quotes inside
// JSON string values (e.g. `chatID != ""` in newString). This function detects and
// escapes those quotes so json.Unmarshal can parse the result.
func repairJSONStringValues(jsonStr string) string {
	var buf bytes.Buffer
	inString := false
	escaped := false

	for i := 0; i < len(jsonStr); i++ {
		ch := jsonStr[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && inString {
			buf.WriteByte(ch)
			escaped = true
			continue
		}

		if ch == '"' {
			if inString {
				j := i + 1
				for j < len(jsonStr) && (jsonStr[j] == ' ' || jsonStr[j] == '\t' || jsonStr[j] == '\n' || jsonStr[j] == '\r') {
					j++
				}
				if j >= len(jsonStr) || jsonStr[j] == ',' || jsonStr[j] == '}' || jsonStr[j] == ']' || jsonStr[j] == ':' {
					inString = false
					buf.WriteByte(ch)
				} else {
					buf.WriteString(`\"`)
				}
			} else {
				inString = true
				buf.WriteByte(ch)
			}
			continue
		}

		if inString && ch < 0x20 {
			switch ch {
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				buf.WriteString(fmt.Sprintf(`\u%04x`, ch))
			}
			continue
		}

		buf.WriteByte(ch)
	}

	return buf.String()
}

// parseToolCallMarkers parses ##TOOL_CALL##...##END_CALL## markers
func parseToolCallMarkers(text string) ([]providers.ToolCall, []*toolcall.ValidationError) {
	var calls []providers.ToolCall
	var parseErrors []*toolcall.ValidationError

	// Pattern: ##TOOL_CALL## [content] ##END_CALL## (newlines optional)
	// Captures any content between markers, supports single-line and multi-line formats
	markerPattern := regexp.MustCompile(`(?s)##TOOL_CALL##\s*(.+?)\s*##END_CALL##`)
	matches := markerPattern.FindAllStringSubmatch(text, -1)

	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])

		// Parse the JSON object
		var toolData struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}

		err := json.Unmarshal([]byte(jsonStr), &toolData)
		if err != nil {
			repaired := repairJSONStringValues(jsonStr)
			err = json.Unmarshal([]byte(repaired), &toolData)
		}
		if err != nil {
			slog.Warn("Failed to parse tool call JSON", "error", err, "json", truncate(jsonStr, 200))
			parseErrors = append(parseErrors, &toolcall.ValidationError{
				ToolName:  extractToolName(jsonStr),
				Parameter: "",
				Message:   fmt.Sprintf("JSON unmarshal failed: %v", err),
				Expected:  `valid JSON object with "name" (string) and "input" (object) fields`,
				Actual:    truncate(jsonStr, 200),
				Severity:  "error",
			})
			continue
		}

		// De-obfuscate the tool name first, then apply fixes with the real name
		// This ensures tool-specific parameter fixes use the correct tool name
		deName := fromQwenName(toolData.Name)
		toolData.Input = fixToolCallArguments(deName, toolData.Input)

		slog.Debug("Parsed tool call", "name", deName, "args", toolData.Input)

		argsJSON, err := json.Marshal(toolData.Input)
		if err != nil {
			slog.Warn("Failed to marshal tool call input", "error", err)
			continue
		}

		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      deName,
				Arguments: string(argsJSON),
			},
		})
	}

	return calls, parseErrors
}

// parseToolCallFallback tries to parse tool calls from fallback formats
func parseToolCallFallback(text string) ([]providers.ToolCall, []*toolcall.ValidationError) {
	var calls []providers.ToolCall
	var parseErrors []*toolcall.ValidationError

	// Try to find ```tool_call blocks
	codeBlockPattern := regexp.MustCompile("(?s)```tool_call\\s*\\n(.*?)```")
	matches := codeBlockPattern.FindAllStringSubmatch(text, -1)

	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])

		var toolData struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}

		err := json.Unmarshal([]byte(jsonStr), &toolData)
		if err != nil {
			repaired := repairJSONStringValues(jsonStr)
			err = json.Unmarshal([]byte(repaired), &toolData)
		}
		if err != nil {
			parseErrors = append(parseErrors, &toolcall.ValidationError{
				ToolName:  extractToolName(jsonStr),
				Parameter: "",
				Message:   fmt.Sprintf("tool_call block JSON unmarshal failed: %v", err),
				Expected:  `valid JSON object with "name" (string) and "input" (object) fields`,
				Actual:    truncate(jsonStr, 200),
				Severity:  "error",
			})
			continue
		}

		// De-obfuscate the tool name first
		deName := fromQwenName(toolData.Name)
		toolData.Input = fixToolCallArguments(deName, toolData.Input)

		argsJSON, _ := json.Marshal(toolData.Input)
		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      deName,
				Arguments: string(argsJSON),
			},
		})
	}

	// Try ``` blocks with invoke tags (DSML format)
	if len(calls) == 0 {
		codeBlockPattern := regexp.MustCompile("(?s)```\\s*\\n(.*?)```")
		matches := codeBlockPattern.FindAllStringSubmatch(text, -1)

		for i, match := range matches {
			if len(match) < 2 {
				continue
			}
			xmlStr := strings.TrimSpace(match[1])

			// Parse <invoke> tags
			invokePattern := regexp.MustCompile(`(?s)<invoke name="([^"]+)"[^>]*>([\s\S]*?)</invoke>`)
			for _, invokeMatch := range invokePattern.FindAllStringSubmatch(xmlStr, -1) {
				if len(invokeMatch) < 3 {
					continue
				}
				name := invokeMatch[1]
				body := invokeMatch[2]

				// Extract parameters
				paramPattern := regexp.MustCompile(`(?s)<parameter name="([^"]+)"[^>]*>([\s\S]*?)</parameter>`)
				input := make(map[string]any)
				for _, paramMatch := range paramPattern.FindAllStringSubmatch(body, -1) {
					if len(paramMatch) >= 3 {
						paramName := paramMatch[1]
						paramValue := strings.TrimSpace(paramMatch[2])
						// Strip CDATA if present
						paramValue = strings.TrimPrefix(paramValue, "<![CDATA[")
						paramValue = strings.TrimSuffix(paramValue, "]]>")
						input[paramName] = paramValue
					}
				}

				// De-obfuscate the tool name first
				deName := fromQwenName(name)
				input = fixToolCallArguments(deName, input)
				argsJSON, _ := json.Marshal(input)
				calls = append(calls, providers.ToolCall{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      deName,
						Arguments: string(argsJSON),
					},
				})
			}
		}
	}

	// Try native phase=tool_call format (Qwen streaming)
	if len(calls) == 0 {
		nativeCalls, nativeErrs := parseNativeToolCalls(text)
		return nativeCalls, nativeErrs
	}

	return calls, parseErrors
}

// parseNativeToolCalls parses native tool_calls format from Qwen streaming
func parseNativeToolCalls(text string) ([]providers.ToolCall, []*toolcall.ValidationError) {
	var calls []providers.ToolCall
	var parseErrors []*toolcall.ValidationError

	// Pattern for native tool_call format
	nativePattern := regexp.MustCompile(`(?s)\{"type":"tool_use"[^}]*\}`)
	for _, match := range nativePattern.FindAllString(text, -1) {
		var toolData struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}

		err := json.Unmarshal([]byte(match), &toolData)
		if err != nil {
			repaired := repairJSONStringValues(match)
			err = json.Unmarshal([]byte(repaired), &toolData)
		}
		if err != nil {
			parseErrors = append(parseErrors, &toolcall.ValidationError{
				ToolName:  extractToolName(match),
				Parameter: "",
				Message:   fmt.Sprintf("native tool_call JSON unmarshal failed: %v", err),
				Expected:  `valid JSON with "type":"tool_use", "id", "name" (string) and "input" (object) fields`,
				Actual:    truncate(match, 200),
				Severity:  "error",
			})
			continue
		}

		if toolData.Type == "tool_use" && toolData.Name != "" {
			// De-obfuscate the tool name first
			deName := fromQwenName(toolData.Name)
			toolData.Input = fixToolCallArguments(deName, toolData.Input)
			argsJSON, _ := json.Marshal(toolData.Input)
			calls = append(calls, providers.ToolCall{
				ID:   toolData.ID,
				Type: "function",
				Function: providers.ToolCallFunction{
					Name:      deName,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	return calls, parseErrors
}

// extractToolName attempts to extract the tool name from JSON, even if malformed.
// Tries strict JSON unmarshal first, then falls back to regex.
func extractToolName(jsonStr string) string {
	var partial struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(jsonStr), &partial) == nil && partial.Name != "" {
		return partial.Name
	}
	namePattern := regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
	if m := namePattern.FindStringSubmatch(jsonStr); len(m) >= 2 {
		return m[1]
	}
	return "unknown"
}

// fixToolCallArguments fixes tool call arguments for specific tools
// Following qwen2API fix_tool_call_arguments approach
// Enhanced with type coercion, parameter name mapping, default values, and structure fixes
func fixToolCallArguments(name string, input map[string]any) map[string]any {
	if input == nil {
		return input
	}

	fixed := make(map[string]any)
	for k, v := range input {
		fixed[k] = v
	}

	// Step 1: Type coercion for all values (string↔number, boolean variations)
	for k, v := range fixed {
		fixed[k] = coerceValue(v)
	}

	// Step 2: Tool-specific fixes
	fixed = applyToolSpecificFixes(name, fixed)

	// Step 3: Array/object structure corrections
	fixed = fixStructures(name, fixed)

	return fixed
}

// coerceValue attempts to coerce a value to more appropriate types
func coerceValue(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		if val == "" {
			return val
		}
		switch strings.ToLower(val) {
		case "true", "yes", "y":
			return true
		case "false", "no", "n":
			return false
		}
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return float64(i)
		}
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	case bool:
		return v
	case float64:
		return v
	case []any:
		for i, item := range val {
			val[i] = coerceValue(item)
		}
		return val
	case map[string]any:
		for k, item := range val {
			val[k] = coerceValue(item)
		}
		return val
	default:
		return v
	}
}

// applyToolSpecificFixes applies per-tool parameter name mappings, defaults, and structure fixes
// Tool name matching is case-insensitive to handle tools that come back from Qwen with different casing
func applyToolSpecificFixes(name string, fixed map[string]any) map[string]any {
	// Normalize tool name to lowercase for matching, but preserve original for case-specific logic
	nameLower := strings.ToLower(name)

	switch nameLower {
	case "askuserquestion":
		// Map singular 'question' to 'questions' array
		if question, ok := fixed["question"]; ok && fixed["questions"] == nil {
			fixed["questions"] = []any{
				map[string]any{
					"question": question,
					"header":   "Question",
					"options": []any{
						map[string]any{"label": "Yes", "description": "Confirm"},
						map[string]any{"label": "No", "description": "Decline"},
					},
					"multiSelect": false,
				},
			}
			delete(fixed, "question")
			slog.Debug("Fixed AskUserQuestion: converted 'question' to 'questions' array")
		}
		// Ensure each question has a header
		if questions, ok := fixed["questions"].([]any); ok {
			for _, q := range questions {
				if qm, ok := q.(map[string]any); ok {
					if qm["header"] == nil {
						qm["header"] = "Question"
					}
					if qm["multiSelect"] == nil {
						qm["multiSelect"] = false
					}
				}
			}
		}

	case "read":
		// Handle both snake_case (from Qwen) and camelCase (expected by OpenCode)
		if path, ok := fixed["path"]; ok {
			fixed["file_path"] = path
			delete(fixed, "path")
		}
		if filename, ok := fixed["filename"]; ok {
			fixed["file_path"] = filename
			delete(fixed, "filename")
		}
		// Map Qwen's file_path to OpenCode's filePath
		if filePath, ok := fixed["file_path"]; ok && fixed["filePath"] == nil {
			fixed["filePath"] = filePath
			delete(fixed, "file_path")
			slog.Debug("Fixed Read: mapped file_path to filePath")
		}

	case "bash":
		if cmd, ok := fixed["cmd"]; ok {
			fixed["command"] = cmd
			delete(fixed, "cmd")
		}
		if script, ok := fixed["script"]; ok {
			fixed["command"] = script
			delete(fixed, "script")
		}
		if fixed["timeout"] == nil {
			fixed["timeout"] = float64(30000)
			slog.Debug("Fixed Bash: added default timeout 30000ms")
		}

	case "agent":
		if fixed["description"] == nil {
			fixed["description"] = "Execute sub-task"
		}
		if fixed["prompt"] == nil {
			fixed["prompt"] = fixed["description"]
		}

	case "write":
		if path, ok := fixed["path"]; ok && fixed["file_path"] == nil {
			fixed["file_path"] = path
			delete(fixed, "path")
		}
		if text, ok := fixed["text"]; ok && fixed["content"] == nil {
			fixed["content"] = text
			delete(fixed, "text")
		}
		// Map file_path to filePath if needed
		if filePath, ok := fixed["file_path"]; ok && fixed["filePath"] == nil {
			fixed["filePath"] = filePath
			delete(fixed, "file_path")
		}

	case "edit":
		if path, ok := fixed["path"]; ok && fixed["file_path"] == nil {
			fixed["file_path"] = path
			delete(fixed, "path")
		}
		if oldStr, ok := fixed["old"]; ok && fixed["old_string"] == nil {
			fixed["old_string"] = oldStr
			delete(fixed, "old")
		}
		if newStr, ok := fixed["new"]; ok && fixed["new_string"] == nil {
			fixed["new_string"] = newStr
			delete(fixed, "new")
		}
		// Map file_path to filePath if needed
		if filePath, ok := fixed["file_path"]; ok && fixed["filePath"] == nil {
			fixed["filePath"] = filePath
			delete(fixed, "file_path")
		}

	case "search", "glob":
		if pattern, ok := fixed["pattern"]; ok && fixed["query"] == nil {
			fixed["query"] = pattern
		}
		if query, ok := fixed["query"]; ok && fixed["pattern"] == nil {
			fixed["pattern"] = query
		}
	}

	return fixed
}

// fixStructures corrects array/object structure issues in tool call parameters
func fixStructures(name string, input map[string]any) map[string]any {
	for k, v := range input {
		// Wrap scalar values in arrays for known array parameters
		if _, isArray := v.([]any); !isArray {
			arrayParams := map[string]bool{
				"questions": true,
				"options":   true,
				"files":     true,
				"items":     true,
			}
			if arrayParams[k] {
				input[k] = []any{v}
				slog.Debug("Wrapped value in array for structure fix", "tool", name, "param", k)
			}
		}

		// Parse JSON string to object if it starts with '{'
		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "{") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
				input[k] = parsed
				slog.Debug("Parsed JSON string to object", "tool", name, "param", k)
			}
		}

		// Parse JSON string to array if it starts with '['
		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "[") {
			var parsed []any
			if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
				input[k] = parsed
				slog.Debug("Parsed JSON string to array", "tool", name, "param", k)
			}
		}
	}
	return input
}

// validateToolCalls validates all tool calls against provided tool definitions
func validateToolCalls(calls []providers.ToolCall, tools []providers.Tool) {
	if tools == nil {
		return
	}
	for _, call := range calls {
		// Unmarshal arguments JSON to map for validation
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			slog.Debug("Failed to unmarshal tool call arguments for validation",
				"tool", call.Function.Name,
				"error", err,
				"arguments", call.Function.Arguments)
			continue
		}
		validateSingleToolCall(call.Function.Name, args, tools)
	}
}

// validateToolCallsWithErrors validates tool calls and returns validation errors
func validateToolCallsWithErrors(calls []providers.ToolCall, tools []providers.Tool) []*toolcall.ValidationError {
	var allErrors []*toolcall.ValidationError
	if len(tools) == 0 {
		return nil
	}
	for _, call := range calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			continue
		}
		for _, t := range tools {
			if t.Function.Name == call.Function.Name {
				if t.Function.Parameters != nil {
					schemaJSON, err := json.Marshal(t.Function.Parameters)
					if err != nil {
						continue
					}
					result := toolcall.ValidateToolCall(call.Function.Name, args, string(schemaJSON))
					if result.HasErrors() {
						allErrors = append(allErrors, result.Errors...)
					}
				}
				break
			}
		}
	}
	return allErrors
}

// validateSingleToolCall validates a single tool call
func validateSingleToolCall(name string, input map[string]any, tools []providers.Tool) {
	// Find the matching tool definition
	var toolDef *providers.Tool
	for _, t := range tools {
		if t.Function.Name == name {
			toolDef = &t
			break
		}
	}
	if toolDef == nil {
		// Tool not found in definitions - this may be handled by error feedback later
		return
	}

	// Convert Parameters map to JSON string for validator
	if toolDef.Function.Parameters == nil {
		return
	}
	schemaJSON, err := json.Marshal(toolDef.Function.Parameters)
	if err != nil {
		slog.Warn("Failed to marshal tool schema", "tool", name, "error", err)
		return
	}

	// Validate
	result := toolcall.ValidateToolCall(name, input, string(schemaJSON))
	slog.Info("Validation result",
		"tool", name,
		"valid", !result.HasErrors(),
		"error_count", len(result.Errors),
		"errors", result.Errors)
}
