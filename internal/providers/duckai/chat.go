package duckai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

const maxRetries = toolcall.DefaultMaxRetries

func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return c.chatWithRetry(ctx, req, 0)
}

func (c *Client) chatWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	start := time.Now()

	c.rateLimiter.WaitIfNeeded()

	vqdHash, err := c.getVQD(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get VQD: %w", err)
	}

	hasTools := len(req.Tools) > 0 && req.ToolChoice != "none"

	var bodyText string
	{
		chatReq := buildChatRequest(req, hasTools)

		resp, err := c.doChatRequest(ctx, vqdHash, chatReq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("rate limited by DuckDuckGo")
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("chat request failed with %d: %s", resp.StatusCode, string(body))
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read chat response: %w", err)
		}
		bodyText = string(bodyBytes)
	}

	content, toolCalls := parseSSE(bodyText, hasTools)

	id := fmt.Sprintf("chatcmpl-%d", start.UnixNano())
	created := start.Unix()

	if len(toolCalls) > 0 {
		validationErrors := toolcall.ValidateToolCallsWithErrors(toolCalls, req.Tools)
		if toolcall.ShouldRetry(validationErrors, retryCount, maxRetries) {
			feedback := toolcall.GenerateToolCallErrorFeedback(validationErrors)
			backoff := toolcall.CalculateBackoff(retryCount)

			slog.Info("Retrying chat with tool call feedback",
				"retry", retryCount+1,
				"max", maxRetries,
				"errors", len(validationErrors),
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}

			retryReq := toolcall.BuildRetryRequest(req, feedback)
			return c.chatWithRetry(ctx, retryReq, retryCount+1)
		}

		return &providers.ChatResponse{
			ID:      id,
			Object:  "chat.completion",
			Created: created,
			Model:   req.Model,
			Choices: []providers.Choice{
				{
					Index:        0,
					Message:      providers.Message{Role: "assistant", Content: "", ToolCalls: toolCalls},
					FinishReason: "tool_calls",
				},
			},
			Usage: estimateUsage(req.Messages, content),
		}, nil
	}

	msgContent := providers.MessageContent(content)
	return &providers.ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   req.Model,
		Choices: []providers.Choice{
			{
				Index:        0,
				Message:      providers.Message{Role: "assistant", Content: msgContent},
				FinishReason: "stop",
			},
		},
		Usage: estimateUsage(req.Messages, content),
	}, nil
}

func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	ch := make(chan providers.StreamResponse)

	hasTools := len(req.Tools) > 0 && req.ToolChoice != "none"

	if hasTools {
		go c.streamWithTools(ctx, req, ch)
	} else {
		go c.streamDirect(ctx, req, ch)
	}

	return ch, nil
}

func (c *Client) streamWithTools(ctx context.Context, req *providers.ChatRequest, ch chan<- providers.StreamResponse) {
	defer close(ch)

	completion, err := c.CreateChatCompletion(ctx, req)
	if err != nil {
		slog.Error("Stream with tools failed", "error", err)
		return
	}

	id := completion.ID
	created := completion.Created

	choice := completion.Choices[0]

	if choice.Message.ToolCalls != nil {
		ch <- providers.StreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{
				{
					Index: 0,
					Delta: providers.Delta{
						Role:      "assistant",
						ToolCalls: choice.Message.ToolCalls,
					},
				},
			},
		}

		finish := "tool_calls"
		ch <- providers.StreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{
				{
					Index:        0,
					Delta:        providers.Delta{},
					FinishReason: &finish,
				},
			},
		}
	} else {
		content := string(choice.Message.Content)

		ch <- providers.StreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{
				{
					Index: 0,
					Delta: providers.Delta{Role: "assistant"},
				},
			},
		}

		chunkSize := 10
		for i := 0; i < len(content); i += chunkSize {
			end := i + chunkSize
			if end > len(content) {
				end = len(content)
			}
			ch <- providers.StreamResponse{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   req.Model,
				Choices: []providers.StreamChoice{
					{
						Index: 0,
						Delta: providers.Delta{Content: content[i:end]},
					},
				},
			}
		}

		finish := "stop"
		ch <- providers.StreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{
				{
					Index:        0,
					Delta:        providers.Delta{},
					FinishReason: &finish,
				},
			},
		}
	}
}

func (c *Client) streamDirect(ctx context.Context, req *providers.ChatRequest, ch chan<- providers.StreamResponse) {
	defer close(ch)

	start := time.Now()

	c.rateLimiter.WaitIfNeeded()

	vqdHash, err := c.getVQD(ctx)
	if err != nil {
		slog.Error("Stream VQD failed", "error", err)
		return
	}

	chatReq := buildChatRequest(req, false)

	resp, err := c.doChatRequest(ctx, vqdHash, chatReq)
	if err != nil {
		slog.Error("Stream request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("Stream request bad status", "status", resp.StatusCode, "body", string(body))
		return
	}

	id := fmt.Sprintf("chatcmpl-%d", start.UnixNano())
	created := start.Unix()

	first := true
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var sseMsg struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &sseMsg); err != nil {
			continue
		}

		delta := providers.Delta{Content: sseMsg.Message}
		if first {
			delta.Role = "assistant"
			first = false
		}

		ch <- providers.StreamResponse{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{
				{
					Index: 0,
					Delta: delta,
				},
			},
		}
	}

	finish := "stop"
	ch <- providers.StreamResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   req.Model,
		Choices: []providers.StreamChoice{
			{
				Index:        0,
				Delta:        providers.Delta{},
				FinishReason: &finish,
			},
		},
	}
}

func buildChatRequest(req *providers.ChatRequest, hasTools bool) map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(req.Messages)+1)

	if hasTools {
		instructions := buildToolInstructions(req.Tools, req.ToolChoice)
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": "[SYSTEM INSTRUCTIONS] " + instructions + "\n\nPlease follow these instructions when responding.",
		})
	}

	for _, msg := range req.Messages {
		m := map[string]interface{}{
			"role":    msg.Role,
			"content": string(msg.Content),
		}
		if msg.ToolCallID != "" {
			m["tool_call_id"] = msg.ToolCallID
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			m["content"] = wrapToolCalls(msg.ToolCalls)
		}
		messages = append(messages, m)
	}

	body := map[string]interface{}{
		"model":    req.Model,
		"messages": messages,
	}

	return body
}

func (c *Client) doChatRequest(ctx context.Context, vqdHash string, body map[string]interface{}) (*http.Response, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.String()+chatURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat request: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("x-vqd-hash-1", vqdHash)
	req.Header.Set("accept", "text/event-stream")

	slog.Debug("Sending chat request",
		"vqd_hash_len", len(vqdHash),
		"vqd_hash_preview", vqdHash[:min(50, len(vqdHash))],
		"user_agent", req.Header.Get("User-Agent"))

	return c.httpClient.Do(req)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseSSE(body string, checkTools bool) (string, []providers.ToolCall) {
	var content strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var sseMsg struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &sseMsg); err != nil {
			continue
		}
		content.WriteString(sseMsg.Message)
	}

	result := strings.TrimSpace(content.String())

	if !checkTools || result == "" {
		return result, nil
	}

	if detectToolCalls(result) {
		toolCalls := extractToolCalls(result)
		if len(toolCalls) > 0 {
			return "", toolCalls
		}
	}

	return result, nil
}

func estimateUsage(messages []providers.Message, content string) providers.Usage {
	promptTokens := 0
	for _, m := range messages {
		promptTokens += len(string(m.Content)) / 4
	}
	completionTokens := len(content) / 4
	if completionTokens < 1 {
		completionTokens = 1
	}
	return providers.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}
