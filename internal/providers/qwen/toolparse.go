package qwen

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	providers "github.com/user/wc2api/internal/providers"
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
// Returns OpenAI-compatible ToolCall objects
func parseToolCallsFromText(text string) ([]providers.ToolCall, error) {
	if text == "" {
		return nil, nil
	}

	// Normalize fragmented tool calls (following qwen2API _normalize_fragmented_tool_call)
	normalizedText := normalizeFragmentedToolCall(text)

	// Check for tool call markers
	if !strings.Contains(normalizedText, "##TOOL_CALL##") && !strings.Contains(normalizedText, "##END_CALL##") {
		return nil, nil
	}

	slog.Debug("Parsing tool calls from text", "text_preview", truncate(normalizedText, 300))

	// Try primary format: ##TOOL_CALL##...##END_CALL## markers
	toolCalls := parseToolCallMarkers(normalizedText)
	if len(toolCalls) > 0 {
		slog.Debug("Found tool calls via markers", "count", len(toolCalls))
		return toolCalls, nil
	}

	// Try fallback formats (following qwen2API approach)
	toolCalls = parseToolCallFallback(normalizedText)
	if len(toolCalls) > 0 {
		slog.Debug("Found tool calls via fallback", "count", len(toolCalls))
		return toolCalls, nil
	}

	return nil, nil
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

	// Fix common marker issues
	if strings.Contains(normalized, "TOOL_CALL##") && !strings.Contains(normalized, "##TOOL_CALL##") {
		normalized = strings.ReplaceAll(normalized, "TOOL_CALL##", "##TOOL_CALL##")
	}
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
		input := make(map[string]interface{})
		paramPattern := regexp.MustCompile(`(?is)<parameter\s+name="([^"]+)"[^>]*>([\s\S]*?)<\/parameter>`)
		for _, pm := range paramPattern.FindAllStringSubmatch(body, -1) {
			if len(pm) >= 3 {
				input[pm[1]] = strings.TrimSpace(pm[2])
			}
		}
		result := map[string]interface{}{"name": name, "input": input}
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
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					jsonStr := text[startPos : i+1]
					// Validate it's a tool call
					var obj map[string]interface{}
					if err := json.Unmarshal([]byte(jsonStr), &obj); err == nil {
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

// parseToolCallMarkers parses ##TOOL_CALL##...##END_CALL## markers
func parseToolCallMarkers(text string) []providers.ToolCall {
	var calls []providers.ToolCall

	// Pattern: ##TOOL_CALL##\n{json}\n##END_CALL##
	markerPattern := regexp.MustCompile(`(?s)##TOOL_CALL##\s*\n(.*?)\n\s*##END_CALL##`)
	matches := markerPattern.FindAllStringSubmatch(text, -1)

	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])

		// Parse the JSON object
		var toolData struct {
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}

		if err := json.Unmarshal([]byte(jsonStr), &toolData); err != nil {
			slog.Warn("Failed to parse tool call JSON", "error", err, "json", truncate(jsonStr, 200))
			continue
		}

		// Apply tool argument fixes
		toolData.Input = fixToolCallArguments(toolData.Name, toolData.Input)

		slog.Debug("Parsed tool call", "name", toolData.Name, "args", toolData.Input)

		argsJSON, err := json.Marshal(toolData.Input)
		if err != nil {
			slog.Warn("Failed to marshal tool call input", "error", err)
			continue
		}

		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      toolData.Name,
				Arguments: string(argsJSON),
			},
		})
	}

	return calls
}

// parseToolCallFallback tries to parse tool calls from fallback formats
func parseToolCallFallback(text string) []providers.ToolCall {
	var calls []providers.ToolCall

	// Try to find ```tool_call blocks
	codeBlockPattern := regexp.MustCompile("(?s)```tool_call\\s*\\n(.*?)```")
	matches := codeBlockPattern.FindAllStringSubmatch(text, -1)

	for i, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])

		var toolData struct {
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}

		if err := json.Unmarshal([]byte(jsonStr), &toolData); err != nil {
			continue
		}

		toolData.Input = fixToolCallArguments(toolData.Name, toolData.Input)

		argsJSON, _ := json.Marshal(toolData.Input)
		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      toolData.Name,
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
				input := make(map[string]interface{})
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

				input = fixToolCallArguments(name, input)
				argsJSON, _ := json.Marshal(input)
				calls = append(calls, providers.ToolCall{
					ID:   fmt.Sprintf("call_%d", i),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      name,
						Arguments: string(argsJSON),
					},
				})
			}
		}
	}

	// Try native phase=tool_call format (Qwen streaming)
	if len(calls) == 0 {
		return parseNativeToolCalls(text)
	}

	return calls
}

// parseNativeToolCalls parses native tool_calls format from Qwen streaming
func parseNativeToolCalls(text string) []providers.ToolCall {
	var calls []providers.ToolCall

	// Pattern for native tool_call format
	nativePattern := regexp.MustCompile(`(?s)\{"type":"tool_use"[^}]*\}`)
	for _, match := range nativePattern.FindAllString(text, -1) {
		var toolData struct {
			Type  string                 `json:"type"`
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}

		if err := json.Unmarshal([]byte(match), &toolData); err != nil {
			continue
		}

		if toolData.Type == "tool_use" && toolData.Name != "" {
			toolData.Input = fixToolCallArguments(toolData.Name, toolData.Input)
			argsJSON, _ := json.Marshal(toolData.Input)
			calls = append(calls, providers.ToolCall{
				ID:   toolData.ID,
				Type: "function",
				Function: providers.ToolCallFunction{
					Name:      toolData.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	return calls
}

// fixToolCallArguments fixes tool call arguments for specific tools
// Following qwen2API fix_tool_call_arguments approach
func fixToolCallArguments(name string, input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return input
	}

	fixed := make(map[string]interface{})
	for k, v := range input {
		fixed[k] = v
	}

	// Fix AskUserQuestion tool parameters
	if name == "AskUserQuestion" {
		if question, ok := fixed["question"]; ok && fixed["questions"] == nil {
			fixed["questions"] = []interface{}{
				map[string]interface{}{
					"question":    question,
					"header":      "Question",
					"options":     []interface{}{
						map[string]interface{}{"label": "Yes", "description": "Confirm"},
						map[string]interface{}{"label": "No", "description": "Decline"},
					},
					"multiSelect": false,
				},
			}
			delete(fixed, "question")
			slog.Debug("Fixed AskUserQuestion: converted 'question' to 'questions' array")
		}
	}

	// Fix Read tool parameters
	if name == "Read" {
		if path, ok := fixed["path"]; ok {
			fixed["file_path"] = path
			delete(fixed, "path")
		}
		if filename, ok := fixed["filename"]; ok {
			fixed["file_path"] = filename
			delete(fixed, "filename")
		}
	}

	// Fix Bash tool parameters
	if name == "Bash" {
		if cmd, ok := fixed["cmd"]; ok {
			fixed["command"] = cmd
			delete(fixed, "cmd")
		}
		if script, ok := fixed["script"]; ok {
			fixed["command"] = script
			delete(fixed, "script")
		}
	}

	// Fix Agent tool parameters
	if name == "Agent" {
		if fixed["description"] == nil {
			fixed["description"] = "Execute sub-task"
		}
		if fixed["prompt"] == nil {
			fixed["prompt"] = fixed["description"]
		}
	}

	return fixed
}