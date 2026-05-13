package deepseek

import (
	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

// injectToolPrompt injects tool schemas into the system prompt using DSML format
// Based on ds2api's implementation
func injectToolPrompt(messages []providers.Message, tools []providers.Tool, toolChoice providers.ToolChoice) []providers.Message {
	if len(tools) == 0 {
		return messages
	}

	// Build DSML tool call instructions using the shared package
	toolPrompt := toolcall.BuildToolCallInstructions(tools)

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
