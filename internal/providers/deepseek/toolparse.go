package deepseek

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

// truncate shortens a string for logging
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// parseToolCallsFromText detects and parses tool calls from DSML markup in text
// Returns OpenAI-compatible ToolCall objects
func parseToolCallsFromText(text string, tools []providers.Tool) ([]providers.ToolCall, error) {
	// Check for DSML tool_calls markup
	if !strings.Contains(text, "<|DSML|tool_calls>") && !strings.Contains(text, "<tool_call>") {
		return nil, nil // No tool calls found
	}

	slog.Debug("Parsing tool calls from text", "text_preview", truncate(text, 300))

	// Use the shared toolcall package for robust parsing
	parsedCalls := toolcall.ParseToolCalls(text)

	if len(parsedCalls) == 0 {
		slog.Debug("No tool calls found by parser")
		return nil, nil
	}

	slog.Debug("Found tool calls", "count", len(parsedCalls))

	var toolCalls []providers.ToolCall
	for i, call := range parsedCalls {
		slog.Debug("Parsed tool call", "name", call.Name, "args", call.Input)

		// Validate tool call parameters if tool definitions are available
		if tools != nil {
			validateToolCall(call.Name, call.Input, tools)
		}

		// Convert input to JSON string for OpenAI format
		argsJSON, err := json.Marshal(call.Input)
		if err != nil {
			slog.Warn("Failed to marshal tool call input", "error", err)
			continue
		}

		slog.Debug("Parsed tool call", "name", call.Name, "args", string(argsJSON))

		toolCalls = append(toolCalls, providers.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: providers.ToolCallFunction{
				Name:      call.Name,
				Arguments: string(argsJSON),
			},
		})
	}

	return toolCalls, nil
}

// validateToolCall validates a tool call against provided tool definitions
func validateToolCall(name string, input map[string]any, tools []providers.Tool) {
	// Find matching tool definition
	for _, t := range tools {
		if t.Function.Name == name {
			if t.Function.Parameters != nil {
				schemaJSON, err := json.Marshal(t.Function.Parameters)
				if err != nil {
					slog.Warn("Failed to marshal tool schema", "tool", name, "error", err)
					return
				}
				result := toolcall.ValidateToolCall(name, input, string(schemaJSON))
				if result.HasErrors() {
					slog.Warn("Tool call validation failed",
						"tool", name,
						"error_count", len(result.Errors),
						"errors", result.Errors)
				}
			}
			break
		}
	}
}
