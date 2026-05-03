package toolcall

import "strings"

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
