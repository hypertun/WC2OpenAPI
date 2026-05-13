package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MessageContent handles JSON unmarshaling for content that can be string, null, or array of content parts
type MessageContent string

func (m *MessageContent) UnmarshalJSON(data []byte) error {
	// Handle null
	if string(data) == "null" {
		*m = ""
		return nil
	}
	// Handle string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*m = MessageContent(s)
		return nil
	}
	// Handle array of content parts
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err == nil {
		var sb strings.Builder
		for _, raw := range parts {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &part); err != nil {
				continue // Skip malformed parts
			}
			if part.Type == "text" {
				sb.WriteString(part.Text)
			}
		}
		*m = MessageContent(sb.String())
		return nil
	}
	return fmt.Errorf("invalid content type: %s", string(data))
}

// Message represents a chat message
type Message struct {
	Role             string         `json:"role"`
	Content          MessageContent `json:"content"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"` // For role="tool" messages
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

// Tool represents a function tool
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction represents a function definition
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction represents the function call details
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string of arguments
}


// MarshalJSON implements json.Marshaler to ensure Arguments is always a JSON string
func (t ToolCallFunction) MarshalJSON() ([]byte, error) {
	// Use a temporary struct to ensure proper marshaling
	type Alias ToolCallFunction
	return json.Marshal(Alias(t))
}

// ToolCallArgs handles flexible unmarshaling of tool call arguments
// It accepts both JSON objects and JSON strings for backwards compatibility
type ToolCallArgs struct {
	data map[string]interface{}
}

// UnmarshalJSON implements json.Unmarshaler to handle both string and object formats
func (t *ToolCallArgs) UnmarshalJSON(data []byte) error {
	// First try to unmarshal as a map (JSON object)
	var objMap map[string]interface{}
	if err := json.Unmarshal(data, &objMap); err == nil {
		t.data = objMap
		return nil
	}
	
	// If that fails, try to unmarshal as a string and then parse it
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		var parsedMap map[string]interface{}
		if err := json.Unmarshal([]byte(str), &parsedMap); err == nil {
			t.data = parsedMap
			return nil
		}
	}
	
	// Return the original error
	return fmt.Errorf("failed to unmarshal tool call arguments: %w", json.Unmarshal(data, &objMap))
}

// MarshalJSON implements json.Marshaler to serialize as JSON string
func (t ToolCallArgs) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.data)
}

// ToMap returns the underlying map
func (t ToolCallArgs) ToMap() map[string]interface{} {
	return t.data
}

// ToolChoice can be "auto", "none", or {"type":"function","function":{"name":"..."}}
type ToolChoice any

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  ToolChoice `json:"tool_choice,omitempty"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice
type Choice struct {
	Index        int       `json:"index"`
	Message      Message   `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

// Usage represents token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamResponse represents a streaming chunk
type StreamResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []StreamChoice  `json:"choices"`
}

// StreamChoice represents a streaming choice
type StreamChoice struct {
	Index        int             `json:"index"`
	Delta        Delta           `json:"delta"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

// Delta represents a streaming delta
type Delta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

// Model represents an available model
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// Provider defines the interface for AI webchat providers
type Provider interface {
	// Name returns the provider name
	Name() string

	// CreateChatCompletion creates a chat completion
	CreateChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// CreateChatCompletionStream creates a streaming chat completion
	CreateChatCompletionStream(ctx context.Context, req *ChatRequest) (<-chan StreamResponse, error)

	// ListModels returns available models
	ListModels() []Model

	// Close cleans up the provider
	Close() error
}
