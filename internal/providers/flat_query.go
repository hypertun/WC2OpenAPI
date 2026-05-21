package providers

import (
	"fmt"
	"strings"
)

// BuildFlatQuery constructs a flat prompt string from messages using role prefixes.
// This format is used by providers that don't support structured message arrays
// (e.g., MiMo, StepFun). Messages are concatenated with role prefixes:
//
//	system: <content>
//	user: <content>
//	assistant: <content>
//	TOOL_CALL: <name>(<arguments>)
//
// This matches the flat-prompt format expected by those providers' APIs.
func BuildFlatQuery(messages []Message) string {
	var parts []string
	var systemText string

	for _, msg := range messages {
		content := string(msg.Content)

		switch msg.Role {
		case "system":
			systemText = content
			continue
		case "user":
			parts = append(parts, "user: "+content)
	case "assistant":
		if len(msg.ToolCalls) > 0 {
			var tcLines []string
			for _, tc := range msg.ToolCalls {
				// Format tool calls using the ##TOOL_CALL## marker format that matches the injection prompt
				// This ensures consistent tool-call representation across all turns
				tcLines = append(tcLines, fmt.Sprintf("##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##", tc.Function.Name, tc.Function.Arguments))
			}
			if content != "" {
				parts = append(parts, "assistant: "+content+"\n"+strings.Join(tcLines, "\n"))
			} else {
				parts = append(parts, "assistant: "+strings.Join(tcLines, "\n"))
			}
		} else {
			parts = append(parts, "assistant: "+content)
		}
		case "tool":
			clean := strings.TrimPrefix(content, "[TOOL_RESULT]")
			parts = append(parts, "user: [tool_result id="+msg.ToolCallID+"] "+clean)
		}
	}

	// Prepend system message if present
	if systemText != "" {
		parts = append([]string{"system: " + systemText}, parts...)
	}

	query := strings.Join(parts, "\n")
	if query == "" {
		query = "Hello"
	}

	return query
}
