package toolcall

import (
	"context"
	"testing"

	providers "github.com/user/wc2api/internal/providers"
)

func TestStreamSieveAdapter_NoTools(t *testing.T) {
	engine := New(DefaultConfig())
	adapter := NewStreamSieveAdapter(engine, nil, "msg-1", "test-model", 1000)

	// FeedText should still stream text when no tools
	chunks := adapter.FeedText("hello")
	if len(chunks) == 0 {
		t.Fatal("expected text chunks even when no tools")
	}

	// FeedThinking should return chunks
	chunks = adapter.FeedThinking("thinking...")
	if len(chunks) == 0 {
		t.Fatal("expected thinking chunks")
	}
	hasReasoning := false
	for _, c := range chunks {
		for _, ch := range c.Choices {
			if ch.Delta.ReasoningContent != "" {
				hasReasoning = true
			}
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning_content in thinking chunk")
	}

	// Flush should just return finish reason
	flush := adapter.Flush()
	if len(flush) != 1 {
		t.Fatalf("expected 1 flush chunk, got %d", len(flush))
	}
	if flush[0].Choices[0].FinishReason == nil || *flush[0].Choices[0].FinishReason != "stop" {
		t.Error("expected finish_reason=stop")
	}
}

func TestStreamSieveAdapter_NoTools_FlushAfterText(t *testing.T) {
	engine := New(DefaultConfig())
	adapter := NewStreamSieveAdapter(engine, nil, "msg-1", "test-model", 1000)

	adapter.FeedText("hello")
	flush := adapter.Flush()
	// Finish_reason only — text was already streamed via FeedText
	if len(flush) != 1 || *flush[0].Choices[0].FinishReason != "stop" {
		t.Error("expected finish_reason=stop with no additional text chunks")
	}
}

func TestStreamSieveAdapter_ToolsNoToolCall(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{Name: "Read", Description: "read a file"}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	// Feed plain text (no tool call markers) — should be emitted immediately
	chunks := adapter.FeedText("Hello, this is a normal response. No tools here.")
	if len(chunks) == 0 {
		t.Fatal("expected text chunks to be emitted immediately via sieve")
	}
	hasContent := false
	for _, c := range chunks {
		for _, ch := range c.Choices {
			if ch.Delta.Content != "" {
				hasContent = true
			}
		}
	}
	if !hasContent {
		t.Error("expected content in text chunks")
	}

	// Flush should end with stop (text already streamed, nothing held in sieve)
	flush := adapter.Flush()
	if len(flush) != 1 {
		t.Fatalf("expected 1 flush chunk, got %d", len(flush))
	}
	if *flush[0].Choices[0].FinishReason != "stop" {
		t.Error("expected finish_reason=stop")
	}
}

func TestStreamSieveAdapter_ToolsWithToolCall(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{
			Name:        "Read",
			Description: "read a file",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"filePath": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"filePath"},
			},
		}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	// Feed text leading up to tool call
	chunks := adapter.FeedText("Let me read that file. ")
	if len(chunks) == 0 {
		t.Fatal("expected text before tool call to be emitted")
	}

	// Feed tool call marker (sieve should capture it, no tool_calls events from adapter)
	marker := `##TOOL_CALL##{"name":"Read","input":{"filePath":"/tmp/x"}}##END_CALL##`
	_ = adapter.FeedText(marker)

	flush := adapter.Flush()
	if len(flush) < 2 {
		t.Fatalf("expected 2+ flush chunks (tool_calls + finish), got %d", len(flush))
	}

	// Final chunk should have finish_reason=tool_calls
	last := flush[len(flush)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %v", last.Choices[0].FinishReason)
	}

	// There should be a tool_calls delta somewhere
	foundToolCall := false
	for _, c := range flush {
		for _, ch := range c.Choices {
			if len(ch.Delta.ToolCalls) > 0 {
				foundToolCall = true
				if ch.Delta.ToolCalls[0].Function.Name != "Read" {
					t.Errorf("expected tool name 'Read', got %q", ch.Delta.ToolCalls[0].Function.Name)
				}
			}
		}
	}
	if !foundToolCall {
		t.Error("expected tool_calls in flush output")
	}
}

func TestStreamSieveAdapter_ThinkingStreaming(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{Name: "Read"}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	// Thinking always emits immediately, even with tools
	chunks := adapter.FeedThinking("I need to think about this...")
	if len(chunks) == 0 {
		t.Fatal("expected thinking chunks to be emitted immediately")
	}
	hasReasoning := false
	for _, c := range chunks {
		for _, ch := range c.Choices {
			if ch.Delta.ReasoningContent != "" {
				hasReasoning = true
			}
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning_content in thinking chunk")
	}
}

func TestStreamSieveAdapter_EmptyContent(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{Name: "Read"}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	// Feed empty chunks should be no-ops
	if chunks := adapter.FeedText(""); chunks != nil {
		t.Error("expected nil for empty text")
	}
	if chunks := adapter.FeedThinking(""); chunks != nil {
		t.Error("expected nil for empty thinking")
	}

	flush := adapter.Flush()
	if len(flush) != 1 {
		t.Fatalf("expected 1 flush chunk, got %d", len(flush))
	}
	if *flush[0].Choices[0].FinishReason != "stop" {
		t.Error("expected finish_reason=stop")
	}
}

func TestStreamSieveAdapter_RetryNotConfigured(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{
			Name:        "Read",
			Description: "read",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"filePath": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"filePath"},
			},
		}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	adapter.FeedText(`Let me read. ##TOOL_CALL##{"name":"Read","input":{"filePath":"/x"}}##END_CALL##`)
	flush := adapter.Flush()

	// Without a retry function, should still work and emit tool calls
	if len(flush) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(flush))
	}
	last := flush[len(flush)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %v", last.Choices[0].FinishReason)
	}
}

func TestStreamSieveAdapter_RetryFallsBackWhenChannelNil(t *testing.T) {
	engine := New(DefaultConfig())
	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{Name: "Read"}},
	}
	adapter := NewStreamSieveAdapter(engine, tools, "msg-1", "test-model", 1000)

	called := false
	retryFn := func(ctx context.Context, req *providers.ChatRequest, retryCount int) (<-chan providers.StreamResponse, error) {
		called = true
		return nil, nil // nil channel — should not hang
	}
	adapter.WithRetry(context.Background(), retryFn, &providers.ChatRequest{}, 0)

	adapter.FeedText(`##TOOL_CALL##{"name":"Read","input":{"filePath":"/x"}}##END_CALL##`)
	flush := adapter.Flush()

	if !called {
		t.Error("retry should have been invoked (no matching tool def for Read)")
	}
	// Even with retry falling back (nil channel), we should still get output
	if len(flush) < 1 {
		t.Error("expected some output after retry fallback")
	}
}
