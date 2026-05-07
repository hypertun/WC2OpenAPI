package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/user/wc2api/internal/providers"
)

// CreateChatCompletion creates a non-streaming chat completion
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	if err := c.ensureAuthenticated(); err != nil {
		return nil, err
	}

	// Get PoW header (required by DeepSeek)
	powHeader, err := c.GetPow(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get PoW: %w", err)
	}

	// Convert OpenAI format to DeepSeek format
	dsReq := c.convertRequest(req)
	dsReq.Stream = false

	slog.Debug("Creating chat completion",
		"model", req.Model,
		"message_count", len(req.Messages),
	)

	// Debug: log the request payload
	reqJSON, _ := json.Marshal(dsReq)
	slog.Debug("Request payload", "payload", string(reqJSON))

	resp, err := c.doRequestWithRetryAndPow(ctx, "POST", completionURL, dsReq, powHeader)
	if err != nil {
		return nil, fmt.Errorf("completion request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	slog.Debug("DeepSeek completion response", "body", string(body))

	// Parse SSE response (DeepSeek returns SSE format even for non-streaming requests)
	content, toolCalls := parseSSEContent(string(body), req.Tools)

	// Build response
	chatResp := &providers.ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []providers.Choice{
			{
				Index: 0,
				Message: providers.Message{
					Role: "assistant",
				},
				FinishReason: "stop",
			},
		},
		Usage: providers.Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	// If tool calls detected, add them to the response
	if len(toolCalls) > 0 {
		chatResp.Choices[0].Message.ToolCalls = toolCalls
		chatResp.Choices[0].FinishReason = "tool_calls"
	} else {
			chatResp.Choices[0].Message.Content = providers.MessageContent(content)
	}

	return chatResp, nil
}

// parseSSEContent extracts content from SSE response using proper JSON parsing
// Also detects tool calls in the response
func parseSSEContent(sseBody string, tools []providers.Tool) (string, []providers.ToolCall) {
	lines := strings.Split(sseBody, "\n")
	var content strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" || data == "[DONE]" {
			continue
		}

		// First try to parse as a simple string V (most common case)
		var simple struct {
			V string `json:"v"`
			P string `json:"p"`
			O string `json:"o"`
		}
		if err := json.Unmarshal([]byte(data), &simple); err == nil && simple.V != "" {
			// This is an incremental content update
			if simple.P == "" || (simple.P != "" && simple.O == "APPEND") {
				content.WriteString(simple.V)
			}
			continue
		}

		// Try to parse as full response with nested v.response.fragments
		var resp struct {
			V any `json:"v"`
		}
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}

		// Handle V field which can be a nested object with response
		if resp.V != nil {
			if vMap, ok := resp.V.(map[string]interface{}); ok {
				if response, ok := vMap["response"].(map[string]interface{}); ok {
					if fragments, ok := response["fragments"].([]interface{}); ok {
						for _, f := range fragments {
							if frag, ok := f.(map[string]interface{}); ok {
								if c, ok := frag["content"].(string); ok && c != "" {
									content.WriteString(c)
								}
							}
						}
					}
				}
			}
		}
	}

	fullText := strings.TrimSpace(content.String())

	// Check for tool calls in the response
	toolCalls, err := parseToolCallsFromText(fullText, tools)
	if err != nil {
		slog.Debug("Failed to parse tool calls", "error", err, "text", fullText)
	}

	// If tool calls found, return empty content (tool calls will be used instead)
	if len(toolCalls) > 0 {
		return "", toolCalls
	}

	return fullText, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func stringPtr(s string) *string {
	return &s
}

// CreateChatCompletionStream creates a streaming chat completion
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	if err := c.ensureAuthenticated(); err != nil {
		return nil, err
	}

	// Get PoW header (required by DeepSeek)
	powHeader, err := c.GetPow(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get PoW: %w", err)
	}

	// Convert OpenAI format to DeepSeek format
	dsReq := c.convertRequest(req)
	dsReq.Stream = true

	slog.Debug("Creating streaming chat completion",
		"model", req.Model,
		"message_count", len(req.Messages),
	)

	resp, err := c.doRequestWithRetryAndPow(ctx, "POST", completionURL, dsReq, powHeader)
	if err != nil {
		return nil, fmt.Errorf("stream request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	// Create channel for streaming responses
	streamChan := make(chan providers.StreamResponse)

	// Start goroutine to process stream
	go func() {
		defer close(streamChan)
		defer resp.Body.Close()

		// Panic recovery to ensure we always close the channel and process buffer
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Panic in stream goroutine", "recover", r)
			}
		}()

		reader := bufio.NewReader(resp.Body)
		msgID := generateMessageID()
		created := time.Now().Unix()

		// Buffer to collect full response for tool call detection
		// We don't send chunks immediately - we buffer everything first
		var contentBuffer strings.Builder
		var allChunks []string // Store all content chunks for replay if no tool calls
		var streamError error
		var errorOccurred bool

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					// Stream ended normally, process buffered content
					break
				}
				slog.Error("Stream read error", "error", err)
				streamError = err
				errorOccurred = true
				break
			}

			// Parse SSE line
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// Stream ended with [DONE] marker
				fullText := contentBuffer.String()
				handleStreamEnd(streamChan, msgID, created, req.Model, fullText, allChunks, req.Tools)
				return
			}

			// Parse DeepSeek SSE response
			// First try simple string V
			var simple struct {
				V string `json:"v"`
				P string `json:"p"`
				O string `json:"o"`
			}
			if err := json.Unmarshal([]byte(data), &simple); err == nil && simple.V != "" {
				if simple.P == "" || (simple.P != "" && simple.O == "APPEND") {
					contentBuffer.WriteString(simple.V)
					allChunks = append(allChunks, simple.V)
				}
				continue
			}

			// Try nested v.response.fragments
			var resp struct {
				V any `json:"v"`
			}
			if err := json.Unmarshal([]byte(data), &resp); err != nil {
				slog.Debug("Failed to parse stream chunk", "error", err, "data", data)
				continue
			}

			// Extract content from nested format
			if resp.V != nil {
				if vMap, ok := resp.V.(map[string]interface{}); ok {
					if response, ok := vMap["response"].(map[string]interface{}); ok {
						if fragments, ok := response["fragments"].([]interface{}); ok {
							for _, f := range fragments {
								if frag, ok := f.(map[string]interface{}); ok {
									if c, ok := frag["content"].(string); ok && c != "" {
										contentBuffer.WriteString(c)
										allChunks = append(allChunks, c)
									}
								}
							}
						}
					}
				}
			}
		}

		// Process buffered content at end of stream
		fullText := contentBuffer.String()
		if errorOccurred {
			// If there was a stream error, still try to send what we have
			slog.Warn("Stream ended with error, sending partial content", 
				"error", streamError, 
				"buffered_chunks", len(allChunks),
				"buffered_length", len(fullText))
		}
		handleStreamEnd(streamChan, msgID, created, req.Model, fullText, allChunks, req.Tools)
	}()
	return streamChan, nil
}

// convertRequest converts OpenAI request to DeepSeek format
func (c *Client) convertRequest(req *providers.ChatRequest) *deepseekRequest {
	// Map model names (replace hyphens with underscores for DeepSeek API)
	model := strings.ReplaceAll(req.Model, "-", "_")
	// Remove -nothinking suffix from model name (handled separately)
	model = strings.TrimSuffix(model, "_nothinking")

	// Get model type based on model name
	modelType := getModelType(req.Model)

	// Inject tool prompt if tools are present
	messages := req.Messages
	if len(req.Tools) > 0 {
		messages = injectToolPrompt(req.Messages, req.Tools, req.ToolChoice)
	}

	// Convert messages to prompt string (DS2API-style)
	prompt := c.formatMessagesToPrompt(messages)

	return &deepseekRequest{
		Model:         model,
		ModelType:     modelType,
		Prompt:        prompt,
		RefFileIDs:    []string{},
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		Stream:        req.Stream,
		ChatSessionID: c.sessionID,
	}
}

// formatMessagesToPrompt converts OpenAI messages to DeepSeek prompt string
func (c *Client) formatMessagesToPrompt(messages []providers.Message) string {
	var promptParts []string
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			promptParts = append(promptParts, "System: "+string(msg.Content))
		case "user":
			promptParts = append(promptParts, "User: "+string(msg.Content))
		case "assistant":
			// Handle assistant messages with or without tool calls
			content := string(msg.Content)
			if len(msg.ToolCalls) > 0 {
				// Format tool calls for context
				var toolCallParts []string
				for _, tc := range msg.ToolCalls {
					toolCallParts = append(toolCallParts, fmt.Sprintf(
						`<|DSML|invoke name="%s">`+
						`<|DSML|parameter name="arguments"><![CDATA[%s]]></|DSML|parameter>`+
						`</|DSML|invoke>`,
						tc.Function.Name,
						tc.Function.Arguments,
					))
				}
				if content != "" {
					promptParts = append(promptParts, "Assistant: "+content+"\n<|DSML|tool_calls>\n"+strings.Join(toolCallParts, "\n")+"\n</|DSML|tool_calls>")
				} else {
					promptParts = append(promptParts, "Assistant: <|DSML|tool_calls>\n"+strings.Join(toolCallParts, "\n")+"\n</|DSML|tool_calls>")
				}
			} else {
				promptParts = append(promptParts, "Assistant: "+content)
			}
		case "tool":
			// Tool results are critical for continuing tool conversations
			// They should be formatted as assistant context showing the tool output
			promptParts = append(promptParts, "Tool result: "+string(msg.Content))
		}
	}
	return strings.Join(promptParts, "\n")
}

func generateMessageID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}

// DeepSeek request types

type deepseekRequest struct {
	Model         string            `json:"model"`
	ModelType     string            `json:"model_type"`
	Prompt        string            `json:"prompt"`
	RefFileIDs    []string          `json:"ref_file_ids"`
	Messages      []deepseekMessage `json:"messages,omitempty"`
	Temperature   float64           `json:"temperature,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Stream        bool              `json:"stream"`
	ChatSessionID string            `json:"chat_session_id,omitempty"`
}

type deepseekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// handleStreamEnd processes the buffered stream content and sends appropriate responses
func handleStreamEnd(streamChan chan providers.StreamResponse, msgID string, created int64, model string, fullText string, allChunks []string, tools []providers.Tool) {
	// Check for tool calls in the full response
	toolCalls, err := parseToolCallsFromText(fullText, tools)
	if err != nil {
		slog.Debug("Failed to parse tool calls in stream", "error", err, "text", fullText)
	}

	if len(toolCalls) > 0 {
		// Send tool calls as a single chunk (no DSML content to client)
		streamChan <- providers.StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []providers.StreamChoice{
				{
					Index: 0,
					Delta: providers.Delta{
						ToolCalls: toolCalls,
					},
					FinishReason: stringPtr("tool_calls"),
				},
			},
		}
	} else {
		// No tool calls, replay all content chunks
		for _, chunk := range allChunks {
			streamChan <- providers.StreamResponse{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model,
				Choices: []providers.StreamChoice{
					{
						Index: 0,
						Delta: providers.Delta{
							Content: chunk,
						},
					},
				},
			}
		}
		// Send finish message
		finishReason := "stop"
		streamChan <- providers.StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []providers.StreamChoice{
				{
					Index:        0,
					Delta:        providers.Delta{},
					FinishReason: &finishReason,
				},
			},
		}
	}
}
