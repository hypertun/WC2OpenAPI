package qwen

import (
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

	// Extract and obfuscate tool names for the prompt
	var toolNames []string
	for _, tool := range tools {
		toolNames = append(toolNames, toQwenName(tool.Function.Name))
	}

	// Build tool schemas section (names obfuscated)
	var toolSchemas []string
	for _, tool := range tools {
		schema := tool.Function
		qwenName := toQwenName(schema.Name)
		toolSchemas = append(toolSchemas, fmt.Sprintf("- %s: %s", qwenName, schema.Description))
	}

	// Build Qwen tool call instructions using the shared package (names already obfuscated)
	toolInstruction := toolcall.BuildQwenToolCallInstructions(toolNames)

	// Build the full tool prompt
	toolPrompt := fmt.Sprintf("You have access to the following tools:\n\n%s\n\n%s", strings.Join(toolSchemas, "\n"), toolInstruction)

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
