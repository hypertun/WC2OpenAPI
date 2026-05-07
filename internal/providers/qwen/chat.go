package qwen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

// CreateChatCompletion creates a chat completion with Qwen
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	// Check if we need to refresh token
	if err := c.ensureAuthenticated(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Create chat session first
	chatID, err := c.createChatSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create chat session: %w", err)
	}

	// Build request payload
	payload := c.convertRequest(chatID, req)

	// Send request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+completionURL+"?chat_id="+chatID, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/event-stream, application/json, text/plain, */*")
	httpReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	httpReq.Header.Set("Referer", c.config.BaseURL+"/")
	httpReq.Header.Set("Origin", c.config.BaseURL)
	httpReq.Header.Set("Connection", "keep-alive")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse SSE response (Qwen returns SSE even for non-streaming)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	slog.Debug("Qwen completion response", "body_preview", string(body)[:min(len(body), 500)])

	// Parse SSE content
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

// parseSSEContent extracts content from SSE response
func parseSSEContent(sseBody string, tools []providers.Tool) (string, []providers.ToolCall) {
	lines := strings.Split(sseBody, "\n")
	var content strings.Builder
	var allToolCalls []providers.ToolCall

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" || data == "[DONE]" {
			continue
		}

		// Parse JSON
		var evt struct {
			Choices []struct {
				Delta struct {
					Content string                 `json:"content"`
					Phase   string                 `json:"phase"`
					Status  string                 `json:"status"`
					Extra   map[string]interface{} `json:"extra"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		if len(evt.Choices) == 0 {
			continue
		}

		delta := evt.Choices[0].Delta
		phase := delta.Phase
		contentStr := delta.Content

		// Handle different phases
		switch phase {
		case "think", "thinking_summary":
			// Reasoning content - append to content
			content.WriteString(contentStr)
		case "tool_call":
			// Native tool call - buffer as regular content; tool calls detected via marker parsing on fullText
			content.WriteString(contentStr)
		default: // "answer" or empty
			if contentStr != "" {
				content.WriteString(contentStr)
			}
		}
	}

	fullText := strings.TrimSpace(content.String())

	// Check for ##TOOL_CALL## markers in the full text
	toolCalls, err := parseToolCallsFromText(fullText, tools)
	if err != nil {
		slog.Debug("Failed to parse tool calls from text", "error", err)
	}

	if len(toolCalls) > 0 {
		return "", toolCalls
	}

	return fullText, allToolCalls
}

// CreateChatCompletionStream creates a streaming chat completion with Qwen
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	// Check if we need to refresh token
	if err := c.ensureAuthenticated(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Create chat session first
	chatID, err := c.createChatSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create chat session: %w", err)
	}

	// Build request payload
	payload := c.convertRequest(chatID, req)

	// Create response channel
	streamChan := make(chan providers.StreamResponse, 10)

	go func() {
		defer close(streamChan)

		// Send request
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+completionURL+"?chat_id="+chatID, bytes.NewReader(payload))
		if err != nil {
			streamChan <- providers.StreamResponse{
				ID:     "",
				Object: "error",
				Model:  req.Model,
				Choices: []providers.StreamChoice{
					{
						Index: 0,
						Delta: providers.Delta{Content: "Failed to create request"},
					},
				},
			}
			return
		}

		// Set headers
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		httpReq.Header.Set("Accept", "text/event-stream, application/json, text/plain, */*")
		httpReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		httpReq.Header.Set("Referer", c.config.BaseURL+"/")
		httpReq.Header.Set("Origin", c.config.BaseURL)
		httpReq.Header.Set("Connection", "keep-alive")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			streamChan <- providers.StreamResponse{
				ID:     "",
				Object: "error",
				Model:  req.Model,
				Choices: []providers.StreamChoice{
					{
						Index: 0,
						Delta: providers.Delta{Content: "Request failed"},
					},
				},
			}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			streamChan <- providers.StreamResponse{
				ID:     "",
				Object: "error",
				Model:  req.Model,
				Choices: []providers.StreamChoice{
					{
						Index: 0,
						Delta: providers.Delta{Content: fmt.Sprintf("Request failed with status %d", resp.StatusCode)},
					},
				},
			}
			return
		}

		// Parse SSE stream
		reader := bufio.NewReader(resp.Body)
		msgID := generateMessageID()
		created := time.Now().Unix()

		// Buffer to collect full response for tool call detection
		var contentBuffer strings.Builder
		var sawToolCallPhase bool

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					// Stream ended, process buffered content
					break
				}
				slog.Error("Stream read error", "error", err)
				break
			}

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
			if sawToolCallPhase {
				toolCalls, err := parseToolCallsFromText(fullText, req.Tools)
				if err != nil {
					slog.Debug("Failed to parse tool calls in stream", "error", err, "text", fullText[:min(len(fullText), 300)])
				}
				if len(toolCalls) > 0 {
					streamChan <- providers.StreamResponse{
						ID:      msgID,
						Object:  "chat.completion.chunk",
						Created: created,
						Model:   req.Model,
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
					finishReason := "stop"
					streamChan <- providers.StreamResponse{
						ID:      msgID,
						Object:  "chat.completion.chunk",
						Created: created,
						Model:   req.Model,
						Choices: []providers.StreamChoice{
							{
								Index:        0,
								Delta:        providers.Delta{},
								FinishReason: &finishReason,
							},
						},
					}
				}
			} else {
				finishReason := "stop"
				streamChan <- providers.StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
					Choices: []providers.StreamChoice{
						{
							Index:        0,
							Delta:        providers.Delta{},
							FinishReason: &finishReason,
						},
					},
				}
			}
			return
		}

			// Parse SSE event
			var evt struct {
				Choices []struct {
					Delta struct {
						Content string                 `json:"content"`
						Phase   string                 `json:"phase"`
						Status  string                 `json:"status"`
						Extra   map[string]interface{} `json:"extra"`
					} `json:"delta"`
				} `json:"choices"`
			}

			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				continue
			}

			if len(evt.Choices) == 0 {
				continue
			}

			delta := evt.Choices[0].Delta
			phase := delta.Phase
			contentStr := delta.Content

			// Handle different phases
			switch phase {
		case "think", "thinking_summary":
			// Reasoning content - buffer for tool call detection and stream immediately
			contentBuffer.WriteString(contentStr)
			streamChan <- providers.StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
					Choices: []providers.StreamChoice{
						{
							Index: 0,
							Delta: providers.Delta{
								Content: contentStr,
							},
						},
					},
				}
		case "tool_call":
			// Native tool call - mark phase and buffer for tool call detection only (do not stream to client)
			sawToolCallPhase = true
			contentBuffer.WriteString(contentStr)
			default: // "answer" or empty
				if contentStr != "" {
					contentBuffer.WriteString(contentStr)
					// Send immediately to prevent timeout
					streamChan <- providers.StreamResponse{
						ID:      msgID,
						Object:  "chat.completion.chunk",
						Created: created,
						Model:   req.Model,
						Choices: []providers.StreamChoice{
							{
								Index: 0,
								Delta: providers.Delta{
									Content: contentStr,
								},
							},
						},
					}
				}
			}
		}

	// Process buffered content at end of stream (EOF)
	fullText := contentBuffer.String()
	slog.Debug("Stream end processing", "sawToolCallPhase", sawToolCallPhase, "buffer_len", len(fullText))
		if sawToolCallPhase {
			toolCalls, err := parseToolCallsFromText(fullText, req.Tools)
			if err != nil {
				slog.Debug("Failed to parse tool calls in stream", "error", err, "text", fullText[:min(len(fullText), 300)])
			}
			if len(toolCalls) > 0 {
				streamChan <- providers.StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
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
				finishReason := "stop"
				streamChan <- providers.StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   req.Model,
					Choices: []providers.StreamChoice{
						{
							Index:        0,
							Delta:        providers.Delta{},
							FinishReason: &finishReason,
						},
					},
				}
			}
		} else {
			// No tool calls expected; just send finish (content already streamed)
			finishReason := "stop"
			streamChan <- providers.StreamResponse{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   req.Model,
				Choices: []providers.StreamChoice{
					{
						Index:        0,
						Delta:        providers.Delta{},
						FinishReason: &finishReason,
					},
				},
			}
		}
	}()

	return streamChan, nil
}


// convertRequest converts OpenAI request to Qwen format
func (c *Client) convertRequest(chatID string, req *providers.ChatRequest) []byte {
	// Map model name
	model := req.Model
	// Strip -nothinking suffix if present to get actual Qwen model name
	isNoThinking := strings.HasSuffix(model, "-nothinking")
	if isNoThinking {
		model = strings.TrimSuffix(model, "-nothinking")
	}
	// Qwen uses model names like qwen3.5-flash, qwen3.6-plus (hyphens kept)

	// Format messages to prompt string
	messages := req.Messages
	if len(req.Tools) > 0 {
		messages = injectToolPrompt(req.Messages, req.Tools, req.ToolChoice)
	}

	// Format messages into Qwen's expected format
	prompt := c.formatMessagesToPrompt(messages)

	// Build Qwen-specific payload (from qwen2api payload_builder.py)
	timestamp := time.Now().Unix()

	// Determine feature config: base values + low-latency override when tools are present
	featureConfig := map[string]interface{}{
		"thinking_enabled":       !isNoThinking,
		"output_schema":         "phase",
		"research_mode":         "normal",
		"auto_thinking":         !isNoThinking,
		"thinking_mode":         "Auto",
		"thinking_format":       "summary",
		"auto_search":           false,
		"code_interpreter":      false,
		"plugins_enabled":       false,
		"function_calling":     false, // Disable native function calling to avoid interception
		"enable_tools":         false, // Disable native tools
		"enable_function_call": false, // Disable native function calls
		"tool_choice":          "none", // Prevent upstream interception
	}

	// When custom tools are used, match Python's CUSTOM_TOOL_LOW_LATENCY_OVERRIDES:
	// disable thinking to reduce latency and avoid upstream content filters.
	if len(req.Tools) > 0 {
		featureConfig["thinking_enabled"] = false
		featureConfig["auto_thinking"] = false
	}

	payload := map[string]interface{}{
		"stream":            true,
		"version":           "2.1",
		"incremental_output": true,
		"chat_id":           chatID,
		"chat_mode":         "normal",
		"model":             model,
		"parent_id":         nil,
		"messages": []map[string]interface{}{
			{
				"fid":          fmt.Sprintf("%d", time.Now().UnixNano()),
				"parentId":     nil,
				"childrenIds":  []string{fmt.Sprintf("%d", time.Now().UnixNano()+1)},
				"role":         "user",
				"content":      prompt,
				"user_action":  "chat",
				"files":        []interface{}{},
				"timestamp":    timestamp,
				"models":       []string{model},
				"chat_type":    "t2t",
				"feature_config": featureConfig,
				"extra": map[string]interface{}{
					"meta": map[string]interface{}{
						"subChatType": "t2t",
					},
				},
				"sub_chat_type": "t2t",
				"parent_id":     nil,
			},
		},
		"timestamp": timestamp,
	}

	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}

	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}

	jsonPayload, _ := json.Marshal(payload)
	return jsonPayload
}

// formatMessagesToPrompt converts OpenAI messages to Qwen prompt string
func (c *Client) formatMessagesToPrompt(messages []providers.Message) string {
	var promptParts []string
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			promptParts = append(promptParts, "System: "+string(msg.Content))
		case "user":
			promptParts = append(promptParts, "Human: "+string(msg.Content))
		case "assistant":
			// Handle assistant messages with or without tool calls
			content := string(msg.Content)
			if len(msg.ToolCalls) > 0 {
				// Format tool calls for context (obfuscate tool names)
				var toolCallParts []string
				for _, tc := range msg.ToolCalls {
					toolCallParts = append(toolCallParts, fmt.Sprintf(
						"##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##",
						toQwenName(tc.Function.Name),
						tc.Function.Arguments,
					))
				}
				if content != "" {
					promptParts = append(promptParts, "Assistant: "+content+"\n"+strings.Join(toolCallParts, "\n"))
				} else {
					promptParts = append(promptParts, "Assistant: "+strings.Join(toolCallParts, "\n"))
				}
			} else {
				promptParts = append(promptParts, "Assistant: "+content)
			}
		case "tool":
			// Tool results
			promptParts = append(promptParts, "Tool Result: "+string(msg.Content))
		}
	}
	return strings.Join(promptParts, "\n\n")
}

func generateMessageID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}

func stringPtr(s string) *string {
	return &s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// createChatSession creates a new chat session
func (c *Client) createChatSession() (string, error) {
	// Create chat session via Qwen API
	ts := time.Now().Unix()
	body := map[string]interface{}{
		"title":    fmt.Sprintf("api_%d", ts),
		"models":   []string{},
		"chat_mode": "normal",
		"chat_type": "t2t",
		"timestamp": ts,
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", c.config.BaseURL+createChatURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", c.config.BaseURL+"/")
	req.Header.Set("Origin", c.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to create chat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create chat failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Success bool `json:"success"`
		Data    struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success || result.Data.ID == "" {
		return "", fmt.Errorf("Qwen API returned error or missing id")
	}

	return result.Data.ID, nil
}
