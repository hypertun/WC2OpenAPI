package toolcall

import (
	"fmt"
	"sort"
	"strings"

	"github.com/user/wc2api/internal/providers"
)

// compressSchema converts a JSON Schema into a concise TypeScript-like signature.
// Example: {"type":"object","properties":{"file_path":{"type":"string"},"encoding":{"type":"string","enum":["utf-8","base64"]}},"required":["file_path"]}
// Output: {file_path!: string, encoding?: utf-8|base64}
func compressSchema(schema map[string]interface{}) string {
	if schema == nil {
		return "{}"
	}

	propsRaw, ok := schema["properties"]
	if !ok {
		return "{}"
	}
	props, ok := propsRaw.(map[string]interface{})
	if !ok || len(props) == 0 {
		return "{}"
	}

	required := map[string]bool{}
	if reqRaw, ok := schema["required"]; ok {
		if reqList, ok := reqRaw.([]interface{}); ok {
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
	}

	var paramStrs []string
	// Sort property names for stable output
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		propRaw := props[name]
		prop, ok := propRaw.(map[string]interface{})
		if !ok {
			continue
		}
		// Get base type
		paramType := "any"
		if t, ok := prop["type"]; ok {
			if ts, ok := t.(string); ok {
				paramType = ts
			}
		}
		// Handle enum
		if enumRaw, ok := prop["enum"]; ok {
			if enumList, ok := enumRaw.([]interface{}); ok {
				enumStrs := make([]string, len(enumList))
				for i, e := range enumList {
					if es, ok := e.(string); ok {
						enumStrs[i] = es
					} else {
						enumStrs[i] = fmt.Sprintf("%v", e)
					}
				}
				if len(enumStrs) > 0 {
					paramType = strings.Join(enumStrs, "|")
				}
			}
		}
		// Build parameter representation
		reqMark := "?"
		if required[name] {
			reqMark = "!"
		}
		paramStrs = append(paramStrs, fmt.Sprintf("%s%s: %s", name, reqMark, paramType))
	}

	return "{" + strings.Join(paramStrs, ", ") + "}"
}

// Deprecated: use compressSchema instead. Kept for backward compatibility. Will be removed in a future version.
func buildParameterHints(schema map[string]interface{}) string {
	if schema == nil {
		return ""
	}
	propsRaw, ok := schema["properties"]
	if !ok {
		return ""
	}
	props, ok := propsRaw.(map[string]interface{})
	if !ok || len(props) == 0 {
		return ""
	}

	required := map[string]bool{}
	if reqRaw, ok := schema["required"]; ok {
		if reqList, ok := reqRaw.([]interface{}); ok {
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
	}

	var paramStrs []string
	for name, propRaw := range props {
		prop, ok := propRaw.(map[string]interface{})
		if !ok {
			continue
		}
		paramType := "any"
		if t, ok := prop["type"]; ok {
			if ts, ok := t.(string); ok {
				paramType = ts
			}
		}
		if required[name] {
			paramStrs = append(paramStrs, fmt.Sprintf("%s: %s (required)", name, paramType))
		} else {
			paramStrs = append(paramStrs, fmt.Sprintf("%s: %s (optional)", name, paramType))
		}
	}
	sort.Strings(paramStrs)
	return strings.Join(paramStrs, ", ")
}

func toolDescriptionWithHints(tool providers.Tool) string {
	name := tool.Function.Name
	desc := tool.Function.Description
	// Use parameter schema compression for better hints
	hints := compressSchema(tool.Function.Parameters)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  - %s", name))
	if hints != "" {
		b.WriteString(fmt.Sprintf("(%s)", hints))
	}
	if desc != "" {
		b.WriteString(fmt.Sprintf(" — %s", desc))
	}
	return b.String()
}

// BuildMarkerPrompt generates tool calling instructions using ##TOOL_CALL## text markers.
// This is the canonical prompt format used by all providers.
// If includeFlexibilityNote is true, a note is appended telling the LLM that XML formats
// are also accepted — giving flexibility while preferring the marker format.
func BuildMarkerPrompt(tools []providers.Tool, includeFlexibilityNote bool) string {
	var b strings.Builder

	b.WriteString("=== ACTION MARKER PROTOCOL (client-parsed text patterns) ===\n")
	b.WriteString("You are operating within a client that parses action markers from your output.\n")
	b.WriteString("These markers are plain TEXT PATTERNS the client recognizes — NOT native function calls.\n")
	b.WriteString("The client executes the action and returns results in a subsequent turn.\n\n")

	b.WriteString("AVAILABLE ACTIONS:\n")
	for _, tool := range tools {
		b.WriteString(toolDescriptionWithHints(tool))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("WHEN YOU NEED TO TRIGGER AN ACTION — emit this exact text pattern (nothing else):\n")
	b.WriteString("##TOOL_CALL##\n")
	b.WriteString(`{"name": "ACTION_NAME", "input": {"param1": "value1"}}` + "\n")
	b.WriteString("##END_CALL##\n\n")

	b.WriteString("CORRECT EXAMPLE:\n")
	b.WriteString("##TOOL_CALL##\n")
	b.WriteString(`{"name": "Read", "input": {"filePath": "/path/to/file"}}` + "\n")
	b.WriteString("##END_CALL##\n\n")

	b.WriteString("MULTI-TURN RULES:\n")
	b.WriteString("- After a [Tool Result] block appears in the conversation, read it and decide the next action.\n")
	b.WriteString("- If more actions are needed, emit another ##TOOL_CALL## block.\n")
	b.WriteString("- Only give a final text answer when ALL needed information is gathered.\n")
	b.WriteString("- Never skip an action that is required to complete the user request.\n\n")

	b.WriteString("STRICT RULES:\n")
	b.WriteString("- No preamble, no explanation before or after ##TOOL_CALL##...##END_CALL##.\n")
	b.WriteString("- Use EXACT action name from the list above.\n")
	b.WriteString("- Use EXACT parameter names as listed in AVAILABLE ACTIONS.\n")
	b.WriteString("- When NO action is needed, answer normally in plain text.\n")
	b.WriteString("- Put arguments inside the input object as JSON.\n")
	b.WriteString("- Do not invent tool names.\n")
	b.WriteString("- Each parameter must match its expected type.\n\n")

	b.WriteString("VALIDATION CHECKLIST (before emitting ##TOOL_CALL##):\n")
	b.WriteString("- All required parameters are present in the input object\n")
	b.WriteString("- Parameter names match exactly (use names from AVAILABLE ACTIONS)\n")
	b.WriteString("- Parameter types are correct (strings in quotes, numbers without quotes, booleans as true/false)\n")
	b.WriteString("- No extra parameters beyond those listed in the schema\n\n")

	b.WriteString("COMMON MISTAKES:\n")
	b.WriteString("- Wrong: " + `{"input": {"path": "/file"}}` + " — use \"filePath\", not \"path\"\n")
	b.WriteString("- Wrong: " + `{"input": {"command": 123}}` + " — use a string, not a number\n")
	b.WriteString("- Wrong: " + `{"input": {}}` + " (missing required params)\n")
	b.WriteString("- Wrong: extra params not in schema\n\n")

	b.WriteString("CRITICAL — ABSOLUTELY FORBIDDEN OUTPUTS:\n")
	b.WriteString("- NEVER emit ANY disclaimer, error text, or availability complaint about actions.\n")
	b.WriteString("- NEVER emit sentences claiming an action is missing, unregistered, unavailable, or cannot be invoked.\n")
	b.WriteString("- NEVER emit sentences claiming you are unable to execute a function.\n")
	b.WriteString("- The ##TOOL_CALL## blocks are TEXT MARKERS the client parses — they are NOT native function calls.\n")
	b.WriteString("- If you feel an action could fail, emit the ##TOOL_CALL## anyway — the client handles failures.\n\n")

	b.WriteString("ONLY ##TOOL_CALL##...##END_CALL## is accepted.\n")

	if includeFlexibilityNote {
		b.WriteString("\nNOTE: While the ##TOOL_CALL##...##END_CALL## marker format is preferred, you may also output XML tool call formats (<tool_calls>, <|MiMoML|tool_calls>, or <function_calls>) if that feels more natural — the client handles both correctly.\n")
	}

	return b.String()
}

// truncateFirstSentence extracts the first sentence from text (up to `.`, `!`, or newline).
func truncateFirstSentence(s string) string {
	maxLen := 80
	for i, c := range s {
		if c == '.' || c == '!' || c == '\n' {
			return s[:i+1]
		}
		if i >= maxLen {
			return s[:maxLen]
		}
	}
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// BuildCompactMarkerPrompt generates a compact version of the tool calling instructions.
// It reduces boilerplate significantly while keeping parameter hints and the format block.
// Tool descriptions are truncated to their first sentence to save tokens.
// This is optimized for small-context providers like StepFun.
func BuildCompactMarkerPrompt(tools []providers.Tool) string {
	var b strings.Builder

	b.WriteString("Call tools with ##TOOL_CALL## markers.\n")
	b.WriteString("Format: ##TOOL_CALL##\\n{\"name\": \"TOOL_NAME\", \"input\": {...}}\\n##END_CALL##\n\n")

	b.WriteString("AVAILABLE ACTIONS:\n")
	for _, tool := range tools {
		name := tool.Function.Name
		hints := compressSchema(tool.Function.Parameters)

		b.WriteString("  - " + name)
		if hints != "" {
			b.WriteString(fmt.Sprintf("(%s)", hints))
		}
		if tool.Function.Description != "" {
			// Use first sentence only
			desc := truncateFirstSentence(tool.Function.Description)
			b.WriteString(fmt.Sprintf(" — %s", desc))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("RULES: Use exact names/params from above. Required params must be present. No preamble before ##TOOL_CALL##. Continue after [Tool Result] until done.\n")
	b.WriteString("FORBIDDEN: Never claim a tool is unavailable or cannot be invoked.\n")

	return b.String()
}

// injectFewShotExample generates a few-shot tool call example for up to the first 2 tools.
// The obfuscator function is applied to tool names in the example output.
func injectFewShotExample(tools []providers.Tool, obfuscator func(string) string) string {
	if len(tools) == 0 {
		return ""
	}
	if obfuscator == nil {
		obfuscator = func(s string) string { return s }
	}
	selected := make([]providers.Tool, 0, 2)
	for _, tool := range tools {
		selected = append(selected, tool)
		if len(selected) >= 2 {
			break
		}
	}
	if len(selected) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n=== FEW-SHOT EXAMPLE ===\n\n")
	b.WriteString("[User]: Analyze the data and list files.\n\n")
	b.WriteString("[Assistant]:\n")
	for _, tool := range selected {
		obfuscatedName := obfuscator(tool.Function.Name)
		input := BuildExampleInput(tool.Function.Parameters)
		b.WriteString(fmt.Sprintf("##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##\n", obfuscatedName, input))
	}
	b.WriteString("\n[Tool Results]\n")
	for _, tool := range selected {
		obfuscatedName := obfuscator(tool.Function.Name)
		result := BuildExampleResult(tool.Function.Name)
		b.WriteString(fmt.Sprintf("[%s Result]: %s\n", obfuscatedName, result))
	}
	b.WriteString("\n[Assistant]: Based on the results, here is my analysis.\n")
	return b.String()
}


