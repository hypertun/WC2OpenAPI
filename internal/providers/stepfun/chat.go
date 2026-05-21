package stepfun

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

const defaultMaxQueryChars = 15000

// maxQueryChars returns the configured max query character limit, or the default.
func (c *Client) maxQueryChars() int {
	if c.config.MaxQueryChars > 0 {
		return c.config.MaxQueryChars
	}
	return defaultMaxQueryChars
}

// CreateChatCompletion creates a non-streaming chat completion.
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return c.chatWithRetry(ctx, req, 0)
}

// chatWithRetry performs non-streaming chat with automatic message splitting when needed.
// If the query size exceeds the limit, it splits messages and uses SplitAndSend.
// Otherwise, it delegates to sendDirect.
func (c *Client) chatWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	// Estimate query size before tool injection (tools will be added by sendDirect)
	toolPromptSize := providers.EstimateToolPromptSize(req.Tools)
	querySize := providers.EstimateQuerySize(req.Messages) + toolPromptSize
	maxChars := c.maxQueryChars()

	if querySize > maxChars {
		slog.Info("StepFun: proactively splitting long message",
			"query_size", querySize,
			"max_chars", maxChars,
		)
		return providers.SplitAndSend(ctx, c.sendDirect, req, maxChars, providers.StandardEstimate)
	}

	return c.sendDirect(ctx, req, retryCount)
}

// sendDirect performs a single chat completion without retry guards.
// It handles tool injection, parsing, validation, and optional retry.
func (c *Client) sendDirect(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	// Inject tools into messages before formatting
	if len(req.Tools) > 0 {
		req.Messages = c.toolEngine.InjectTools(req.Messages, req.Tools, req.ToolChoice)
	}

	query := buildQuery(req)
	slog.Info("stepfun: sendDirect called", "model", req.Model, "query_len", len(query), "retry", retryCount)

	eventChan, err := c.chatStream(ctx, query, req.Model, true)
	if err != nil {
		slog.Error("stepfun: chatStream error", "error", err)
		return nil, fmt.Errorf("stepfun: chat: %w", err)
	}

	var contentBuf, reasoningBuf strings.Builder
	eventCount := 0

	for event := range eventChan {
		eventCount++
		if event.Data == nil || event.Data.Event == nil {
			continue
		}
		e := event.Data.Event

		switch {
		case e.ReasoningEvent != nil:
			var p reasoningPayload
			if err := json.Unmarshal(*e.ReasoningEvent, &p); err == nil {
				reasoningBuf.WriteString(p.Text)
			}
		case e.TextEvent != nil:
			var p textPayload
			if err := json.Unmarshal(*e.TextEvent, &p); err == nil {
				contentBuf.WriteString(p.Text)
			}
		case e.MessageEvent != nil:
			// Full message snapshot — extract final content if available
			var msgEvent struct {
				Message *struct {
					Content *struct {
						AssistantMessage *struct {
							QA *struct {
								Answer string `json:"answer"`
							} `json:"qa"`
						} `json:"assistantMessage"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal(*e.MessageEvent, &msgEvent); err == nil {
				if msgEvent.Message != nil && msgEvent.Message.Content != nil &&
					msgEvent.Message.Content.AssistantMessage != nil &&
					msgEvent.Message.Content.AssistantMessage.QA != nil &&
					msgEvent.Message.Content.AssistantMessage.QA.Answer != "" {
					// Use the full answer from messageEvent as the authoritative final content
					_ = contentBuf // streaming textEvent already captured the content
				}
			}
		}
	}

	content := contentBuf.String()
	reasoning := reasoningBuf.String()

	slog.Debug("stepfun: sendDirect finished", "event_count", eventCount, "content_len", len(content), "reasoning_len", len(reasoning))

	// Fallback: if streaming textEvent didn't capture content, try messageEvent
	if content == "" {
		// We already consumed the channel; content must come from streaming events
		slog.Warn("StepFun: no content captured from streaming events")
	}

	// Extract tool calls if tools were provided
	var toolCalls []providers.ToolCall
	if len(req.Tools) > 0 {
		tc, cleaned, _ := c.toolEngine.Parse(content, req.Tools)
		if len(tc) > 0 {
			toolCalls = tc
			content = cleaned
		} else if reasoning != "" {
			// Fallback: tool call markup may have come through the reasoning stream
			tc, cleaned, _ = c.toolEngine.Parse(reasoning, req.Tools)
			if len(tc) > 0 {
				toolCalls = tc
				reasoning = cleaned
			}
		}

		// Validate tool calls and retry if needed
		validationErrors := c.toolEngine.Validate(toolCalls, req.Tools)
		if c.toolEngine.ShouldRetry(validationErrors, retryCount) {
			for _, ve := range validationErrors {
				slog.Debug("StepFun: validation error detail",
					"tool", ve.ToolName,
					"param", ve.Parameter,
					"severity", ve.Severity,
					"message", ve.Message,
					"expected", ve.Expected,
					"actual", fmt.Sprintf("%v", ve.Actual),
				)
			}
			feedback := c.toolEngine.GenerateErrorFeedback(validationErrors)
			backoff := c.toolEngine.CalculateBackoff(retryCount)

			slog.Info("StepFun: retrying chat completion with error feedback",
				"retry", retryCount+1,
				"errors", len(validationErrors),
				"backoff_ms", backoff.Milliseconds(),
			)

			time.Sleep(backoff)
			retryReq := c.toolEngine.BuildRetryRequest(req, feedback)
			return c.sendDirect(ctx, retryReq, retryCount+1)
		}
	}

	created := time.Now().Unix()
	resp := &providers.ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: created,
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
		Usage: providers.Usage{},
	}

	if len(toolCalls) > 0 {
		resp.Choices[0].Message.ToolCalls = toolCalls
		resp.Choices[0].FinishReason = "tool_calls"
	} else {
		reasoningContent := reasoning
		finalContent := content
		if reasoningContent != "" {
			// Include reasoning in the message as ReasoningContent
			resp.Choices[0].Message.ReasoningContent = reasoningContent
		}
		resp.Choices[0].Message.Content = providers.MessageContent(finalContent)
	}

	return resp, nil
}

// CreateChatCompletionStream creates a streaming chat completion.
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	return c.streamWithRetry(ctx, req, 0)
}

// streamWithRetry sends a streaming chat completion with tool call support and automatic retry.
// If the query size exceeds the limit, converts a split non-streaming response to a stream.
func (c *Client) streamWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (<-chan providers.StreamResponse, error) {
	// Pre-check: if message size exceeds limit, split proactively and convert to stream
	toolPromptSize := providers.EstimateToolPromptSize(req.Tools)
	querySize := providers.EstimateQuerySize(req.Messages) + toolPromptSize
	maxChars := c.maxQueryChars()

	if querySize > maxChars {
		slog.Info("StepFun: proactively splitting long message (streaming)",
			"query_size", querySize,
			"max_chars", maxChars,
		)
		// Convert non-streaming split response to stream
		return convertToStream(ctx, c, req)
	}

	// Inject tools into messages before formatting
	if len(req.Tools) > 0 {
		req.Messages = c.toolEngine.InjectTools(req.Messages, req.Tools, req.ToolChoice)
	}

	query := buildQuery(req)

	eventChan, err := c.chatStream(ctx, query, req.Model, true)
	if err != nil {
		return nil, fmt.Errorf("stepfun: stream chat: %w", err)
	}

	outChan := make(chan providers.StreamResponse, 50)

	go func() {
		defer close(outChan)

		msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()

		// Create adapter for real-time streaming with tool call support.
		adapter := toolcall.NewStreamSieveAdapter(c.toolEngine, req.Tools, msgID, req.Model, created)
		if len(req.Tools) > 0 {
			adapter.WithRetry(ctx, func(ctx2 context.Context, req2 *providers.ChatRequest, rc int) (<-chan providers.StreamResponse, error) {
				return c.streamWithRetry(ctx2, req2, rc)
			}, req, retryCount)
		}

		// Emit initial role chunk
		outChan <- providers.StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   req.Model,
			Choices: []providers.StreamChoice{{
				Index: 0,
				Delta: providers.Delta{Role: "assistant"},
			}},
		}

		for event := range eventChan {
			if event.Data == nil || event.Data.Event == nil {
				continue
			}
			e := event.Data.Event

			switch {
			case e.ReasoningEvent != nil:
				var p reasoningPayload
				if err := json.Unmarshal(*e.ReasoningEvent, &p); err == nil && p.Text != "" {
					for _, chunk := range adapter.FeedThinking(p.Text) {
						outChan <- chunk
					}
				}

			case e.TextEvent != nil:
				var p textPayload
				if err := json.Unmarshal(*e.TextEvent, &p); err == nil && p.Text != "" {
					for _, chunk := range adapter.FeedText(p.Text) {
						outChan <- chunk
					}
				}

			case e.DoneEvent != nil:
				// Stream finished
			}
		}

		// Flush adapter: handles parsing, validation, optional retry, and final chunks.
		for _, chunk := range adapter.Flush() {
			outChan <- chunk
		}
	}()

	return outChan, nil
}

// buildQuery constructs a query string from the OpenAI chat request messages.
// This delegates to the shared BuildFlatQuery function.
func buildQuery(req *providers.ChatRequest) string {
	return providers.BuildFlatQuery(req.Messages)
}



// convertToStream converts a non-streaming split response to a streaming response channel.
// Used when the query is too long and must be split for non-streaming, then converted to stream.
func convertToStream(ctx context.Context, c *Client, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	outChan := make(chan providers.StreamResponse, 10)

	go func() {
		defer close(outChan)

		resp, err := providers.SplitAndSend(ctx, c.sendDirect, req, c.maxQueryChars(), providers.StandardEstimate)
		if err != nil {
			slog.Error("StepFun: split failed", "error", err)
			return
		}

		// Delegate to the shared stream conversion function
		for chunk := range providers.EmitCompletionAsStream(ctx, resp, req.Model) {
			outChan <- chunk
		}
	}()

	return outChan, nil
}
