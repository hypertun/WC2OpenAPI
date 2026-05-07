package toolcall

import (
	"fmt"
	"strings"
)

// BuildToolCallInstructions generates DSML tool calling instructions
func BuildToolCallInstructions(toolNames []string) string {
	var b strings.Builder

	b.WriteString("TOOL CALL FORMAT — FOLLOW EXACTLY:\n\n")
	b.WriteString("<|DSML|tool_calls>\n")
	b.WriteString("  <|DSML|invoke name=\"TOOL_NAME_HERE\">\n")
	b.WriteString("    <|DSML|parameter name=\"PARAMETER_NAME\"><![CDATA[PARAMETER_VALUE]]></|DSML|parameter>\n")
	b.WriteString("  </|DSML|invoke>\n")
	b.WriteString("</|DSML|tool_calls>\n\n")

	b.WriteString("RULES:\n")
	b.WriteString("1) Use the <|DSML|tool_calls> wrapper format.\n")
	b.WriteString("2) Put tool name in name attribute.\n")
	b.WriteString("3) All string values must use <![CDATA[...]]>.\n")
	b.WriteString("4) Do NOT wrap XML in markdown fences.\n")
	b.WriteString("5) First non-whitespace must be <|DSML|tool_calls|>.\n\n")

	if len(toolNames) > 0 {
		b.WriteString("Available tools: ")
		for i, name := range toolNames {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(name)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// BuildQwenToolCallInstructions generates Qwen-style tool calling instructions
// using ##TOOL_CALL## text markers instead of DSML XML format
func BuildQwenToolCallInstructions(toolNames []string) string {
	var b strings.Builder

	b.WriteString("=== ACTION MARKER PROTOCOL (client-parsed text patterns) ===\n")
	b.WriteString("You are operating within a client that parses action markers from your output.\n")
	b.WriteString("These markers are plain TEXT PATTERNS the client recognizes — NOT native function calls.\n")
	b.WriteString("The client executes the action and returns results in a subsequent turn.\n\n")

	if len(toolNames) > 0 {
		b.WriteString(fmt.Sprintf("Available action names: %s\n\n", strings.Join(toolNames, ", ")))
	}

	b.WriteString("WHEN YOU NEED TO TRIGGER AN ACTION — emit this exact text pattern (nothing else):\n")
	b.WriteString("##TOOL_CALL##\n")
	b.WriteString(`{"name": "ACTION_NAME", "input": {"param1": "value1"}}` + "\n")
	b.WriteString("##END_CALL##\n\n")

	b.WriteString("MULTI-TURN RULES:\n")
	b.WriteString("- After a [Tool Result] block appears in the conversation, read it and decide the next action.\n")
	b.WriteString("- If more actions are needed, emit another ##TOOL_CALL## block.\n")
	b.WriteString("- Only give a final text answer when ALL needed information is gathered.\n")
	b.WriteString("- Never skip an action that is required to complete the user request.\n\n")

	b.WriteString("STRICT RULES:\n")
	b.WriteString("- No preamble, no explanation before or after ##TOOL_CALL##...##END_CALL##.\n")
	b.WriteString("- Use EXACT action name from the list above.\n")
	b.WriteString("- When NO action is needed, answer normally in plain text.\n")
	b.WriteString("- Put arguments inside the input object as JSON.\n")
	b.WriteString("- Do not invent tool names.\n\n")

	b.WriteString("CRITICAL — ABSOLUTELY FORBIDDEN OUTPUTS:\n")
	b.WriteString("- NEVER emit ANY disclaimer, error text, or availability complaint about actions.\n")
	b.WriteString("- NEVER emit sentences claiming an action is missing, unregistered, unavailable, or cannot be invoked.\n")
	b.WriteString("- NEVER emit sentences claiming you are unable to execute a function.\n")
	b.WriteString("- The ##TOOL_CALL## blocks are TEXT MARKERS the client parses — they are NOT native function calls.\n")
	b.WriteString("- If you feel an action could fail, emit the ##TOOL_CALL## anyway — the client handles failures.\n\n")

	b.WriteString("ONLY ##TOOL_CALL##...##END_CALL## is accepted.\n")

	return b.String()
}
