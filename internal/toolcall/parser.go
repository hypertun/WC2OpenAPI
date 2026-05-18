package toolcall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	providers "github.com/user/wc2api/internal/providers"
)

// --- Parsing entry points ---

// ParseAllToolCalls tries all extraction strategies in priority order.
// Returns SieveToolCall slice and cleaned text.
func ParseAllToolCalls(text string, toolNames []string) ([]SieveToolCall, string) {
	if text == "" {
		return nil, text
	}

	// Strategy 1: ##TOOL_CALL## markers (highest priority)
	if strings.Contains(text, "##TOOL_CALL##") || strings.Contains(text, "##END_CALL##") {
		calls, _ := parseToolCallMarkers(text)
		if len(calls) > 0 {
			return toSieveCalls(calls), cleanAllToolText(text)
		}
	}

	// Strategy 2: MiMoML / DSML XML with enhanced noise tolerance
	calls := extractMiMoMLToolCalls(text, toolNames)
	if len(calls) > 0 {
		return calls, cleanAllToolText(text)
	}

	// Strategy 3: <tool_call> / <function_calls> XML wrapper (existing)
	pcalls := extractXMLToolCallsFromWrapper(text, toolNames)
	if len(pcalls) > 0 {
		return provToSieveCalls(pcalls), cleanAllToolText(text)
	}

	// Strategy 4: <tool_call> singular (existing)
	pcalls = extractXMLToolCallSingular(text, toolNames)
	if len(pcalls) > 0 {
		return provToSieveCalls(pcalls), cleanAllToolText(text)
	}

	// Strategy 5: TOOL_CALL: name(args)
	tcCalls := extractTOOLCALLPattern(text, toolNames)
	if len(tcCalls) > 0 {
		return tcCalls, cleanAllToolText(text)
	}

	// Strategy 6: Bare JSON {"name":"x","arguments":{...}}
	jsonCalls := extractJSONToolCall(text, toolNames)
	if len(jsonCalls) > 0 {
		return jsonCalls, cleanAllToolText(text)
	}

	// Strategy 7: <function_call> JSON+XML
	fcCalls := extractFunctionCallJSON(text, toolNames)
	if len(fcCalls) > 0 {
		return fcCalls, cleanAllToolText(text)
	}

	return nil, text
}

func toSieveCalls(calls []providers.ToolCall) []SieveToolCall {
	result := make([]SieveToolCall, len(calls))
	for i, c := range calls {
		result[i] = SieveToolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: SieveToolCallFunction{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		}
	}
	return result
}

func provToSieveCalls(calls []providers.ToolCall) []SieveToolCall {
	return toSieveCalls(calls)
}

// ParseToolCallsFromText detects and parses tool calls from ##TOOL_CALL## markers.
// This is the primary strategy used by Qwen and QwenCN.
func ParseToolCallsFromText(text string, tools []providers.Tool) ([]providers.ToolCall, []*ValidationError) {
	if text == "" {
		return nil, nil
	}

	sawMarkers := strings.Contains(text, "##TOOL_CALL##") || strings.Contains(text, "##END_CALL##")
	var allParseErrors []*ValidationError

	// Try parsing from original text first
	if sawMarkers {
		if calls, errs := parseToolCallMarkers(text); len(calls) > 0 {
			ValidateToolCalls(calls, tools)
			return calls, errs
		} else {
			allParseErrors = append(allParseErrors, errs...)
		}
		if calls, errs := parseToolCallFallback(text); len(calls) > 0 {
			ValidateToolCalls(calls, tools)
			return calls, errs
		} else {
			allParseErrors = append(allParseErrors, errs...)
		}
	}

	// Normalize fragmented tool calls
	normalized := NormalizeFragmentedToolCall(text)
	hasNormalizedMarkers := strings.Contains(normalized, "##TOOL_CALL##") || strings.Contains(normalized, "##END_CALL##")
	if !hasNormalizedMarkers {
		if len(allParseErrors) > 0 {
			return nil, allParseErrors
		}
		return nil, nil
	}
	if !sawMarkers {
		sawMarkers = true
	}

	// Try primary format on normalized text
	toolCalls, errs := parseToolCallMarkers(normalized)
	allParseErrors = append(allParseErrors, errs...)
	if len(toolCalls) > 0 {
		ValidateToolCalls(toolCalls, tools)
		return toolCalls, allParseErrors
	}

	// Try fallback formats
	toolCalls, errs = parseToolCallFallback(normalized)
	allParseErrors = append(allParseErrors, errs...)
	if len(toolCalls) > 0 {
		ValidateToolCalls(toolCalls, tools)
		return toolCalls, allParseErrors
	}

	return nil, allParseErrors
}

// ParseXMLToolCalls parses MiMoML / DSML XML tool calls from response text.
func ParseXMLToolCalls(text string, toolNames []string) ([]providers.ToolCall, string) {
	if text == "" {
		return nil, text
	}

	normalized := stripMiMoMLNoise(text)

	// Strategy A: <tool_calls> / <function_calls> wrapper with <invoke> inside
	calls := extractXMLToolCallsFromWrapper(normalized, toolNames)
	if len(calls) > 0 {
		return calls, cleanXMLToolText(text)
	}

	// Strategy B: <tool_call> singular format
	calls = extractXMLToolCallSingular(normalized, toolNames)
	if len(calls) > 0 {
		return calls, cleanXMLToolText(text)
	}

	return nil, text
}

// --- Marker parsing ---

func parseToolCallMarkers(text string) ([]providers.ToolCall, []*ValidationError) {
	var calls []providers.ToolCall
	var parseErrors []*ValidationError

	markerPattern := regexp.MustCompile(`(?s)##TOOL_CALL##\s*(.+?)\s*##END_CALL##`)
	matches := markerPattern.FindAllStringSubmatch(text, -1)

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
			repaired := RepairJSONStringValues(jsonStr)
			err = json.Unmarshal([]byte(repaired), &toolData)
		}
		if err != nil {
			slog.Warn("Failed to parse tool call JSON", "error", err, "json", truncate(jsonStr, 200))
			parseErrors = append(parseErrors, &ValidationError{
				ToolName:  ExtractToolName(jsonStr),
				Parameter: "",
				Message:   fmt.Sprintf("JSON unmarshal failed: %v", err),
				Expected:  `valid JSON object with "name" (string) and "input" (object) fields`,
				Actual:    truncate(jsonStr, 200),
				Severity:  "error",
			})
			continue
		}

		toolData.Input = FixToolCallArguments(toolData.Name, toolData.Input)

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

	return calls, parseErrors
}

func parseToolCallFallback(text string) ([]providers.ToolCall, []*ValidationError) {
	var calls []providers.ToolCall
	var parseErrors []*ValidationError

	// Try ```tool_call blocks
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
			repaired := RepairJSONStringValues(jsonStr)
			err = json.Unmarshal([]byte(repaired), &toolData)
		}
		if err != nil {
			parseErrors = append(parseErrors, &ValidationError{
				ToolName:  ExtractToolName(jsonStr),
				Parameter: "",
				Message:   fmt.Sprintf("tool_call block JSON unmarshal failed: %v", err),
				Expected:  `valid JSON object with "name" (string) and "input" (object) fields`,
				Actual:    truncate(jsonStr, 200),
				Severity:  "error",
			})
			continue
		}

		toolData.Input = FixToolCallArguments(toolData.Name, toolData.Input)
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

			invokePattern := regexp.MustCompile(`(?s)<invoke name="([^"]+)"[^>]*>([\s\S]*?)</invoke>`)
			for _, invokeMatch := range invokePattern.FindAllStringSubmatch(xmlStr, -1) {
				if len(invokeMatch) < 3 {
					continue
				}
				name := invokeMatch[1]
				body := invokeMatch[2]

				paramPattern := regexp.MustCompile(`(?s)<parameter name="([^"]+)"[^>]*>([\s\S]*?)</parameter>`)
				input := make(map[string]any)
				for _, paramMatch := range paramPattern.FindAllStringSubmatch(body, -1) {
					if len(paramMatch) >= 3 {
						paramName := paramMatch[1]
						paramValue := strings.TrimSpace(paramMatch[2])
						paramValue = strings.TrimPrefix(paramValue, "<![CDATA[")
						paramValue = strings.TrimSuffix(paramValue, "]]>")
						input[paramName] = paramValue
					}
				}

				input = FixToolCallArguments(name, input)
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

	// Try native {"type":"tool_use"} format
	if len(calls) == 0 {
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
				repaired := RepairJSONStringValues(match)
				err = json.Unmarshal([]byte(repaired), &toolData)
			}
			if err != nil {
				parseErrors = append(parseErrors, &ValidationError{
					ToolName:  ExtractToolName(match),
					Parameter: "",
					Message:   fmt.Sprintf("native tool_call JSON unmarshal failed: %v", err),
					Expected:  `valid JSON with "type":"tool_use", "id", "name" (string) and "input" (object) fields`,
					Actual:    truncate(match, 200),
					Severity:  "error",
				})
				continue
			}

			if toolData.Type == "tool_use" && toolData.Name != "" {
				toolData.Input = FixToolCallArguments(toolData.Name, toolData.Input)
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
	}

	return calls, parseErrors
}

// --- XML parsing (MiMo / DSML) ---

func extractXMLToolCallsFromWrapper(text string, toolNames []string) []providers.ToolCall {
	wrapperRe := regexp.MustCompile(`(?is)<(?:tool_calls|function_calls)>(.*?)</(?:tool_calls|function_calls)>`)
	wrapperMatch := wrapperRe.FindStringSubmatch(text)
	if wrapperMatch == nil {
		return nil
	}

	inner := wrapperMatch[1]
	invokeRe := regexp.MustCompile(`(?is)<invoke\s+name=["']([^"']+)["'][^>]*>(.*?)</invoke>`)
	invokeMatches := invokeRe.FindAllStringSubmatch(inner, -1)
	if len(invokeMatches) == 0 {
		return nil
	}

	var calls []providers.ToolCall
	for _, m := range invokeMatches {
		name := m[1]
		paramInner := m[2]
		resolvedName := ResolveToolName(name, toolNames)
		if resolvedName == "" {
			continue
		}
		args := extractXMLParameters(paramInner)
		argsJSON, _ := json.Marshal(args)
		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", len(calls)),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      resolvedName,
				Arguments: string(argsJSON),
			},
		})
	}

	return calls
}

func extractXMLToolCallSingular(text string, toolNames []string) []providers.ToolCall {
	re := regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var calls []providers.ToolCall
	for _, m := range matches {
		inner := strings.TrimSpace(m[1])

		// Try <tool_name>NAME</tool_name> format first
		nameRe := regexp.MustCompile(`(?is)<tool_name>(.*?)</tool_name>`)
		nameMatch := nameRe.FindStringSubmatch(inner)

		// Try <function=NAME> or <function name="NAME"> format (MiMo native)
		funcRe := regexp.MustCompile(`(?i)<function(?:\s*=\s*(\w+)|\s+name\s*=\s*["\'](\w+)["\'])>`)

		var name string
		var funcBody string

		if nameMatch != nil {
			name = strings.TrimSpace(nameMatch[1])
			paramsInner := nameRe.ReplaceAllString(inner, "")
			paramsInner = strings.TrimSpace(paramsInner)
			paramsRe := regexp.MustCompile(`(?is)</?parameters>`)
			paramsInner = paramsRe.ReplaceAllString(paramsInner, "")
			paramsInner = strings.TrimSpace(paramsInner)

			resolvedName := ResolveToolName(name, toolNames)
			if resolvedName == "" {
				continue
			}

			args := extractXMLParameters(paramsInner)
			if len(args) > 0 {
				argsJSON, _ := json.Marshal(args)
				calls = append(calls, providers.ToolCall{
					ID:   fmt.Sprintf("call_%d", len(calls)),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      resolvedName,
						Arguments: string(argsJSON),
					},
				})
				continue
			}

			paramsInner = extractCDATAInner(paramsInner)
			var jsonVal interface{}
			if json.Unmarshal([]byte(paramsInner), &jsonVal) == nil {
				if obj, ok := jsonVal.(map[string]interface{}); ok {
					argsJSON, _ := json.Marshal(obj)
					calls = append(calls, providers.ToolCall{
						ID:   fmt.Sprintf("call_%d", len(calls)),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      resolvedName,
						Arguments: string(argsJSON),
					},
					})
					continue
				}
			}

			if paramsInner != "" {
				args = map[string]interface{}{"input": paramsInner}
				argsJSON, _ := json.Marshal(args)
				calls = append(calls, providers.ToolCall{
					ID:   fmt.Sprintf("call_%d", len(calls)),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      resolvedName,
						Arguments: string(argsJSON),
					},
				})
			}
		} else {
			// Try <function=NAME>content</function> format (MiMo v2.5 native)
			funcMatch := funcRe.FindStringSubmatchIndex(inner)
			if funcMatch != nil {
				if funcMatch[2] >= 0 && funcMatch[3] >= 0 {
					name = inner[funcMatch[2]:funcMatch[3]]
				} else if funcMatch[4] >= 0 && funcMatch[5] >= 0 {
					name = inner[funcMatch[4]:funcMatch[5]]
				}
				afterOpen := funcMatch[1]
				closeIdx := strings.Index(inner[afterOpen:], "</function>")
				if closeIdx >= 0 {
					funcBody = strings.TrimSpace(inner[afterOpen : afterOpen+closeIdx])
				}
			}

			if name == "" {
				continue
			}

			resolvedName := ResolveToolName(name, toolNames)
			if resolvedName == "" {
				continue
			}

			funcBody = extractCDATAInner(funcBody)
			var jsonVal interface{}
			if json.Unmarshal([]byte(funcBody), &jsonVal) == nil {
				switch v := jsonVal.(type) {
				case map[string]any:
					argsJSON, _ := json.Marshal(v)
					calls = append(calls, providers.ToolCall{
						ID:   fmt.Sprintf("call_%d", len(calls)),
						Type: "function",
						Function: providers.ToolCallFunction{
							Name:      resolvedName,
							Arguments: string(argsJSON),
						},
					})
				case []any:
					argsJSON, _ := json.Marshal(v)
					calls = append(calls, providers.ToolCall{
						ID:   fmt.Sprintf("call_%d", len(calls)),
						Type: "function",
						Function: providers.ToolCallFunction{
							Name:      resolvedName,
							Arguments: string(argsJSON),
						},
					})
				default:
					args := map[string]any{"input": funcBody}
					argsJSON, _ := json.Marshal(args)
					calls = append(calls, providers.ToolCall{
						ID:   fmt.Sprintf("call_%d", len(calls)),
						Type: "function",
						Function: providers.ToolCallFunction{
							Name:      resolvedName,
							Arguments: string(argsJSON),
						},
					})
				}
				continue
			}

			if funcBody != "" {
				args := map[string]any{"input": funcBody}
				argsJSON, _ := json.Marshal(args)
				calls = append(calls, providers.ToolCall{
					ID:   fmt.Sprintf("call_%d", len(calls)),
					Type: "function",
					Function: providers.ToolCallFunction{
						Name:      resolvedName,
						Arguments: string(argsJSON),
						},
				})
			}
		}
	}

	return calls
}

// --- Normalization ---

// NormalizeFragmentedToolCall normalizes fragmented/incomplete tool calls.
func NormalizeFragmentedToolCall(text string) string {
	text = strings.TrimSpace(text)

	if strings.Contains(text, "##TOOL_CALL##") && strings.Contains(text, "##END_CALL##") {
		return text
	}

	extracted := tryExtractXMLToolCall(text)
	if extracted != "" {
		return extracted
	}
	extracted = tryExtractJSONToolCall(text)
	if extracted != "" {
		return extracted
	}

	text = regexp.MustCompile(`\[.*?\]`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`<\/?think>`).ReplaceAllString(text, "")
	text = regexp.MustCompile("```[\\s\\S]*?```").ReplaceAllString(text, "")

	extracted = tryExtractXMLToolCall(text)
	if extracted != "" {
		return extracted
	}
	extracted = tryExtractJSONToolCall(text)
	if extracted != "" {
		return extracted
	}

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

	if strings.Contains(normalized, "TOOL_CALL##") && !strings.Contains(normalized, "##TOOL_CALL##") {
		normalized = strings.ReplaceAll(normalized, "TOOL_CALL##", "##TOOL_CALL##")
	}
	if strings.Contains(normalized, "##END_CALL") && !strings.Contains(normalized, "##END_CALL##") {
		normalized = strings.ReplaceAll(normalized, "##END_CALL", "##END_CALL##")
	}
	if strings.Contains(normalized, "##END CALL##") {
		normalized = strings.ReplaceAll(normalized, "##END CALL##", "##END_CALL##")
	}
	if strings.Contains(normalized, "##END_CALL##") && !strings.Contains(normalized, "##TOOL_CALL##") && strings.Contains(normalized, `"name"`) {
		normalized = "##TOOL_CALL##\n" + normalized
	}

	return normalized
}

// --- Validation ---

// ValidateToolCalls validates all tool calls against provided tool definitions (no-error variant).
func ValidateToolCalls(calls []providers.ToolCall, tools []providers.Tool) {
	if tools == nil {
		return
	}
	for _, call := range calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			continue
		}
		ValidateSingleToolCall(call.Function.Name, args, tools)
	}
}



// ValidateSingleToolCall validates a single tool call.
func ValidateSingleToolCall(name string, input map[string]any, tools []providers.Tool) {
	var toolDef *providers.Tool
	for _, t := range tools {
		if t.Function.Name == name {
			toolDef = &t
			break
		}
	}
	if toolDef == nil {
		return
	}
	if toolDef.Function.Parameters == nil {
		return
	}
	schemaJSON, err := json.Marshal(toolDef.Function.Parameters)
	if err != nil {
		slog.Warn("Failed to marshal tool schema", "tool", name, "error", err)
		return
	}
	result := ValidateToolCall(name, input, string(schemaJSON))
	slog.Info("Validation result",
		"tool", name,
		"valid", !result.HasErrors(),
		"error_count", len(result.Errors),
		"errors", result.Errors)
}

// --- Repair / fix functions ---

// RepairJSONStringValues fixes unescaped double quotes inside JSON string values.
func RepairJSONStringValues(jsonStr string) string {
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

// FixToolCallArguments applies type coercion, tool-specific fixes, and structure fixes.
func FixToolCallArguments(name string, input map[string]any) map[string]any {
	if input == nil {
		return input
	}

	fixed := make(map[string]any)
	for k, v := range input {
		fixed[k] = v
	}

	for k, v := range fixed {
		fixed[k] = coerceValue(v)
	}

	fixed = applyToolSpecificFixes(name, fixed)
	fixed = fixStructures(name, fixed)

	return fixed
}

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

func applyToolSpecificFixes(name string, fixed map[string]any) map[string]any {
	nameLower := strings.ToLower(name)

	switch nameLower {
	case "askuserquestion":
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
		if path, ok := fixed["path"]; ok {
			fixed["file_path"] = path
			delete(fixed, "path")
		}
		if filename, ok := fixed["filename"]; ok {
			fixed["file_path"] = filename
			delete(fixed, "filename")
		}
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

func fixStructures(name string, input map[string]any) map[string]any {
	for k, v := range input {
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

		if strVal, ok := v.(string); ok && strings.HasPrefix(strVal, "{") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(strVal), &parsed); err == nil {
				input[k] = parsed
				slog.Debug("Parsed JSON string to object", "tool", name, "param", k)
			}
		}

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

// --- Utility functions ---

// ResolveToolName resolves a tool name using exact, case-insensitive, and snake_case matching.
func ResolveToolName(name string, toolNames []string) string {
	nameLower := strings.ToLower(name)

	sorted := make([]string, len(toolNames))
	copy(sorted, toolNames)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})

	for _, tn := range sorted {
		if tn == name {
			return tn
		}
		if strings.ToLower(tn) == nameLower {
			return tn
		}
	}

	snake := CamelToSnake(name)
	for _, tn := range sorted {
		if strings.ToLower(tn) == strings.ToLower(snake) {
			return tn
		}
	}

	return ""
}

// CamelToSnake converts camelCase to snake_case.
func CamelToSnake(s string) string {
	re := regexp.MustCompile(`([a-z0-9])([A-Z])`)
	snake := re.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
}

func stripMiMoMLNoise(text string) string {
	// Enhanced noise tolerance for MiMoML / DSML / garbled XML prefixes.
	// Handles: doubled <, missing |, fullwidth ｜, no separator, hyphenated
	// variants, DSML variants, CDATA pass-through, fenced code block skip.
	if text == "" || !strings.Contains(text, "<") {
		return text
	}

	var result strings.Builder
	result.Grow(len(text))
	i, n := 0, len(text)
	for i < n {
		// Pass through CDATA blocks unchanged
		if strings.HasPrefix(text[i:], "<![CDATA[") {
			if close := strings.Index(text[i+9:], "]]>"); close >= 0 {
				result.WriteString(text[i : i+9+close+3])
				i += 9 + close + 3
				continue
			}
			result.WriteString(text[i:])
			break
		}

		// Pass through fenced code blocks
		if text[i] == '`' || text[i] == '~' {
			fenceChar := text[i]
			fenceLen := 0
			for i+fenceLen < n && text[i+fenceLen] == fenceChar {
				fenceLen++
			}
			if fenceLen >= 3 {
				// Find closing fence
				j := i + fenceLen
				found := false
				for j < n {
					nl := strings.IndexByte(text[j:], '\n')
					if nl < 0 {
						break
					}
					lineStart := j + nl + 1
					if lineStart+fenceLen <= n {
						allMatch := true
						for k := 0; k < fenceLen; k++ {
							if text[lineStart+k] != fenceChar {
								allMatch = false
								break
							}
						}
						if allMatch {
							endPos := lineStart + fenceLen
							if endPos >= n || text[endPos] == '\n' || text[endPos] == '\r' {
								if endPos < n {
									endPos++
								}
								result.WriteString(text[i:endPos])
								i = endPos
								found = true
								break
							}
						}
					}
					j = lineStart + 1
				}
				if !found {
					result.WriteString(text[i:])
					i = n
				}
				continue
			}
		}

		if text[i] != '<' {
			result.WriteByte(text[i])
			i++
			continue
		}

		// Try to match a noisy MiMoML/DSML prefix
		rest := text[i+1:]
		restLower := strings.ToLower(rest)

		hasDSML := strings.Contains(restLower, "dsml")
		hasMiMoML := strings.Contains(restLower, "mimoml")
		if !hasDSML && !hasMiMoML {
			result.WriteByte(text[i])
			i++
			continue
		}

		// Consume noise chars and keyword, extract tag name
		j := 0
		isClosing := false
		for j < len(rest) {
			ch := rest[j]
			if ch == '<' {
				j++
				continue
			}
			if ch == '/' && j == 0 {
				isClosing = true
				j++
				continue
			}
			if ch == '|' || ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
				j++
				continue
			}
			// Fullwidth pipe ｜ (U+FF5C, 3 bytes: 0xEF 0xBD 0xBC)
			if j+3 <= len(rest) && rest[j:j+3] == "\uFF5C" {
				j += 3
				continue
			}
			break
		}

		kwLen := 0
		if j+4 <= len(rest) && restLower[j:j+4] == "dsml" {
			kwLen = 4
		} else if j+6 <= len(rest) && restLower[j:j+6] == "mimoml" {
			kwLen = 6
		}

		if kwLen == 0 {
			result.WriteByte(text[i])
			i++
			continue
		}
		j += kwLen

		// Consume trailing noise (-, _, space, |, fullwidth |)
		for j < len(rest) {
			ch := rest[j]
			if ch == '-' || ch == '_' || ch == '|' || ch == ' ' || ch == '\t' {
				j++
				continue
			}
			if j+3 <= len(rest) && rest[j:j+3] == "\uFF5C" {
				j += 3
				continue
			}
			break
		}

		// Find tag name end
		tagStart := j
		for j < len(rest) && (rest[j] == '_' || rest[j] >= 'a' && rest[j] <= 'z' || rest[j] >= 'A' && rest[j] <= 'Z' || rest[j] >= '0' && rest[j] <= '9') {
			j++
		}
		tagName := strings.ToLower(rest[tagStart:j])

		if !isValidXMLTagName(tagName) {
			result.WriteByte(text[i])
			i++
			continue
		}

		// Find closing >
		end := strings.IndexByte(rest[j:], '>')
		if end < 0 {
			result.WriteByte(text[i])
			i++
			continue
		}

		result.WriteByte('<')
		if isClosing {
			result.WriteByte('/')
		}
		// Include full tag content (attributes, etc.) up to >
		result.WriteString(rest[tagStart : j+end+1])
		i += 1 + j + end + 1
	}
	return result.String()
}

func isValidXMLTagName(s string) bool {
	switch s {
	case "tool_calls", "function_calls", "invoke", "parameter", "tool_call", "function_call":
		return true
	}
	return false
}

func extractXMLParameters(inner string) map[string]interface{} {
	args := make(map[string]interface{})

	paramRe := regexp.MustCompile(`(?is)<parameter\s+name=["']([^"']+)["'][^>]*>(.*?)</parameter>`)
	matches := paramRe.FindAllStringSubmatch(inner, -1)

	for _, m := range matches {
		key := m[1]
		val := extractCDATAInnerEnhanced(strings.TrimSpace(m[2]))
		parsed := parseParamValue(val, key)
		// Merge duplicate keys into arrays
		if existing, ok := args[key]; ok {
			if arr, ok := existing.([]any); ok {
				args[key] = append(arr, parsed)
			} else {
				args[key] = []any{existing, parsed}
			}
		} else {
			args[key] = parsed
		}
	}

	// Fallback: <parameter=KEY>VALUE</parameter>
	eqRe := regexp.MustCompile(`(?is)<parameter=(\w+)>(.*?)</parameter>`)
	eqMatches := eqRe.FindAllStringSubmatch(inner, -1)
	for _, m := range eqMatches {
		key := m[1]
		val := extractCDATAInnerEnhanced(strings.TrimSpace(m[2]))
		parsed := parseParamValue(val, key)
		if existing, ok := args[key]; ok {
			if arr, ok := existing.([]any); ok {
				args[key] = append(arr, parsed)
			} else {
				args[key] = []any{existing, parsed}
			}
		} else {
			args[key] = parsed
		}
	}

	return args
}

func extractCDATAInner(val string) string {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "<![CDATA[") && strings.HasSuffix(val, "]]>") {
		val = val[9 : len(val)-3]
	}
	return val
}

func extractCDATAInnerEnhanced(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	if strings.HasPrefix(val, "<![CDATA[") && strings.HasSuffix(val, "]]>") {
		return val[9 : len(val)-3]
	}
	// Handle unclosed CDATA
	if strings.HasPrefix(val, "<![CDATA[") {
		return val[9:]
	}
	return val
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ExtractToolName attempts to extract the tool name from JSON, even if malformed.
func ExtractToolName(jsonStr string) string {
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

func tryExtractXMLToolCall(text string) string {
	wrappedPattern := regexp.MustCompile(`(?is)<tool_calls>\s*(<invoke[\s\S]*?<\/invoke>)\s*<\/tool_calls>`)
	if matches := wrappedPattern.FindStringSubmatch(text); len(matches) > 1 {
		return matches[1]
	}

	invokePattern := regexp.MustCompile(`(?is)<invoke\s+name="([^"]+)"[^>]*>([\s\S]*?)<\/invoke>`)
	if matches := invokePattern.FindStringSubmatch(text); len(matches) > 0 {
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

func tryExtractJSONToolCall(text string) string {
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
					var obj map[string]any
					err := json.Unmarshal([]byte(jsonStr), &obj)
					if err != nil {
						repaired := RepairJSONStringValues(jsonStr)
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

// ── New extraction strategies ──────────────────────────────────

func extractMiMoMLToolCalls(text string, toolNames []string) []SieveToolCall {
	if !strings.ContainsAny(text, "<|") {
		return nil
	}
	normalized := stripMiMoMLNoise(text)

	// Try <tool_calls> / <function_calls> wrapper
	wrapperRe := regexp.MustCompile(`(?is)<(?:tool_calls|function_calls)>(.*?)</(?:tool_calls|function_calls)>`)
	blocks := wrapperRe.FindAllStringSubmatch(normalized, -1)

	var calls []SieveToolCall
	invokeRe := regexp.MustCompile(`(?is)<invoke\s+name=["']([^"']+)["'][^>]*>(.*?)</invoke>`)
	invokeNoSpace := regexp.MustCompile(`(?is)<invokename=["']([^"']+)["']>(.*?)</invoke>`)

	for _, block := range blocks {
		inner := block[1]
		for _, m := range invokeRe.FindAllStringSubmatch(inner, -1) {
			if resolved := ResolveToolName(m[1], toolNames); resolved != "" {
				argsJSON, _ := json.Marshal(extractXMLParameters(m[2]))
				calls = append(calls, SieveToolCall{
					ID: fmt.Sprintf("call_%d", len(calls)), Type: "function",
					Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
				})
			}
		}
		for _, m := range invokeNoSpace.FindAllStringSubmatch(inner, -1) {
			if resolved := ResolveToolName(m[1], toolNames); resolved != "" {
				argsJSON, _ := json.Marshal(extractXMLParameters(m[2]))
				calls = append(calls, SieveToolCall{
					ID: fmt.Sprintf("call_%d", len(calls)), Type: "function",
					Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
				})
			}
		}
	}

	// Bare <invoke> without wrapper
	if len(calls) == 0 {
		for _, m := range invokeRe.FindAllStringSubmatch(normalized, -1) {
			if resolved := ResolveToolName(m[1], toolNames); resolved != "" {
				argsJSON, _ := json.Marshal(extractXMLParameters(m[2]))
				calls = append(calls, SieveToolCall{
					ID: fmt.Sprintf("call_%d", len(calls)), Type: "function",
					Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
				})
			}
		}
	}
	return calls
}

func extractTOOLCALLPattern(text string, toolNames []string) []SieveToolCall {
	var calls []SieveToolCall
	idx := 0
	pattern := regexp.MustCompile(`(?i)TOOL_CALL:\s*(\w+)\s*\(`)
	for idx < len(text) {
		m := pattern.FindStringSubmatchIndex(text[idx:])
		if m == nil {
			break
		}
		fname := text[idx+m[2] : idx+m[3]]
		// Find the actual ( position — it's after the full match's group 1 end
		// The full match ends at m[1], and the ( is the last char of the full match
		parenIdx := idx + m[1] - 1

		depth := 1
		inStr, escaped := false, false
		end := -1
		for i := parenIdx + 1; i < len(text); i++ {
			c := text[i]
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && inStr {
				escaped = true
				continue
			}
			if c == '"' {
				inStr = !inStr
				continue
			}
			if inStr {
				continue
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end == -1 {
			break
		}

		args := parseFunctionArgs(text[parenIdx+1 : end])
		if resolved := ResolveToolName(fname, toolNames); resolved != "" {
			argsJSON, _ := json.Marshal(args)
			calls = append(calls, SieveToolCall{
				ID: fmt.Sprintf("call_%d", len(calls)), Type: "function",
				Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
			})
		}
		idx = end + 1
	}
	return calls
}

func extractJSONToolCall(text string, toolNames []string) []SieveToolCall {
	start := 0
	for {
		brace := strings.Index(text[start:], "{")
		if brace < 0 {
			break
		}
		absStart := start + brace
		js := findBalancedJSON(text, absStart)
		if js == "" {
			start = absStart + 1
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(js), &obj) != nil {
			start = absStart + 1
			continue
		}
		name, _ := obj["name"].(string)
		if fn, ok := obj["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				name = n
			}
		}
		if resolved := ResolveToolName(name, toolNames); resolved != "" {
			args := obj["arguments"]
			if args == nil {
				args = obj["parameters"]
			}
			if args == nil {
				args = obj["input"]
			}
			if args == nil {
				args = map[string]any{}
			}
			if s, ok := args.(string); ok {
				var parsed map[string]any
				if json.Unmarshal([]byte(s), &parsed) == nil {
					args = parsed
				}
			}
			argsJSON, _ := json.Marshal(args)
			return []SieveToolCall{{
				ID: "call_0", Type: "function",
				Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
			}}
		}
		nextBrace := strings.Index(text[absStart+len(js):], "}")
		if nextBrace < 0 {
			break
		}
		start = absStart + len(js) + nextBrace + 1
	}
	return nil
}

func extractFunctionCallJSON(text string, toolNames []string) []SieveToolCall {
	re := regexp.MustCompile(`(?s)<function_calls?>(.*?)</function_calls?>`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	inner := m[1]
	blocks := strings.Split(inner, "</function_call>")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		reOpen := regexp.MustCompile(`(?s)<function_call>`)
		block = reOpen.ReplaceAllString(block, "")
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		jsStart := strings.Index(block, "{")
		if jsStart < 0 {
			continue
		}
		js := findBalancedJSON(block, jsStart)
		if js == "" {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(js), &data) != nil {
			continue
		}
		name, _ := data["name"].(string)
		if resolved := ResolveToolName(name, toolNames); resolved != "" {
			args, _ := data["arguments"].(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			argsJSON, _ := json.Marshal(args)
			return []SieveToolCall{{
				ID: "call_0", Type: "function",
				Function: SieveToolCallFunction{Name: resolved, Arguments: string(argsJSON)},
			}}
		}
	}
	return nil
}

func findBalancedJSON(text string, start int) string {
	if start >= len(text) || text[start] != '{' {
		return ""
	}
	depth := 0
	inStr, escaped := false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inStr {
			escaped = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func parseFunctionArgs(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		var obj map[string]any
		if json.Unmarshal([]byte(raw), &obj) == nil {
			return obj
		}
	}
	args := map[string]any{}
	for _, pair := range smartSplit(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" || !strings.Contains(pair, "=") {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		v = strings.Trim(v, "'")
		if k != "" {
			args[k] = autoType(v)
		}
	}
	if len(args) > 0 {
		return args
	}
	return map[string]any{"input": raw}
}

func smartSplit(text string, sep string) []string {
	var parts []string
	var cur []rune
	dp, db, dbr := 0, 0, 0
	inStr, escaped := false, false
	for _, ch := range text {
		if escaped {
			cur = append(cur, ch)
			escaped = false
			continue
		}
		if ch == '\\' && inStr {
			cur = append(cur, ch)
			escaped = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			cur = append(cur, ch)
			continue
		}
		if inStr {
			cur = append(cur, ch)
			continue
		}
		switch ch {
		case '(':
			dp++
		case ')':
			dp--
		case '[':
			db++
		case ']':
			db--
		case '{':
			dbr++
		case '}':
			dbr--
		}
		if ch == rune(sep[0]) && dp == 0 && db == 0 && dbr == 0 {
			parts = append(parts, strings.TrimSpace(string(cur)))
			cur = nil
			continue
		}
		cur = append(cur, ch)
	}
	if len(cur) > 0 {
		parts = append(parts, strings.TrimSpace(string(cur)))
	}
	return parts
}

func autoType(val string) any {
	switch strings.ToLower(val) {
	case "true":
		return true
	case "false":
		return false
	case "null", "none":
		return nil
	}
	if i, err := strconv.ParseInt(val, 10, 64); err == nil {
		return float64(i)
	}
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return f
	}
	return val
}

func parseParamValue(valRaw string, paramName string) any {
	if valRaw == "" {
		return ""
	}
	if strings.HasPrefix(valRaw, "<![CDATA[") && strings.HasSuffix(valRaw, "]]>") {
		return parseParamValueInner(valRaw[9 : len(valRaw)-3])
	}
	if strings.Contains(valRaw, "<") && strings.Contains(valRaw, ">") {
		if parsed := parseStructuredXML(valRaw); parsed != nil {
			return parsed
		}
	}
	var jv any
	if json.Unmarshal([]byte(valRaw), &jv) == nil {
		return jv
	}
	repaired := repairLooseJSON(valRaw)
	if repaired != valRaw {
		if json.Unmarshal([]byte(repaired), &jv) == nil {
			return jv
		}
	}
	r := autoType(valRaw)
	if s, ok := r.(string); ok {
		return htmlUnescape(s)
	}
	return r
}

func parseParamValueInner(inner string) any {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return ""
	}
	if strings.HasPrefix(inner, "<") && strings.Contains(inner, ">") {
		if parsed := parseStructuredXML(inner); parsed != nil {
			return parsed
		}
	}
	var jv any
	if json.Unmarshal([]byte(inner), &jv) == nil {
		return jv
	}
	return htmlUnescape(inner)
}

func parseStructuredXML(text string) any {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '<' {
		return nil
	}
	itemRe := regexp.MustCompile(`(?s)<(\w+)(?:\s[^>]*)?>(.*?)</\1>`)
	var items []any
	for _, m := range itemRe.FindAllStringSubmatch(text, -1) {
		tag := strings.ToLower(m[1])
		inner := strings.TrimSpace(m[2])
		if tag == "parameter" {
			continue
		}
		var val any
		if strings.Contains(inner, "<") && strings.Contains(inner, ">") {
			if child := parseStructuredXML(inner); child != nil {
				val = child
			} else {
				val = htmlUnescape(inner)
			}
		} else {
			val = parseParamValueInner(inner)
		}
		if tag == "item" {
			items = append(items, val)
		} else {
			items = append(items, map[string]any{tag: val})
		}
	}
	if len(items) == 0 {
		return nil
	}
	allPlain := true
	for _, it := range items {
		if _, ok := it.(map[string]any); ok {
			allPlain = false
			break
		}
	}
	if allPlain {
		return items
	}
	result := map[string]any{}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			for k, v := range m {
				if k == "item" {
					appendToArray(result, "item", v)
				} else {
					appendToArray(result, k, v)
				}
			}
		} else {
			appendToArray(result, "item", it)
		}
	}
	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}
	if len(keys) == 1 && keys[0] == "item" {
		if arr, ok := result["item"].([]any); ok {
			return arr
		}
		return []any{result["item"]}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func appendToArray(m map[string]any, k string, v any) {
	if existing, ok := m[k]; ok {
		if arr, ok := existing.([]any); ok {
			m[k] = append(arr, v)
		} else {
			m[k] = []any{existing, v}
		}
	} else {
		m[k] = v
	}
}

func htmlUnescape(text string) string {
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&quot;", `"`)
	text = strings.ReplaceAll(text, "&apos;", "'")
	text = strings.ReplaceAll(text, "&#39;", "'")
	return text
}

func repairLooseJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	re := regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)
	return re.ReplaceAllString(s, `$1"$2":`)
}

func cleanAllToolText(text string) string {
	if text == "" {
		return text
	}
	text = stripMiMoMLNoise(text)
	// Strip ##TOOL_CALL##...##END_CALL## blocks
	re := regexp.MustCompile(`(?s)##TOOL_CALL##.*?##END_CALL##`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?mi)^TOOL_CALL:.*$`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?i)</?(?:tool_calls|function_calls|invoke|parameter)[^>]*>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?s)<!\[CDATA\[.*?\]\]>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?i)<tool_call>.*?</tool_call>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?\\s*\\{.*?\"tool_call\".*?\\}\\s*\\n?\\s*```")
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?i)<function_calls?>.*?</function_calls?>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func cleanXMLToolText(text string) string {
	if text == "" {
		return text
	}
	text = stripMiMoMLNoise(text)
	re := regexp.MustCompile(`(?mi)^TOOL_CALL:.*$`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?i)</?(?:tool_calls|function_calls|invoke|parameter)[^>]*>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?s)<!\[CDATA\[.*?\]\]>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`(?i)<tool_call>.*?</tool_call>`)
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?\\s*\\{.*?\"tool_call\".*?\\}\\s*\\n?\\s*```")
	text = re.ReplaceAllString(text, "")
	re = regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func buildExampleInput(params map[string]interface{}) string {
	if params == nil {
		return "{}"
	}
	required := map[string]bool{}
	if reqRaw, ok := params["required"]; ok {
		if reqList, ok := reqRaw.([]interface{}); ok {
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
	}
	propsRaw, ok := params["properties"]
	if !ok {
		return "{}"
	}
	props, ok := propsRaw.(map[string]interface{})
	if !ok {
		return "{}"
	}
	example := make(map[string]interface{})
	for name, propRaw := range props {
		prop, ok := propRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if required[name] {
			if t, ok := prop["type"]; ok {
				ts, ok := t.(string)
				if ok {
					switch ts {
					case "string":
						example[name] = "example_value"
					case "number", "integer":
						example[name] = 0
					case "boolean":
						example[name] = false
					case "array":
						example[name] = []interface{}{}
					case "object":
						example[name] = map[string]interface{}{}
					default:
						example[name] = nil
					}
				}
			}
		}
	}
	jsonBytes, err := json.Marshal(example)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func buildExampleResult(toolName string) string {
	switch toolName {
	case "Read":
		return "<file content here>"
	case "Write":
		return "success"
	case "Edit":
		return "edit applied"
	case "Bash":
		return "<command output>"
	case "Glob":
		return "[\"/path/to/file1\", \"/path/to/file2\"]"
	case "Search":
		return "[\"result1\", \"result2\"]"
	default:
		return "done"
	}
}
