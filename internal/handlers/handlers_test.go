package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/user/wc2api/internal/providers"
)

// mockSelector implements ProviderSelector for testing.
type mockSelector struct {
	provider providers.Provider
}

func (m *mockSelector) GetProviderByModel(model string) (providers.Provider, bool) {
	return m.provider, true
}

func (m *mockSelector) ListModels() []providers.Model {
	return []providers.Model{}
}

// mockProvider implements providers.Provider for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string {
	return m.name
}

func (m *mockProvider) ListModels() []providers.Model {
	return []providers.Model{}
}

func (m *mockProvider) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{
		ID:      "test-id",
		Model:   req.Model,
		Object:  "chat.completion",
		Created: 123456789,
		Choices: []providers.Choice{
			{
				Index: 0,
				Message: providers.Message{
					Role:    "assistant",
					Content: "Test response",
				},
				FinishReason: "stop",
			},
		},
	}, nil
}

func (m *mockProvider) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	ch := make(chan providers.StreamResponse, 1)
	close(ch)
	return ch, nil
}

func (m *mockProvider) Close() error {
	return nil
}

// TestChatCompletions_NonStreaming validates non-streaming handler works correctly.
func TestChatCompletions_NonStreaming(t *testing.T) {
	mockSelector := &mockSelector{provider: &mockProvider{name: "test"}}
	handler := ChatCompletions(mockSelector)

	req := providers.ChatRequest{
		Model:    "test-model",
		Messages: []providers.Message{{Role: "user", Content: "test"}},
		Stream:   false,
	}
	body, _ := json.Marshal(req)

	httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.RemoteAddr = "192.168.1.1:12345"

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httpReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp providers.ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Model != "test-model" {
		t.Errorf("Model mismatch: got %s, want test-model", resp.Model)
	}
}




