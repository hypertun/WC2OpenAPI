package providers

import (
	"context"
	"fmt"
	"log/slog"
)

// EstimatorFunc is a provider-specific function that estimates the total query size
// for a set of messages and tools, accounting for the provider's message formatting.
type EstimatorFunc func(messages []Message, tools []Tool) int

// SendFunc is a provider-specific function that sends a single chat completion
// request directly, bypassing any size-check or split logic. The retryCount
// parameter supports providers that retry on tool-call validation errors.
// This type is used by SplitAndSend to avoid infinite recursion: the caller's
// size-check guard calls SplitAndSend, which calls SendFunc, which does NOT re-check size.
type SendFunc func(ctx context.Context, req *ChatRequest, retryCount int) (*ChatResponse, error)

// SplitAndSend splits an oversized chat request into multiple parts, sends each part
// (except the final one) with an acknowledgment prompt and no tools, then sends the final
// part with tools enabled and returns that response. Intermediate responses are logged
// and discarded.
//
// Parameters:
//   - ctx: context for cancellation
//   - send: a function that sends a single chunk directly (no size re-check)
//   - req: the original chat request
//   - maxChars: the maximum character limit for the query (e.g., from provider config)
//   - estimatorFn: provider-specific function to estimate query size (e.g., MiMo's EstimateQuerySize)
//
// Returns:
//   - *ChatResponse: the final response from the last chunk
//   - error: if splitting fails or any chunk request fails
func SplitAndSend(
	ctx context.Context,
	send SendFunc,
	req *ChatRequest,
	maxChars int,
	estimatorFn EstimatorFunc,
) (*ChatResponse, error) {
	toolOverhead := EstimateToolPromptSize(req.Tools)
	effectiveMaxChars := maxChars - toolOverhead
	if effectiveMaxChars <= 0 {
		effectiveMaxChars = 1000 // fallback minimum
	}

	// Split messages into chunks
	chunks := SplitMessages(req.Messages, effectiveMaxChars)
	if len(chunks) <= 1 {
		return nil, fmt.Errorf("query too long and cannot be split further (max_chars: %d, tool_overhead: %d)", maxChars, toolOverhead)
	}

	slog.Info("Splitting long message",
		"chunks", len(chunks),
		"model", req.Model,
		"max_chars", maxChars,
		"effective_max_chars", effectiveMaxChars,
	)

	var totalUsage Usage

	// Send intermediate chunks (1..N-1) without tools, discard responses
	for i := 0; i < len(chunks)-1; i++ {
		chunkNum := i + 1
		totalChunks := len(chunks)

		slog.Debug("Sending intermediate chunk",
			"chunk", chunkNum,
			"of", totalChunks,
		)

		// Append acknowledgment message
		ackMsg := Message{
			Role:    "user",
			Content: MessageContent(fmt.Sprintf("[PART %d OF %d] This is part of a long conversation. Please acknowledge with 'OK, continue.' and wait for the next part.", chunkNum, totalChunks)),
		}
		chunkMessages := append(chunks[i], ackMsg)

		chunkReq := &ChatRequest{
			Model:      req.Model,
			Messages:   chunkMessages,
			Temperature: req.Temperature,
			MaxTokens:  req.MaxTokens,
			// No tools on intermediate chunks
			Tools:      nil,
			ToolChoice: req.ToolChoice,
			ChatID:     req.ChatID,
		}

		resp, err := send(ctx, chunkReq, 0)
		if err != nil {
			return nil, fmt.Errorf("split chunk %d failed: %w", chunkNum, err)
		}

		// Log intermediate response (discard it)
		if len(resp.Choices) > 0 {
			chunkContent := string(resp.Choices[0].Message.Content)
			slog.Debug("Intermediate chunk response",
				"chunk", chunkNum,
				"response_preview", truncatePreview(chunkContent, 100),
			)
		}

		// Accumulate usage
		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
	}

	// Send final chunk (N) with tools enabled
	finalChunkNum := len(chunks)
	slog.Debug("Sending final chunk",
		"chunk", finalChunkNum,
		"of", len(chunks),
	)

	finalAckMsg := Message{
		Role:    "user",
		Content: MessageContent(fmt.Sprintf("[PART %d OF %d — FINAL] You have now received all parts. Please respond to the original request.", finalChunkNum, len(chunks))),
	}
	finalMessages := append(chunks[len(chunks)-1], finalAckMsg)

	finalReq := &ChatRequest{
		Model:      req.Model,
		Messages:   finalMessages,
		Temperature: req.Temperature,
		MaxTokens:  req.MaxTokens,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
		ChatID:     req.ChatID,
	}

	finalResp, err := send(ctx, finalReq, 0)
	if err != nil {
		return nil, fmt.Errorf("split final chunk failed: %w", err)
	}

	// Accumulate usage from final chunk
	totalUsage.PromptTokens += finalResp.Usage.PromptTokens
	totalUsage.CompletionTokens += finalResp.Usage.CompletionTokens
	totalUsage.TotalTokens += finalResp.Usage.TotalTokens

	// Update final response with accumulated usage
	finalResp.Usage = totalUsage

	slog.Info("Split message complete",
		"chunks", len(chunks),
		"model", req.Model,
	)

	return finalResp, nil
}

// truncatePreview returns a truncated preview of text for logging
func truncatePreview(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
