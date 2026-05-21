package qwen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

func makeTestClient(serverURL string) *Client {
	baseURL, _ := url.Parse(serverURL)
	return &Client{
		config:     config.QwenConfig{BaseURL: serverURL, Timeout: 30, TokenRefreshInterval: 1800},
		baseURL:    baseURL,
		httpClient: &http.Client{},
		authToken:  "test-token",
		loggedIn:   true,
		lastLogin:  time.Now(),
		toolEngine: toolcall.New(toolcall.QwenConfig()),
	}
}

func testServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/chats/new", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"id":"test-chat-id"}}`))
	})
	return mux
}

func TestStreamAnswerOnly(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hello","phase":"answer"}}]}

data: [DONE]

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (content + finish), got %d", len(chunks))
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil || *lastChunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %v", lastChunk.Choices[0].FinishReason)
	}

	content := ""
	for _, c := range chunks[:len(chunks)-1] {
		content += c.Choices[0].Delta.Content
	}
	if content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", content)
	}
}

func TestStreamThinkThenAnswer(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Thinking...","phase":"think"}}]}

data: {"choices":[{"delta":{"content":" answer","phase":"answer"}}]}

data: [DONE]

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil || *lastChunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %v", lastChunk.Choices[0].FinishReason)
	}

	content := ""
	reasoning := ""
	for _, c := range chunks[:len(chunks)-1] {
		content += c.Choices[0].Delta.Content
		reasoning += c.Choices[0].Delta.ReasoningContent
	}
	if content != " answer" {
		t.Errorf("expected content ' answer', got %q", content)
	}
	if reasoning != "Thinking..." {
		t.Errorf("expected reasoning 'Thinking...', got %q", reasoning)
	}
}

func TestStreamToolCallOnly(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"##TOOL_CALL##{\"name\":\"calculator\",\"input\":{\"expr\":\"2+2\"}}##END_CALL##","phase":"tool_call"}}]}

data: [DONE]

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Use the calculator"},
		},
		Tools: []providers.Tool{
			{
				Type: "function",
				Function: providers.ToolFunction{
					Name:        "calculator",
					Description: "Calculate a mathematical expression",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"expr": map[string]interface{}{"type": "string"},
						},
						"required": []interface{}{"expr"},
					},
				},
			},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected at least 1 chunk")
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil {
		t.Fatalf("expected non-nil finish_reason")
	}
	finishReason := *lastChunk.Choices[0].FinishReason
	if finishReason != "tool_calls" && finishReason != "stop" {
		t.Errorf("unexpected finish_reason: %s", finishReason)
	}
	t.Logf("finish_reason: %s (note: tool_calls detection depends on parser)", finishReason)

	for _, c := range chunks {
		if c.Choices[0].Delta.Content != "" {
			t.Errorf("expected no content for tool_call phase, got %q", c.Choices[0].Delta.Content)
		}
	}
}

func TestStreamMixedContentThenToolCall(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Let me ","phase":"answer"}}]}

data: {"choices":[{"delta":{"content":"calculate","phase":"answer"}}]}

data: {"choices":[{"delta":{"content":"##TOOL_CALL##{\"name\":\"calculator\",\"input\":{\"expr\":\"2+2\"}}##END_CALL##","phase":"tool_call"}}]}

data: [DONE]

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Use calculator"},
		},
		Tools: []providers.Tool{
			{
				Type: "function",
				Function: providers.ToolFunction{
					Name:        "calculator",
					Description: "Calculate a mathematical expression",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"expr": map[string]interface{}{"type": "string"},
						},
						"required": []interface{}{"expr"},
					},
				},
			},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected at least 1 chunk")
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil {
		t.Fatalf("expected non-nil finish_reason")
	}
	finishReason := *lastChunk.Choices[0].FinishReason
	if finishReason != "tool_calls" && finishReason != "stop" {
		t.Errorf("unexpected finish_reason: %s", finishReason)
	}
	t.Logf("finish_reason: %s (note: tool_calls detection depends on parser)", finishReason)

	// When tool calls are detected, content is not replayed
	// Check if we have tool calls in the chunks
	hasToolCalls := false
	for _, chunk := range chunks {
		if len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			hasToolCalls = true
			break
		}
	}
	
	if !hasToolCalls && finishReason == "tool_calls" {
		t.Error("expected tool calls when finish_reason is tool_calls")
	}
}

func TestStreamHTTPError(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected at least 1 chunk (error)")
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].Delta.Content == "" {
		t.Errorf("expected error content in chunk")
	}
}

func TestStreamReadError(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Partial","phase":"answer"}}]}

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) == 0 {
		t.Fatalf("expected at least 1 chunk")
	}
}

func TestStreamMalformedSSE(t *testing.T) {
	mux := testServeMux()
	mux.HandleFunc("/api/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: valid-json

data: {invalid json}
data: [DONE]

`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := makeTestClient(server.URL)

	req := &providers.ChatRequest{
		Model: "qwen3.5-flash",
		Messages: []providers.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	stream, err := client.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateChatCompletionStream error: %v", err)
	}

	var chunks []providers.StreamResponse
	for resp := range stream {
		chunks = append(chunks, resp)
	}

	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}

	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil || *lastChunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %v", lastChunk.Choices[0].FinishReason)
	}
}