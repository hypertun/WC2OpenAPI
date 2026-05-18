package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/user/wc2api/internal/providers"
)

// ProviderSelector is implemented by the server to route requests to the appropriate provider
type ProviderSelector interface {
	// GetProviderByModel returns the provider for the given model name
	GetProviderByModel(model string) (providers.Provider, bool)
	// ListModels returns all available models from all providers
	ListModels() []providers.Model
}

// ChatCompletions returns a handler for chat completions
func ChatCompletions(selector ProviderSelector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req providers.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Error("Failed to decode request", "error", err)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
			return
		}

		// Validate request
		if req.Model == "" {
			writeError(w, http.StatusBadRequest, "Model is required")
			return
		}

		if len(req.Messages) == 0 {
			writeError(w, http.StatusBadRequest, "Messages are required")
			return
		}

		// Get the appropriate provider for this model
		provider, ok := selector.GetProviderByModel(req.Model)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown or unavailable model: %s", req.Model))
			return
		}

		if req.Stream {
			// Handle streaming response
			handleStreaming(w, r, provider, &req)
		} else {
			// Handle non-streaming response
			handleNonStreaming(w, r.Context(), provider, &req)
		}
	}
}

// handleNonStreaming handles non-streaming chat completions
func handleNonStreaming(w http.ResponseWriter, ctx context.Context, provider providers.Provider, req *providers.ChatRequest) {
	resp, err := provider.CreateChatCompletion(ctx, req)
	if err != nil {
		slog.Error("Chat completion failed", "error", err)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Completion failed: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// handleStreaming handles streaming chat completions
func handleStreaming(w http.ResponseWriter, r *http.Request, provider providers.Provider, req *providers.ChatRequest) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Flush headers
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("Streaming not supported")
		writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Create streaming response
	streamChan, err := provider.CreateChatCompletionStream(r.Context(), req)
	if err != nil {
		slog.Error("Stream creation failed", "error", err)
		// Can't write error response now, so just end the stream
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// Stream responses
	for chunk := range streamChan {
		// Debug: Check the actual type of Arguments in tool calls
		for _, choice := range chunk.Choices {
			for _, tc := range choice.Delta.ToolCalls {
				slog.Debug("Pre-marshal check", 
					"name", tc.Function.Name,
					"argsType", fmt.Sprintf("%T", tc.Function.Arguments),
					"argsValue", tc.Function.Arguments)
			}
		}
		
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("Failed to marshal chunk", "error", err)
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", string(data))
		flusher.Flush()
	}

	// Send done signal
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// writeError writes an error response in OpenAI format
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "invalid_request_error",
			"code":    status,
		},
	})
}

// ListModels returns a handler for listing models
func ListModels(selector ProviderSelector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models := selector.ListModels()

		response := struct {
			Object string            `json:"object"`
			Data   []providers.Model `json:"data"`
		}{
			Object: "list",
			Data:   models,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}

// HealthCheck returns a health check handler
func HealthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": "1.0.0",
		})
	}
}