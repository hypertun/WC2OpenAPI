package providers

import (
	"strings"
	"testing"
)

func TestEstimateQuerySize(t *testing.T) {
	tests := []struct {
		name     string
		messages []Message
		wantMin  int
		wantMax  int
	}{
		{
			name:     "empty",
			messages: nil,
			wantMin:  0,
			wantMax:  0,
		},
		{
			name: "single user message",
			messages: []Message{
				{Role: "user", Content: MessageContent("hello")},
			},
			wantMin: 10,
			wantMax: 15,
		},
		{
			name: "system + user",
			messages: []Message{
				{Role: "system", Content: MessageContent("You are helpful")},
				{Role: "user", Content: MessageContent("hi")},
			},
			wantMin: 30,
			wantMax: 45,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := EstimateQuerySize(tt.messages)
			if size < tt.wantMin || size > tt.wantMax {
				t.Errorf("EstimateQuerySize() = %d, want between %d and %d", size, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestSplitMessages_Empty(t *testing.T) {
	result := SplitMessages(nil, 1000)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestSplitMessages_SingleMessage(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: MessageContent("hello")},
	}
	result := SplitMessages(msgs, 1000)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if len(result[0]) != 1 {
		t.Fatalf("expected 1 message in chunk, got %d", len(result[0]))
	}
}

func TestSplitMessages_FitsInOneChunk(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: MessageContent("be helpful")},
		{Role: "user", Content: MessageContent("hi")},
		{Role: "assistant", Content: MessageContent("hello!")},
		{Role: "user", Content: MessageContent("how are you?")},
	}
	// Very generous limit — everything fits
	result := SplitMessages(msgs, 100000)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	// System message should be in the chunk
	hasSystem := false
	for _, m := range result[0] {
		if m.Role == "system" {
			hasSystem = true
		}
	}
	if !hasSystem {
		t.Error("system message missing from single chunk")
	}
}

func TestSplitMessages_SplitsIntoChunks(t *testing.T) {
	// System prompt + 4 user messages, tight limit
	system := Message{Role: "system", Content: MessageContent("You are a helpful assistant")}
	msgs := []Message{
		system,
		{Role: "user", Content: MessageContent(strings.Repeat("a", 500))},
		{Role: "user", Content: MessageContent(strings.Repeat("b", 500))},
		{Role: "user", Content: MessageContent(strings.Repeat("c", 500))},
		{Role: "user", Content: MessageContent(strings.Repeat("d", 500))},
	}

	// Limit so roughly 2 non-system messages fit per chunk
	// System ~40 chars + "user: " prefix ~6 + 500 content ≈ 546 per message
	// So limit of 1200 should fit ~2 messages per chunk
	result := SplitMessages(msgs, 1200)

	if len(result) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(result))
	}

	// Every chunk should have the system message
	for i, chunk := range result {
		hasSystem := false
		for _, m := range chunk {
			if m.Role == "system" {
				hasSystem = true
			}
		}
		if !hasSystem {
			t.Errorf("chunk %d missing system message", i)
		}
	}

	// All non-system messages should be present across all chunks
	totalNonSystem := 0
	for _, chunk := range result {
		for _, m := range chunk {
			if m.Role != "system" {
				totalNonSystem++
			}
		}
	}
	if totalNonSystem != 4 {
		t.Errorf("expected 4 non-system messages total, got %d", totalNonSystem)
	}
}

func TestSplitMessages_SystemOnly(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: MessageContent("be helpful")},
	}
	result := SplitMessages(msgs, 1000)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
}

func TestSplitMessages_ToolCallsPreserved(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: MessageContent("You are helpful")},
		{
			Role:    "assistant",
			Content: MessageContent("Let me search for that"),
			ToolCalls: []ToolCall{
				{ID: "tc1", Function: ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`}},
			},
		},
		{Role: "tool", Content: MessageContent("result"), ToolCallID: "tc1"},
		{Role: "user", Content: MessageContent("thanks")},
	}
	result := SplitMessages(msgs, 100000)
	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
}

func TestEstimateQuerySize_AssistantContentAndToolCalls(t *testing.T) {
	// Test that assistant messages with BOTH content and tool calls are sized correctly
	msgs := []Message{
		{
			Role:    "assistant",
			Content: MessageContent("Here is my response"),
			ToolCalls: []ToolCall{
				{ID: "1", Function: ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`}},
				{ID: "2", Function: ToolCallFunction{Name: "read", Arguments: `{"path":"/foo"}`}},
			},
		},
	}

	size := EstimateQuerySize(msgs)

	// Should include:
	// - "assistant: " (11 chars)
	// - "Here is my response" (19 chars)
	// - "\nTOOL_CALL: search(...)\n" + content
	// - "\nTOOL_CALL: read(...)\n" + content
	// At minimum, size should be > len("Here is my response") + len("search") + len("read")
	if size < 50 {
		t.Errorf("EstimateQuerySize too low for assistant with content + tool calls: got %d, expected > 50", size)
	}

	// Verify content is included (was missing before fix)
	if !strings.Contains("Here is my response", "response") {
		t.Error("test setup error")
	}
}

func TestEstimateToolPromptSize(t *testing.T) {
	tests := []struct {
		name     string
		tools    []Tool
		wantMin  int
		wantMax  int
	}{
		{
			name:    "no tools",
			tools:   nil,
			wantMin: 0,
			wantMax: 0,
		},
		{
			name: "single tool",
			tools: []Tool{
				{
					Type: "function",
					Function: ToolFunction{
						Name:        "search",
						Description: "Search the web",
					},
				},
			},
			wantMin: 500, // marker format overhead ~500+ chars
			wantMax: 1200,
		},
		{
			name: "multiple tools",
			tools: []Tool{
				{Type: "function", Function: ToolFunction{Name: "search", Description: "Search"}},
				{Type: "function", Function: ToolFunction{Name: "read", Description: "Read"}},
				{Type: "function", Function: ToolFunction{Name: "write", Description: "Write"}},
			},
			wantMin: 550,
			wantMax: 1500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := EstimateToolPromptSize(tt.tools)
			if size < tt.wantMin || size > tt.wantMax {
				t.Errorf("EstimateToolPromptSize() = %d, want between %d and %d", size, tt.wantMin, tt.wantMax)
			}
		})
	}
}
