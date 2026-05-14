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

// Deprecated: use compressSchema instead. Kept for backward compatibility.
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
	// Use parameter hints from schema
	hints := buildParameterHints(tool.Function.Parameters)

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

// BuildToolCallInstructions generates DSML tool calling instructions
func BuildToolCallInstructions(tools []providers.Tool) string {
	var b strings.Builder

	b.WriteString("TOOL CALL FORMAT — FOLLOW EXACTLY:\n\n")
	b.WriteString("<|DSML|tool_calls>\n")
	b.WriteString("  <|DSML|invoke name=\"TOOL_NAME_HERE\">\n")
	b.WriteString("    <|DSML|parameter name=\"PARAMETER_NAME\"><![CDATA[PARAMETER_VALUE]]></|DSML|parameter>\n")
	b.WriteString("  </|DSML|invoke>\n")
	b.WriteString("</|DSML|tool_calls>\n\n")

	if len(tools) > 0 {
		b.WriteString("AVAILABLE TOOLS:\n")
		for _, tool := range tools {
			b.WriteString(toolDescriptionWithHints(tool))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("CORRECT EXAMPLE:\n")
	b.WriteString("<|DSML|tool_calls>\n")
	b.WriteString("  <|DSML|invoke name=\"Read\">\n")
	b.WriteString("    <|DSML|parameter name=\"file_path\"><![CDATA[/path/to/file]]></|DSML|parameter>\n")
	b.WriteString("  </|DSML|invoke>\n")
	b.WriteString("</|DSML|tool_calls>\n\n")

	b.WriteString("RULES:\n")
	b.WriteString("1) Use the <|DSML|tool_calls> wrapper format.\n")
	b.WriteString("2) Put tool name in name attribute.\n")
	b.WriteString("3) All string values must use <![CDATA[...]]>.\n")
	b.WriteString("4) Do NOT wrap XML in markdown fences.\n")
	b.WriteString("5) First non-whitespace must be <|DSML|tool_calls|>.\n")
	b.WriteString("6) Use EXACT parameter names as listed in AVAILABLE TOOLS.\n")
	b.WriteString("7) Each parameter must match its expected type (strings in CDATA, numbers without quotes).\n\n")

	b.WriteString("COMMON MISTAKES:\n")
	b.WriteString("- Wrong parameter name: \"path\" instead of \"file_path\"\n")
	b.WriteString("- Missing required parameters\n")
	b.WriteString("- Wrong type: \"command\": 123 instead of \"command\": \"ls -la\"\n")
	b.WriteString("- Extra unknown parameters not listed in AVAILABLE TOOLS\n\n")

	return b.String()
}

// BuildQwenToolCallInstructions generates Qwen-style tool calling instructions
// using ##TOOL_CALL## text markers instead of DSML XML format
func BuildQwenToolCallInstructions(tools []providers.Tool) string {
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

	return b.String()
}


