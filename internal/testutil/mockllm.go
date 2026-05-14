package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
)

// MockLLMServer wraps an httptest.Server with configurable responses.
type MockLLMServer struct {
	*httptest.Server
	calls int
}

// NewMockLLMServer creates a mock server with a custom handler.
func NewMockLLMServer(handler http.HandlerFunc) *MockLLMServer {
	m := &MockLLMServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls++
		handler(w, r)
	}))
	return m
}

// Calls returns the number of requests received.
func (m *MockLLMServer) Calls() int {
	return m.calls
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// WriteSSE writes a server-sent event.
func WriteSSE(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Write([]byte("data: " + data + "\n\ndata: [DONE]\n\n"))
}

// MalformedToolCallResponse returns a handler that returns a malformed tool call (missing required param).
func MalformedToolCallResponse(_ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"##TOOL_CALL##{\"name\":\"bash\",\"input\":{\"description\":\"test\"}}##END_CALL##"}}]`))
	}
}

// CorrectedToolCallResponse returns a handler that returns a corrected tool call.
func CorrectedToolCallResponse(_ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"##TOOL_CALL##{\"name\":\"bash\",\"input\":{\"command\":\"ls -la\"}}##END_CALL##"}}]`))
	}
}

// StreamMalformedToolCall returns a handler for streaming malformed tool calls.
func StreamMalformedToolCall(_ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEWithContent(w, `{"choices":[{"delta":{"content":"##TOOL_CALL##{\"name\":\"bash\",\"input\":{\"description\":\"test\"}}##END_CALL##","phase":"tool_call"}}]}`)
	}
}

// StreamCorrectedToolCall returns a handler for streaming corrected tool calls.
func StreamCorrectedToolCall(_ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEWithContent(w, `{"choices":[{"delta":{"content":"##TOOL_CALL##{\"name\":\"bash\",\"input\":{\"command\":\"ls -la\"}}##END_CALL##","phase":"tool_call"}}]}`)
	}
}

// writeSSEWithContent writes an SSE event with the given content.
func writeSSEWithContent(w http.ResponseWriter, content string) {
	w.Write([]byte("data: " + content + "\n\ndata: [DONE]\n\n"))
}

// ChatIDResponse returns a handler for the create chat session endpoint (Qwen).
func ChatIDResponse(chatID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chats/new") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"id": chatID},
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// TokenResponse returns a handler that sets an auth token cookie.
func TokenResponse(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/signin") || strings.HasSuffix(r.URL.Path, "/login") {
			http.SetCookie(w, &http.Cookie{Name: "token", Value: token})
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// HTTPErrorResponse returns a handler that returns an HTTP error.
func HTTPErrorResponse(statusCode int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		w.Write([]byte("error"))
	}
}
