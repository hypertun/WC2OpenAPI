package testutil

import (
	"encoding/json"
	providers "github.com/user/wc2api/internal/providers"
)

// MakeToolCall creates a providers.ToolCall for testing.
func MakeToolCall(name string, argsJSON string) providers.ToolCall {
	return providers.ToolCall{
		ID:   "call_" + name,
		Type: "function",
		Function: providers.ToolCallFunction{
			Name:      name,
			Arguments: argsJSON,
		},
	}
}

// MakeTool creates a providers.Tool for testing.
func MakeTool(name, schemaJSON string) providers.Tool {
	var params map[string]interface{}
	json.Unmarshal([]byte(schemaJSON), &params)
	return providers.Tool{
		Type: "function",
		Function: providers.ToolFunction{
			Name:        name,
			Description: "Test tool: " + name,
			Parameters:  params,
		},
	}
}

// MakeChatRequest creates a providers.ChatRequest for testing.
func MakeChatRequest(model string, tools []providers.Tool, messages []providers.Message) *providers.ChatRequest {
	return &providers.ChatRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
	}
}

// MakeStreamChatRequest creates a streaming providers.ChatRequest for testing.
func MakeStreamChatRequest(model string, tools []providers.Tool, messages []providers.Message) *providers.ChatRequest {
	req := MakeChatRequest(model, tools, messages)
	req.Stream = true
	return req
}

// MakeUserMessage creates a user message for testing.
func MakeUserMessage(content string) providers.Message {
	return providers.Message{
		Role:    "user",
		Content: providers.MessageContent(content),
	}
}

// MakeSystemMessage creates a system message for testing.
func MakeSystemMessage(content string) providers.Message {
	return providers.Message{
		Role:    "system",
		Content: providers.MessageContent(content),
	}
}

// BashToolSchema returns a JSON schema for the Bash tool.
const BashToolSchema = `{
	"type": "object",
	"properties": {
		"command": {"type": "string"},
		"timeout": {"type": "number"}
	},
	"required": ["command"]
}`

// ReadToolSchema returns a JSON schema for the Read tool.
const ReadToolSchema = `{
	"type": "object",
	"properties": {
		"file_path": {"type": "string"}
	},
	"required": ["file_path"]
}`

// CalculatorToolSchema returns a JSON schema for a calculator tool.
const CalculatorToolSchema = `{
	"type": "object",
	"properties": {
		"expr": {"type": "string"}
	},
	"required": ["expr"]
}`
