package mimo

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	providers "github.com/user/wc2api/internal/providers"
)

// buildToolPrompt generates the MiMoML tool calling instructions
// and tool definitions to inject into the system message.
func buildToolPrompt(tools []providers.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(`TOOL CALL FORMAT — FOLLOW EXACTLY:

<|MiMoML|tool_calls>
  <|MiMoML|invoke name="TOOL_NAME_HERE">
    <|MiMoML|parameter name="PARAMETER_NAME"><![CDATA[PARAMETER_VALUE]]></|MiMoML|parameter>
  </|MiMoML|invoke>
</|MiMoML|tool_calls>

RULES:
1) Use the <|MiMoML|tool_calls> wrapper format.
2) Put one or more <|MiMoML|invoke> entries under a single <|MiMoML|tool_calls> root.
3) Put the tool name in the invoke name attribute: <|MiMoML|invoke name="TOOL_NAME">.
4) All string values must use <![CDATA[...]]>, even short ones.
5) Every top-level argument must be a <|MiMoML|parameter name="ARG_NAME">...</|MiMoML|parameter> node.
6) Objects use nested XML elements. Arrays may repeat <item> children.
7) Numbers, booleans, and null stay plain text.
8) Use only the parameter names in the tool schema. Do not invent fields.
9) Do NOT wrap XML in markdown fences. Do NOT output explanations or role markers.
10) If you call a tool, the first non-whitespace characters must be exactly <|MiMoML|tool_calls>.
11) Never omit the opening <|MiMoML|tool_calls> tag.

Available tools:
`)

	for _, t := range tools {
		if t.Type == "function" {
			desc := t.Function.Description
			if idx := strings.Index(desc, "\n"); idx >= 0 {
				desc = desc[:idx]
			}
			desc = strings.TrimSpace(desc)
			if t.Function.Name != "" {
				if desc != "" {
					sb.WriteString(fmt.Sprintf("  - %s: %s\n", t.Function.Name, desc))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s\n", t.Function.Name))
				}
			}
		}
	}

	return sb.String()
}

// getToolNames extracts function names from the tools slice
func getToolNames(tools []providers.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" && t.Function.Name != "" {
			names = append(names, t.Function.Name)
		}
	}
	return names
}

// extractToolCall parses MiMoML tool calls from response text.
// Returns tool_calls and cleaned text (with MiMoML markup removed).
func extractToolCall(text string, toolNames []string) ([]providers.ToolCall, string) {
	if !hasToolMarkers(text) {
		slog.Debug("mimo: tc no markers")
		return nil, text
	}

	slog.Debug("mimo: tc markers found", "text_preview", text[:min(len(text), 150)], "toolNames", toolNames)

	// Normalize the text — strip noise prefixes
	normalized := stripMiMoMLNoise(text)
	slog.Debug("mimo: tc normalized", "norm_preview", normalized[:min(len(normalized), 150)])

	// Try MiMoML format: <|MiMoML|tool_calls>...</|MiMoML|tool_calls>
	tc := extractMiMoMLToolCalls(normalized, toolNames)
	if tc != nil {
		slog.Debug("mimo: tc found via MiMoML", "count", len(tc))
		return tc, cleanToolText(text)
	}
	slog.Debug("mimo: tc MiMoML strategy returned nil")

	// Try singular <tool_call> format: <tool_call><tool_name>name</tool_name><parameter name="k">v</parameter></tool_call>
	tc = extractToolCallXMLSingular(normalized, toolNames)
	if tc != nil {
		slog.Debug("mimo: tc found via singular XML", "count", len(tc))
		return tc, cleanToolText(text)
	}
	slog.Debug("mimo: tc singular XML strategy returned nil")

	// Try tool_calls wrapper: <tool_calls>...</tool_calls>
	tc = extractToolCallsXML(normalized, toolNames)
	if tc != nil {
		slog.Debug("mimo: tc found via XML", "count", len(tc))
		return tc, cleanToolText(text)
	}
	slog.Debug("mimo: tc XML strategy returned nil")

	// Check for any residual tool markers and clean
	if hasToolMarkers(text) {
		slog.Debug("mimo: tc residual markers, cleaning")
		return nil, cleanToolText(text)
	}

	slog.Debug("mimo: tc no markers after all")
	return nil, text
}

// hasToolMarkers checks if text contains tool call markers
func hasToolMarkers(text string) bool {
	lower := strings.ToLower(text)
	markers := []string{"mimoml", "tool_calls", "invoke", "tool_call", "function_calls"}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// stripMiMoMLNoise normalizes MiMoML tag variations to clean XML:
// <|MiMoML|tool_calls> → <tool_calls>
// <|MiMoML|invoke ...> → <invoke ...>
// <|MiMoML|parameter ...> → <parameter ...>
func stripMiMoMLNoise(text string) string {
	// Remove MiMoML| prefix variations, preserving closing slash
	re := regexp.MustCompile(`<(/?)[|｜]?[Mm][Ii][Mm][Oo][Mm][Ll][|｜]?`)
	text = re.ReplaceAllString(text, "<$1")
	return text
}

// extractMiMoMLToolCalls extracts tool calls from MiMoML format
func extractMiMoMLToolCalls(text string, toolNames []string) []providers.ToolCall {
	// Pattern: <tool_calls>...</tool_calls> or <function_calls>...</function_calls>
	// (?s) = DOTALL so . matches \n across lines
	wrapperRe := regexp.MustCompile(`(?is)<(?:tool_calls|function_calls)>(.*?)</(?:tool_calls|function_calls)>`)
	wrapperMatch := wrapperRe.FindStringSubmatch(text)
	if wrapperMatch == nil {
		slog.Debug("mimo: tc wrapper regex no match", "text_preview", text[:min(len(text), 120)])
		return nil
	}

	inner := wrapperMatch[1]
	slog.Debug("mimo: tc wrapper matched", "inner_len", len(inner), "inner_preview", inner[:min(len(inner), 200)])

	// Extract each <invoke name="...">...</invoke>
	invokeRe := regexp.MustCompile(`(?is)<invoke\s+name=["']([^"']+)["'][^>]*>(.*?)</invoke>`)
	invokeMatches := invokeRe.FindAllStringSubmatch(inner, -1)
	if len(invokeMatches) == 0 {
		slog.Debug("mimo: tc no invoke matches in inner")
		return nil
	}

	slog.Debug("mimo: tc found invokes", "count", len(invokeMatches))
	var calls []providers.ToolCall
	for _, m := range invokeMatches {
		name := m[1]
		paramInner := m[2]

		slog.Debug("mimo: tc invoke", "name", name, "paramInner_len", len(paramInner), "paramInner_preview", paramInner[:min(len(paramInner), 150)])

		// Resolve tool name
		resolvedName := resolveToolName(name, toolNames)
		slog.Debug("mimo: tc resolve result", "original", name, "resolved", resolvedName)
		if resolvedName == "" {
			continue
		}

		// Extract parameters: <parameter name="key">...</parameter>
		args := extractParameters(paramInner)
		slog.Debug("mimo: tc params extracted", "args", fmt.Sprintf("%v", args))

		argsJSON, err := json.Marshal(args)
		if err != nil {
			argsJSON = []byte("{}")
		}

		calls = append(calls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", len(calls)),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      resolvedName,
				Arguments: string(argsJSON),
			},
		})
	}

	slog.Debug("mimo: tc total calls", "count", len(calls))
	return calls
}

// extractToolCallsXML extracts from <tool_calls><invoke>...</invoke></tool_calls> (already normalized)
func extractToolCallsXML(text string, toolNames []string) []providers.ToolCall {
	return extractMiMoMLToolCalls(text, toolNames)
}

// extractToolCallXMLSingular extracts from <tool_call> (singular) format:
//
//	<tool_call>
//	  <tool_name>NAME</tool_name>
//	  <parameters>
//	    <parameter name="KEY">VALUE</parameter>
//	  </parameters>
//	</tool_call>
//
// The <parameters> wrapper is optional; bare <parameter> children are also accepted.
// If no parameter tags exist the remaining text is tried as JSON, then as a raw string.
func extractToolCallXMLSingular(text string, toolNames []string) []providers.ToolCall {
	re := regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	slog.Debug("mimo: tc singular XML blocks", "count", len(matches))
	var calls []providers.ToolCall

	for _, m := range matches {
		inner := strings.TrimSpace(m[1])

		// Try <tool_name>NAME</tool_name> format first
		nameRe := regexp.MustCompile(`(?is)<tool_name>(.*?)</tool_name>`)
		nameMatch := nameRe.FindStringSubmatch(inner)

		// Try <function=NAME> format (MiMo v2.5 native)
		funcRe := regexp.MustCompile(`(?i)<function\s*=\s*(\w+)>`)

		var name string
		var funcBody string

		if nameMatch != nil {
			name = strings.TrimSpace(nameMatch[1])
			resolvedName := resolveToolName(name, toolNames)
			slog.Debug("mimo: tc singular XML tool_name", "original", name, "resolved", resolvedName)
			if resolvedName == "" {
				continue
			}

			paramsInner := nameRe.ReplaceAllString(inner, "")
			paramsInner = strings.TrimSpace(paramsInner)
			paramsRe := regexp.MustCompile(`(?is)</?parameters>`)
			paramsInner = paramsRe.ReplaceAllString(paramsInner, "")
			paramsInner = strings.TrimSpace(paramsInner)

			args := extractParameters(paramsInner)
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

			paramsInner = extractCDATA(paramsInner)
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
			funcMatch := funcRe.FindStringSubmatch(inner)
			if funcMatch != nil && len(funcMatch) >= 2 {
				name = funcMatch[1]
				// Extract body between <function=...> and </function>
				afterOpen := strings.Index(inner, ">") + 1
				closeIdx := strings.Index(inner[afterOpen:], "</function>")
				if closeIdx >= 0 {
					funcBody = strings.TrimSpace(inner[afterOpen : afterOpen+closeIdx])
				}
			}

			if name == "" {
				slog.Debug("mimo: tc singular XML no tool_name and no function=, skipping")
				continue
			}

			resolvedName := resolveToolName(name, toolNames)
			if resolvedName == "" {
				continue
			}

			funcBody = extractCDATA(funcBody)
			var jsonVal interface{}
			if json.Unmarshal([]byte(funcBody), &jsonVal) == nil {
				switch v := jsonVal.(type) {
				case map[string]interface{}:
					argsJSON, _ := json.Marshal(v)
					calls = append(calls, providers.ToolCall{
						ID:   fmt.Sprintf("call_%d", len(calls)),
						Type: "function",
						Function: providers.ToolCallFunction{
							Name:      resolvedName,
							Arguments: string(argsJSON),
						},
					})
				case []interface{}:
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
					args := map[string]interface{}{"input": funcBody}
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
				args := map[string]interface{}{"input": funcBody}
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

// extractParameters parses <parameter name="key">value</parameter> blocks
func extractParameters(inner string) map[string]interface{} {
	args := make(map[string]interface{})

	paramRe := regexp.MustCompile(`(?is)<parameter\s+name=["']([^"']+)["'][^>]*>(.*?)</parameter>`)
	matches := paramRe.FindAllStringSubmatch(inner, -1)
	slog.Debug("mimo: tc extractParams", "match_count", len(matches))

	for _, m := range matches {
		key := m[1]
		val := strings.TrimSpace(m[2])
		slog.Debug("mimo: tc param match", "key", key, "val_preview", val[:min(len(val), 100)])

		// Try CDATA extraction
		val = extractCDATA(val)
		slog.Debug("mimo: tc param after CDATA", "key", key, "val_preview", val[:min(len(val), 100)])

		// Try JSON
		var jsonVal interface{}
		if json.Unmarshal([]byte(val), &jsonVal) == nil {
			args[key] = jsonVal
		} else {
			args[key] = val
		}
	}

	// Also try <parameter=KEY>VALUE</parameter> format
	eqRe := regexp.MustCompile(`(?is)<parameter=(\w+)>(.*?)</parameter>`)
	eqMatches := eqRe.FindAllStringSubmatch(inner, -1)
	if len(eqMatches) > 0 {
		slog.Debug("mimo: tc eq-param matches", "count", len(eqMatches))
	}
	for _, m := range eqMatches {
		key := m[1]
		val := strings.TrimSpace(m[2])
		val = extractCDATA(val)
		var jsonVal interface{}
		if json.Unmarshal([]byte(val), &jsonVal) == nil {
			args[key] = jsonVal
		} else {
			args[key] = val
		}
	}

	slog.Debug("mimo: tc params result", "args", fmt.Sprintf("%v", args))
	return args
}

// extractCDATA strips CDATA markers if present
func extractCDATA(val string) string {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "<![CDATA[") && strings.HasSuffix(val, "]]>") {
		val = val[9 : len(val)-3]
	}
	return val
}

// resolveToolName resolves a tool name to the canonical name from toolNames
func resolveToolName(name string, toolNames []string) string {
	nameLower := strings.ToLower(name)

	for _, tn := range toolNames {
		if tn == name {
			slog.Debug("mimo: tc resolve exact match", "name", name, "tn", tn)
			return tn
		}
		if strings.ToLower(tn) == nameLower {
			slog.Debug("mimo: tc resolve case-insensitive", "name", name, "tn", tn)
			return tn
		}
	}

	// Try snake_case match (from camelCase)
	snake := camelToSnake(name)
	for _, tn := range toolNames {
		if strings.ToLower(tn) == strings.ToLower(snake) {
			slog.Debug("mimo: tc resolve snake match", "name", name, "snake", snake, "tn", tn)
			return tn
		}
	}

	slog.Debug("mimo: tc resolve no match", "name", name, "snake_attempt", camelToSnake(name), "toolNames_len", len(toolNames))
	return ""
}

// camelToSnake converts camelCase to snake_case
func camelToSnake(s string) string {
	re := regexp.MustCompile(`([a-z0-9])([A-Z])`)
	snake := re.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
}

// cleanToolText removes tool call markup from the response text
func cleanToolText(text string) string {
	if text == "" {
		return text
	}

	// Normalize MiMoML variants first so downstream regexes match
	text = stripMiMoMLNoise(text)

	// Remove TOOL_CALL: lines
	re := regexp.MustCompile(`(?mi)^TOOL_CALL:.*$`)
	text = re.ReplaceAllString(text, "")

	// Remove MiMoML/XML wrapper tags
	re = regexp.MustCompile(`(?i)</?(?:tool_calls|function_calls|invoke|parameter)[^>]*>`)
	text = re.ReplaceAllString(text, "")

	// Remove CDATA markers
	re = regexp.MustCompile(`<!\[CDATA\[.*?\]\]>`)
	text = re.ReplaceAllString(text, "")

	// Remove <tool_call>...</tool_call>
	re = regexp.MustCompile(`(?i)<tool_call>.*?</tool_call>`)
	text = re.ReplaceAllString(text, "")

	// Remove markdown code fences with tool call JSON
	re = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?\\s*\\{.*?\"tool_call\".*?\\}\\s*\\n?\\s*```")
	text = re.ReplaceAllString(text, "")

	// Clean up excessive blank lines
	re = regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}
