package toolcall

import (
	"context"
	"log/slog"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

// RetryFunc restarts a streaming chat with an incremented retry count.
type RetryFunc func(ctx context.Context, req *providers.ChatRequest, retryCount int) (<-chan providers.StreamResponse, error)

// StreamSieveAdapter provides real-time text streaming while transparently
// separating tool call markers via the StreamSieve. Text flows to the caller
// immediately; tool call markers are captured silently and re-parsed,
// validated, and optionally retried at Flush().
//
// When no tools are present, FeedText/FeedThinking are no-ops and Flush
// simply returns a finish-reason chunk.
type StreamSieveAdapter struct {
	engine     *ToolCallEngine
	tools      []providers.Tool
	toolNames  []string
	msgID      string
	model      string
	created    int64
	hasTools   bool

	// Buffered full content for final parsing.
	contentBuf strings.Builder
	sentRole   bool

	// StreamSieve for real-time text vs. tool-call separation.
	sieve *StreamSieve

	// Retry support.
	ctx         context.Context
	retryFn     RetryFunc
	originalReq *providers.ChatRequest
	retryCount  int

	// ValidateExtra is an optional hook for provider-specific validation
	// (e.g. Qwen's Agent subagent_type check). Called during Flush after
	// standard schema validation; its errors are merged for retry decisions.
	ValidateExtra func(calls []providers.ToolCall) []*ValidationError
}

// NewStreamSieveAdapter creates an adapter. When len(tools) > 0 the adapter
// uses a StreamSieve to separate text from tool-call markers in real-time.
func NewStreamSieveAdapter(
	engine *ToolCallEngine,
	tools []providers.Tool,
	msgID, model string,
	created int64,
) *StreamSieveAdapter {
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" && t.Function.Name != "" {
			toolNames = append(toolNames, t.Function.Name)
		}
	}
	hasTools := len(tools) > 0

	a := &StreamSieveAdapter{
		engine:    engine,
		tools:     tools,
		toolNames: toolNames,
		msgID:     msgID,
		model:     model,
		created:   created,
		hasTools:  hasTools,
	}

	if hasTools {
		parseFn := func(text string, toolNames []string) ([]SieveToolCall, string) {
			return ParseAllToolCalls(text, toolNames)
		}
		a.sieve = NewStreamSieve(parseFn)
	}

	return a
}

// WithRetry configures retry behaviour. Must be called before any Feed* calls.
func (a *StreamSieveAdapter) WithRetry(
	ctx context.Context,
	retryFn RetryFunc,
	originalReq *providers.ChatRequest,
	retryCount int,
) *StreamSieveAdapter {
	a.ctx = ctx
	a.retryFn = retryFn
	a.originalReq = originalReq
	a.retryCount = retryCount
	return a
}

// FeedText feeds a content text delta. Text is always streamed immediately.
// When tools are present the StreamSieve further separates confirmed-safe text
// from tool-call markers (which are held for Flush). Returns zero or more
// StreamResponse chunks to emit.
func (a *StreamSieveAdapter) FeedText(chunk string) []providers.StreamResponse {
	if chunk == "" {
		return nil
	}
	a.contentBuf.WriteString(chunk)

	if !a.hasTools {
		// No tools — stream text directly.
		return a.emitText(chunk)
	}

	// Use sieve to separate safe text from tool-call markers in real-time.
	events := a.sieve.Feed(chunk, a.toolNames)
	var out []providers.StreamResponse
	for _, evt := range events {
		if evt.Type == "text" {
			if text, ok := evt.Data.(string); ok && text != "" {
				out = append(out, a.emitText(text)...)
			}
		}
		// tool_calls events are silently re-parsed from the full content
		// during Flush, where validation and retry happen.
	}
	return out
}

// FeedThinking feeds a thinking/reasoning text delta. Thinking is always
// streamed immediately, even when tools are present. Returns zero or more
// StreamResponse chunks to emit.
//
// When tools are present, thinking is also appended to the content buffer so
// that the final tool-call parse may inspect it (some providers like Qwen
// send tool call markers inside thinking-phase events).
func (a *StreamSieveAdapter) FeedThinking(chunk string) []providers.StreamResponse {
	if chunk == "" {
		return nil
	}
	if a.hasTools {
		a.contentBuf.WriteString(chunk)
	}
	return a.emitThinking(chunk)
}

// Flush finalizes the stream: parses the full content for tool calls,
// validates them, optionally retries, and emits the concluding chunks
// (tool-call deltas or finish-reason).
//
// Must be called exactly once when the upstream SSE stream ends.
func (a *StreamSieveAdapter) Flush() []providers.StreamResponse {
	if !a.hasTools {
		return a.finishChunk("stop")
	}

	// Release any text held by the sieve (e.g., partial marker never completed).
	sieveEvents := a.sieve.Flush(a.toolNames)
	var pendingText strings.Builder
	for _, evt := range sieveEvents {
		if evt.Type == "text" {
			if text, ok := evt.Data.(string); ok && text != "" {
				pendingText.WriteString(text)
			}
		}
	}

	fullText := a.contentBuf.String()
	toolCalls, _, parseErrors := a.engine.Parse(fullText, a.tools)

	validationErrors := a.engine.Validate(toolCalls, a.tools)
	if a.ValidateExtra != nil {
		validationErrors = append(validationErrors, a.ValidateExtra(toolCalls)...)
	}
	allErrors := append(validationErrors, parseErrors...)

	// Retry on validation errors.
	if a.engine.ShouldRetry(allErrors, a.retryCount) && a.retryFn != nil {
		feedback := a.engine.GenerateErrorFeedback(allErrors)
		backoff := a.engine.CalculateBackoff(a.retryCount)

		slog.Info("StreamSieveAdapter: retrying stream with error feedback",
			"retry", a.retryCount+1,
			"errors", len(allErrors),
			"backoff_ms", backoff.Milliseconds(),
		)

		time.Sleep(backoff)
		retryReq := a.engine.BuildRetryRequest(a.originalReq, feedback)
		retryChan, retryErr := a.retryFn(a.ctx, retryReq, a.retryCount+1)
		if retryErr == nil && retryChan != nil {
			var results []providers.StreamResponse
			for chunk := range retryChan {
				results = append(results, chunk)
			}
			return results
		}
		slog.Error("StreamSieveAdapter: retry failed, falling back", "error", retryErr)
	}

	if len(toolCalls) > 0 {
		// Set per-call index for OpenAI streaming delta format.
		tcs := make([]providers.ToolCall, len(toolCalls))
		for i := range toolCalls {
			idx := i
			tcs[i] = toolCalls[i]
			tcs[i].Index = &idx
		}

		var out []providers.StreamResponse
		if !a.sentRole {
			out = append(out, a.roleChunk())
		}
		out = append(out, providers.StreamResponse{
			ID: a.msgID, Object: "chat.completion.chunk", Created: a.created, Model: a.model,
			Choices: []providers.StreamChoice{{
				Index: 0,
				Delta: providers.Delta{ToolCalls: tcs},
			}},
		})
		out = append(out, a.finishChunk("tool_calls")...)
		return out
	}

	// Emit any text that was held by the sieve and not yet emitted.
	heldText := strings.TrimSpace(pendingText.String())
	if heldText != "" {
		return append(a.emitText(heldText), a.finishChunk("stop")...)
	}

	return a.finishChunk("stop")
}

// ── helpers ────────────────────────────────────────────────────────────────

func (a *StreamSieveAdapter) emitText(text string) []providers.StreamResponse {
	if text == "" {
		return nil
	}
	var out []providers.StreamResponse
	if !a.sentRole {
		out = append(out, a.roleChunk())
	}
	out = append(out, providers.StreamResponse{
		ID: a.msgID, Object: "chat.completion.chunk", Created: a.created, Model: a.model,
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.Delta{Content: text},
		}},
	})
	return out
}

func (a *StreamSieveAdapter) emitThinking(chunk string) []providers.StreamResponse {
	if chunk == "" {
		return nil
	}
	var out []providers.StreamResponse
	if !a.sentRole {
		out = append(out, a.roleChunk())
	}
	out = append(out, providers.StreamResponse{
		ID: a.msgID, Object: "chat.completion.chunk", Created: a.created, Model: a.model,
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.Delta{ReasoningContent: chunk},
		}},
	})
	return out
}

func (a *StreamSieveAdapter) roleChunk() providers.StreamResponse {
	a.sentRole = true
	return providers.StreamResponse{
		ID: a.msgID, Object: "chat.completion.chunk", Created: a.created, Model: a.model,
		Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Role: "assistant"}}},
	}
}

func (a *StreamSieveAdapter) finishChunk(reason string) []providers.StreamResponse {
	r := reason
	return []providers.StreamResponse{{
		ID: a.msgID, Object: "chat.completion.chunk", Created: a.created, Model: a.model,
		Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{}, FinishReason: &r}},
	}}
}
