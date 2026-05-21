package mimo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

const defaultMaxQueryChars = 12000

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// CreateChatCompletion creates a non-streaming chat completion.
// Buffers all SSE events and returns a single response.
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return c.chatWithRetry(ctx, req, 0)
}

// maxQueryChars returns the configured max query character limit, or the default.
func (c *Client) maxQueryChars() int {
	if c.config.MaxQueryChars > 0 {
		return c.config.MaxQueryChars
	}
	return defaultMaxQueryChars
}

// chatWithRetry sends a chat completion request with automatic retry on
// tool-call validation errors and transparent splitting when the query is too long.
func (c *Client) chatWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	// Pre-check: if message size exceeds limit, split proactively
	toolPromptSize := providers.EstimateToolPromptSize(req.Tools)
	querySize := providers.EstimateQuerySize(req.Messages) + toolPromptSize
	if querySize > c.maxQueryChars() {
		slog.Info("MiMo: proactively splitting long message",
			"query_size", querySize,
			"max_chars", c.maxQueryChars(),
		)
		return providers.SplitAndSend(ctx, c.sendDirect, req, c.maxQueryChars(), providers.StandardEstimate)
	}

	return c.sendDirect(ctx, req, retryCount)
}

// sendDirect performs a single chat completion without the size-check guard.
// This is the raw send path used by SplitAndSend to avoid infinite recursion:
// chatWithRetry → SplitAndSend → sendDirect (stops here, no re-check).
// It still handles tool-call validation errors with retry.
func (c *Client) sendDirect(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	// Inject tools into messages before formatting
	if len(req.Tools) > 0 {
		req.Messages = c.toolEngine.InjectTools(req.Messages, req.Tools, req.ToolChoice)
	}

	query, thinking := buildQuery(req)
	model := req.Model

	chatReq := c.buildChatRequest(query, thinking, model)
	eventChan, err := c.streamChat(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("mimo: chat: %w", err)
	}

	var contentBuf strings.Builder
	var usage providers.Usage

	for event := range eventChan {
		switch event.Type {
		case "text":
			contentBuf.WriteString(event.Content)
		default:
			if event.PromptTokens > 0 || event.CompletionTokens > 0 {
				usage = providers.Usage{
					PromptTokens:     event.PromptTokens,
					CompletionTokens: event.CompletionTokens,
					TotalTokens:      event.TotalTokens,
				}
				if usage.TotalTokens == 0 {
					usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				}
			}
		}
	}

	fullContent := contentBuf.String()
	fullContent = strings.ReplaceAll(fullContent, "\x00", "")

	// Detect "too long" error as a fallback (shouldn't happen with proactive check, but safety net)
	if providers.IsTooLongError(fullContent) {
		slog.Warn("MiMo: received 'too long' error despite proactive splitting check")
		return nil, fmt.Errorf("mimo: query too long and cannot be split further")
	}

	content, thinkingContent := splitThinkTags(fullContent)

	// Extract tool calls if tools were provided
	var toolCalls []providers.ToolCall
	if len(req.Tools) > 0 {
		// Parse fullContent first (includes <think> blocks) to catch tool calls in thinking
		tc, cleaned, _ := c.toolEngine.Parse(fullContent, req.Tools)
		if len(tc) > 0 {
			toolCalls = tc
			// Re-derive display text by re-splitting the cleaned output
			content, thinkingContent = splitThinkTags(cleaned)
		}

		// Validate tool calls and retry if needed
		validationErrors := c.toolEngine.Validate(toolCalls, req.Tools)
		if c.toolEngine.ShouldRetry(validationErrors, retryCount) {
			for _, ve := range validationErrors {
				slog.Debug("MiMo: validation error detail",
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

			slog.Info("MiMo: retrying chat completion with error feedback",
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
		Model:   model,
		Choices: []providers.Choice{
			{
				Index: 0,
				Message: providers.Message{
					Role: "assistant",
				},
				FinishReason: "stop",
			},
		},
		Usage: usage,
	}

	if len(toolCalls) > 0 {
		resp.Choices[0].Message.ToolCalls = toolCalls
		resp.Choices[0].FinishReason = "tool_calls"
	} else {
		combined := content
		if thinkingContent != "" {
			combined = thinkOpen + thinkingContent + thinkClose + "\n" + content
		}
		resp.Choices[0].Message.Content = providers.MessageContent(combined)
		resp.Choices[0].Message.ReasoningContent = thinkingContent
	}

	return resp, nil
}

// CreateChatCompletionStream creates a streaming chat completion.
// Pipes SSE events from MiMo through to the OpenAI streaming format.
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	return c.streamWithRetry(ctx, req, 0)
}

func (c *Client) streamWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (<-chan providers.StreamResponse, error) {
	// Pre-check: if message size exceeds limit, split proactively and convert to stream
	toolPromptSize := providers.EstimateToolPromptSize(req.Tools)
	querySize := providers.EstimateQuerySize(req.Messages) + toolPromptSize
	if querySize > c.maxQueryChars() {
		slog.Info("MiMo: proactively splitting long message (streaming)",
			"query_size", querySize,
			"max_chars", c.maxQueryChars(),
		)
		// Convert non-streaming split response to stream
		return convertToStream(ctx, c, req)
	}

	// Inject tools into messages before formatting
	if len(req.Tools) > 0 {
		req.Messages = c.toolEngine.InjectTools(req.Messages, req.Tools, req.ToolChoice)
	}

	query, thinking := buildQuery(req)
	model := req.Model

	chatReq := c.buildChatRequest(query, thinking, model)
	eventChan, err := c.streamChat(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("mimo: stream chat: %w", err)
	}

	outChan := make(chan providers.StreamResponse, 50)

	go func() {
		defer close(outChan)

		msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()

		// Create adapter for real-time streaming.
		adapter := toolcall.NewStreamSieveAdapter(c.toolEngine, req.Tools, msgID, req.Model, created)
		if len(req.Tools) > 0 {
			adapter.WithRetry(ctx, func(ctx2 context.Context, req2 *providers.ChatRequest, rc int) (<-chan providers.StreamResponse, error) {
				return c.streamWithRetry(ctx2, req2, rc)
			}, req, retryCount)
		}

		// Think-tag state machine for MiMo's <think>...</think> blocks.
		inThink := false
		var thinkBuf strings.Builder

		feedChunk := func(chunk string) {
			if chunk == "" {
				return
			}
			for {
				if inThink {
					idx := strings.Index(chunk, thinkClose)
					if idx >= 0 {
						thinkContent := chunk[:idx]
						if strings.TrimSpace(thinkContent) != "" {
							for _, c := range adapter.FeedThinking(thinkContent) {
								outChan <- c
							}
						}
						chunk = chunk[idx+len(thinkClose):]
						inThink = false
						continue
					}
					// No closing tag yet — buffer the think content.
					thinkBuf.WriteString(chunk)
					return
				}

				idx := strings.Index(chunk, thinkOpen)
				if idx >= 0 {
					// Emit content before think tag.
					before := chunk[:idx]
					if strings.TrimSpace(before) != "" {
						for _, c := range adapter.FeedText(before) {
							outChan <- c
						}
					}
					chunk = chunk[idx+len(thinkOpen):]
					inThink = true
					continue
				}

				// No think tags — emit remaining as content.
				if strings.TrimSpace(chunk) != "" {
					for _, c := range adapter.FeedText(chunk) {
						outChan <- c
					}
				}
				return
			}
		}

		for event := range eventChan {
			switch event.Type {
			case "text":
				chunk := strings.ReplaceAll(event.Content, "\x00", "")
				feedChunk(chunk)
			default:
				// usage events are ignored for streaming
			}
		}

		// If we were mid-think tag, flush the buffered thinking content.
		if inThink && thinkBuf.Len() > 0 {
			for _, c := range adapter.FeedThinking(thinkBuf.String()) {
				outChan <- c
			}
		}

		// Flush adapter: handles parsing, validation, optional retry, and final chunks.
		for _, chunk := range adapter.Flush() {
			outChan <- chunk
		}
	}()

	return outChan, nil
}

// buildQuery constructs the MiMo query string from the chat request.
// Messages are concatenated with role prefixes (system:/user:/assistant:/tool:).
// This delegates to the shared BuildFlatQuery function.
func buildQuery(req *providers.ChatRequest) (query string, thinking bool) {
	return providers.BuildFlatQuery(req.Messages), false
}

// splitThinkTags extracts <think>...</think> blocks from the response.
func splitThinkTags(text string) (content, thinkingContent string) {
	start := strings.Index(text, thinkOpen)
	if start == -1 {
		return text, ""
	}

	end := strings.Index(text, thinkClose)
	if end == -1 {
		return text, ""
	}

	thinkingContent = text[start+len(thinkOpen) : end]
	content = text[:start] + text[end+len(thinkClose):]
	return content, thinkingContent
}



// convertToStream converts a non-streaming split response to a streaming response channel.
func convertToStream(ctx context.Context, c *Client, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	outChan := make(chan providers.StreamResponse, 10)

	go func() {
		defer close(outChan)

		resp, err := providers.SplitAndSend(ctx, c.sendDirect, req, c.maxQueryChars(), providers.StandardEstimate)
		if err != nil {
			slog.Error("MiMo: split failed", "error", err)
			return
		}

		// Delegate to the shared stream conversion function
		for chunk := range providers.EmitCompletionAsStream(ctx, resp, req.Model) {
			outChan <- chunk
		}
	}()

	return outChan, nil
}
