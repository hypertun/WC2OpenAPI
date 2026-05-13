package duckai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/user/wc2api/internal/providers"
)

func buildToolInstructions(tools []providers.Tool, toolChoice providers.ToolChoice) string {
	var b strings.Builder

	b.WriteString("You are an AI assistant with access to the following functions. ")
	b.WriteString("When you need to call a function, respond with a JSON object in this exact format:\n\n")
	b.WriteString("{\n")
	b.WriteString(`  "tool_calls": [`)
	b.WriteString("\n    {\n")
	b.WriteString(`      "id": "call_<unique_id>",`)
	b.WriteString("\n" + `      "type": "function",`)
	b.WriteString("\n" + `      "function": {`)
	b.WriteString("\n" + `        "name": "<function_name>",`)
	b.WriteString("\n" + `        "arguments": "<json_string_of_arguments>"`)
	b.WriteString("\n      }\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")

	b.WriteString("Available functions:\n")
	for _, tool := range tools {
		b.WriteString(fmt.Sprintf("- %s", tool.Function.Name))
		if tool.Function.Description != "" {
			b.WriteString(fmt.Sprintf(": %s", tool.Function.Description))
		}
		if tool.Function.Parameters != nil {
			props, _ := tool.Function.Parameters["properties"].(map[string]interface{})
			reqRaw, _ := tool.Function.Parameters["required"].([]interface{})
			required := make(map[string]bool)
			for _, r := range reqRaw {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
			if len(props) > 0 {
				b.WriteString("\n  Parameters:")
				for name, propRaw := range props {
					prop, _ := propRaw.(map[string]interface{})
					pType := "any"
					if t, ok := prop["type"].(string); ok {
						pType = t
					}
					desc, _ := prop["description"].(string)
					req := "optional"
					if required[name] {
						req = "required"
					}
					b.WriteString(fmt.Sprintf("\n    - %s (%s, %s): %s", name, pType, req, desc))
				}
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("Important rules:\n")
	b.WriteString("1. Only call functions when necessary to answer the user's question\n")
	b.WriteString("2. Use the exact function names provided\n")
	b.WriteString("3. Provide arguments as a JSON string\n")
	b.WriteString("4. Generate unique IDs for each tool call (e.g., call_1, call_2, etc.)\n")
	b.WriteString("5. If you don't need to call any functions, respond without JSON wrapper\n")

	switch v := toolChoice.(type) {
	case string:
		if v == "required" {
			b.WriteString("6. You MUST call at least one function to answer this request\n")
		} else if v == "none" {
			b.WriteString("6. Do NOT call any functions, respond normally\n")
		}
	case map[string]interface{}:
		if fn, ok := v["function"].(map[string]interface{}); ok {
			if name, ok := fn["name"].(string); ok {
				b.WriteString(fmt.Sprintf("6. You MUST call the function \"%s\"\n", name))
			}
		}
	}

	return b.String()
}

type duckToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function duckToolCallFunction `json:"function"`
}

type duckToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type duckToolCallsWrapper struct {
	ToolCalls []duckToolCall `json:"tool_calls"`
}

func detectToolCalls(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	var wrapper duckToolCallsWrapper
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
		return false
	}
	return len(wrapper.ToolCalls) > 0
}

func extractToolCalls(content string) []providers.ToolCall {
	trimmed := strings.TrimSpace(content)
	var wrapper duckToolCallsWrapper
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
		return nil
	}
	tcs := make([]providers.ToolCall, len(wrapper.ToolCalls))
	for i, tc := range wrapper.ToolCalls {
		tcs[i] = providers.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: providers.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return tcs
}

func wrapToolCalls(toolCalls []providers.ToolCall) string {
	wrapper := duckToolCallsWrapper{
		ToolCalls: make([]duckToolCall, len(toolCalls)),
	}
	for i, tc := range toolCalls {
		wrapper.ToolCalls[i] = duckToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: duckToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	data, _ := json.Marshal(wrapper)
	return string(data)
}
