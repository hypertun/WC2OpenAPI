package providers

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// mockSender is a test SendFunc that returns canned responses
type mockSender struct {
	responses map[string]*ChatResponse
	callCount int
}

func (m *mockSender) send(ctx context.Context, req *ChatRequest, retryCount int) (*ChatResponse, error) {
	m.callCount++
	if resp, ok := m.responses[req.Model]; ok {
		return resp, nil
	}
	return &ChatResponse{
		ID:      fmt.Sprintf("mock-%d", m.callCount),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: MessageContent("mock response"),
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, nil
}

// trackingMockSender wraps mockSender to track calls
type trackingMockSender struct {
	mock   *mockSender
	onCall func(*ChatRequest)
}

func (t *trackingMockSender) send(ctx context.Context, req *ChatRequest, retryCount int) (*ChatResponse, error) {
	if t.onCall != nil {
		t.onCall(req)
	}
	return t.mock.send(ctx, req, retryCount)
}

func TestSplitAndSend_SingleChunk(t *testing.T) {
	// When query fits in one chunk, SplitAndSend should return an error (shouldn't be called)
	mock := &mockSender{responses: make(map[string]*ChatResponse)}

	msgs := []Message{
		{Role: "user", Content: MessageContent("hello")},
	}

	estimator := func(msgs []Message, tools []Tool) int {
		return len(msgs) * 10 // Small estimate
	}

	_, err := SplitAndSend(context.Background(), mock.send, &ChatRequest{
		Model:    "test",
		Messages: msgs,
	}, 10000, estimator)

	if err == nil {
		t.Error("expected error when query fits in one chunk")
	}
}

func TestSplitAndSend_IntermediateChunksDiscarded(t *testing.T) {
	// When splitting, intermediate responses should be discarded and not included in final response
	mock := &mockSender{responses: make(map[string]*ChatResponse)}

	msgs := []Message{
		{Role: "system", Content: MessageContent("You are helpful")},
		{Role: "user", Content: MessageContent(repeatString("a", 1000))},
		{Role: "user", Content: MessageContent(repeatString("b", 1000))},
		{Role: "user", Content: MessageContent(repeatString("c", 1000))},
	}

	// Estimator that forces splitting
	estimator := func(msgs []Message, tools []Tool) int {
		return len(msgs) * 500 // Each message ~500 chars
	}

	resp, err := SplitAndSend(context.Background(), mock.send, &ChatRequest{
		Model:    "test",
		Messages: msgs,
	}, 1500, estimator)

	if err != nil {
		t.Fatalf("SplitAndSend failed: %v", err)
	}

	// Should have made multiple calls (at least 2 for splitting)
	if mock.callCount < 2 {
		t.Errorf("expected at least 2 calls (intermediate + final), got %d", mock.callCount)
	}

	// Final response should be from the last call
	if len(resp.Choices) == 0 {
		t.Fatal("no choices in response")
	}

	content := string(resp.Choices[0].Message.Content)
	if content != "mock response" {
		t.Errorf("unexpected response content: %s", content)
	}

	// Usage should be accumulated
	if resp.Usage.TotalTokens <= 0 {
		t.Error("usage not accumulated")
	}
}

func TestSplitAndSend_ToolsOnlyOnFinal(t *testing.T) {
	// Verify that intermediate chunks are sent without tools, final chunk with tools
	var intermediateCalls int
	var finalCall *ChatRequest

	mock := &trackingMockSender{
		mock: &mockSender{responses: make(map[string]*ChatResponse)},
		onCall: func(req *ChatRequest) {
			if len(req.Tools) > 0 {
				finalCall = req
			} else {
				intermediateCalls++
			}
		},
	}

	msgs := []Message{
		{Role: "user", Content: MessageContent(repeatString("a", 500))},
		{Role: "user", Content: MessageContent(repeatString("b", 500))},
		{Role: "user", Content: MessageContent(repeatString("c", 500))},
	}

	tools := []Tool{
		{
			Type: "function",
			Function: ToolFunction{Name: "search", Description: "Search"},
		},
	}

	estimator := func(msgs []Message, tools []Tool) int {
		return len(msgs) * 200
	}

	_, err := SplitAndSend(context.Background(), mock.send, &ChatRequest{
		Model:    "test",
		Messages: msgs,
		Tools:    tools,
	}, 700, estimator)

	if err != nil {
		t.Fatalf("SplitAndSend failed: %v", err)
	}

	// Should have at least one intermediate call without tools
	if intermediateCalls == 0 {
		t.Error("expected at least one intermediate call without tools")
	}

	// Final call should have tools
	if finalCall == nil {
		t.Error("expected final call with tools")
	} else if len(finalCall.Tools) == 0 {
		t.Error("final call should have tools")
	}
}

// repeatString is a helper for tests
func repeatString(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
