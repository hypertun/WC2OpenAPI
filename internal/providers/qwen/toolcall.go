package qwen

import (
	"encoding/json"
	"fmt"
	"strings"

	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

// injectToolPrompt injects ##TOOL_CALL## tool call instructions into the system prompt
// This follows qwen2api's approach of using text markers instead of native function calling
func injectToolPrompt(messages []providers.Message, tools []providers.Tool, toolChoice providers.ToolChoice) []providers.Message {
	if len(tools) == 0 {
		return messages
	}

	// Create obfuscated copy of tools with Qwen-safe names
	obfuscatedTools := make([]providers.Tool, len(tools))
	for i, tool := range tools {
		obfuscatedTools[i] = tool
		obfuscatedTools[i].Function.Name = toQwenName(tool.Function.Name)
	}

	// Build Qwen tool call instructions using the shared package (names already obfuscated)
	toolInstruction := toolcall.BuildQwenToolCallInstructions(obfuscatedTools)

	// Build the full tool prompt
	toolPrompt := fmt.Sprintf("You have access to the following actions:\n\n%s", toolInstruction)

	// Add few-shot example to improve tool diversity
	if len(obfuscatedTools) > 0 {
		toolPrompt += injectFewShotExample(obfuscatedTools)
	}

	// Obfuscate bare tool name mentions in the instruction text itself
	toolPrompt = obfuscateBareNames(toolPrompt)

	// Handle tool_choice policy
	switch tc := toolChoice.(type) {
	case string:
		if tc == "none" {
			// Don't add tool prompt if tool_choice=none
			return messages
		}
		if tc == "required" {
			// Force tool usage
			toolPrompt += "\n\nIMPORTANT: You MUST call one of the available tools. Do not respond without calling a tool."
		}
	// "auto" - proceed normally
	}

	// Inject tool prompt into system message or create new one
	result := make([]providers.Message, len(messages))
	toolPromptInjected := false
	for i, msg := range messages {
		if msg.Role == "system" && !toolPromptInjected {
			result[i] = providers.Message{
				Role:    "system",
				Content: providers.MessageContent(string(msg.Content) + "\n\n" + toolPrompt),
			}
			toolPromptInjected = true
		} else {
			result[i] = msg
		}
	}

	// If no system message found, prepend one
	if !toolPromptInjected {
		result = append([]providers.Message{{Role: "system", Content: providers.MessageContent(toolPrompt)}}, result...)
	}

	return result
}

// injectFewShotExample generates a synthetic multi-turn tool usage example to improve tool diversity.
// It selects up to two tools from the provided list and constructs a short conversation snippet.
func injectFewShotExample(tools []providers.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	// Select up to two distinct tools
	selected := make([]providers.Tool, 0, 2)
	for _, tool := range tools {
		// Skip tools with no parameters? Not necessary.
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
		obfuscatedName := toQwenName(tool.Function.Name)
		// Build a generic input based on parameter hints
		input := buildExampleInput(tool.Function.Parameters)
		b.WriteString(fmt.Sprintf("##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##\n", obfuscatedName, input))
	}
	b.WriteString("\n[Tool Results]\n")
	for _, tool := range selected {
		obfuscatedName := toQwenName(tool.Function.Name)
		result := buildExampleResult(tool.Function.Name)
		b.WriteString(fmt.Sprintf("[%s Result]: %s\n", obfuscatedName, result))
	}
	b.WriteString("\n[Assistant]: Based on the results, here is my analysis.\n")
	return b.String()
}

// buildExampleInput creates a minimal example input JSON for a tool based on its schema.
// It uses placeholder values for required parameters and omits optional ones.
func buildExampleInput(params map[string]interface{}) string {
	if params == nil {
		return "{}"
	}
	// Get required fields
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
		// If required, provide a placeholder based on type
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
	// Marshal to JSON
	jsonBytes, err := json.Marshal(example)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

// buildExampleResult provides a placeholder result string for a given tool name.
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
