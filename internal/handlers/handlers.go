package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/user/wc2api/internal/providers"
	middlewarePkg "github.com/user/wc2api/internal/server/middleware"
)

// ProviderSelector is implemented by the server to route requests to the appropriate provider
type ProviderSelector interface {
	// GetProviderByModel returns the provider for the given model name
	GetProviderByModel(model string) (providers.Provider, bool)
	// ListModels returns all available models from all providers
	ListModels() []providers.Model
}

// requestMeta holds metadata about a request for logging purposes.
type requestMeta struct {
	requestID string
	startTime time.Time
	clientIP  string
	userAgent string
	apiKey    string
}

// ChatCompletions returns a handler for chat completions
func ChatCompletions(selector ProviderSelector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req providers.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
			return
		}

		// Capture request metadata
		startTime := time.Now()
		requestID := middleware.GetReqID(r.Context())
		clientIP := r.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = strings.Split(r.RemoteAddr, ":")[0]
		}
		userAgent := r.UserAgent()
		apiKey := middlewarePkg.GetAPIKey(r)

		meta := requestMeta{
			requestID: requestID,
			startTime: startTime,
			clientIP:  clientIP,
			userAgent: userAgent,
			apiKey:    apiKey,
		}

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
			handleStreaming(w, r, provider, &req, meta)
		} else {
			handleNonStreaming(w, r.Context(), provider, &req, meta)
		}
	}
}

func handleNonStreaming(w http.ResponseWriter, ctx context.Context, provider providers.Provider, req *providers.ChatRequest, meta requestMeta) {
	resp, err := provider.CreateChatCompletion(ctx, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Completion failed: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func handleStreaming(w http.ResponseWriter, r *http.Request, provider providers.Provider, req *providers.ChatRequest, meta requestMeta) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	streamChan, err := provider.CreateChatCompletionStream(r.Context(), req)
	if err != nil {
		slog.Error("stream setup failed", "error", err, "model", req.Model)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// Stream responses with keep-alive pings to prevent client timeout during buffering
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	ctx := r.Context()
	chunkCount := 0
	totalBytes := 0

loop:
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				break loop
			}

			data, err := json.Marshal(chunk)
			if err != nil {
				continue
			}
			chunkCount++
			totalBytes += len(data)
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()

		case <-keepAlive.C:
			// Send keep-alive comment to prevent client timeout during long buffering phases
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()

		case <-ctx.Done():
			// Client cancelled or context deadline exceeded — stop streaming
			return
		}
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