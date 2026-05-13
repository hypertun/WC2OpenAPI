package toolcall

import (
	"strings"
	"testing"

	providers "github.com/user/wc2api/internal/providers"
)

// TestE2E_FeedbackLoop tests the full feedback loop:
// 1. Parse tool calls from text (DSML format)
// 2. Validate tool calls
// 3. Generate error feedback
// 4. Build retry request
// 5. Verify retry request has feedback as system message
func TestE2E_FeedbackLoop(t *testing.T) {
	// Step1: Parse tool calls from text (DSML format)
	text := `<invoke name="Bash">
<parameter name="cmd"><![CDATA[ls -la]]></parameter>
</invoke>`

	calls := ParseToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	// Step 2: Validate using OpenAI format (should fail because "cmd" is not "command")
	tools := []providers.Tool{
		{
			Type: "function",
			Function: providers.ToolFunction{
				Name: "Bash",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{"type": "string"},
					},
					"required": []interface{}{"command"},
				},
			},
		},
	}

	// Convert to OpenAI format for validation
	callsJSON := []providers.ToolCall{
		{
			Function: providers.ToolCallFunction{
				Name:      "Bash",
				Arguments: `{"cmd":"ls -la"}`,
			},
		},
	}

	allErrors := ValidateToolCallsWithErrors(callsJSON, tools)

	if len(allErrors) == 0 {
		t.Fatal("expected validation errors")
	}

	// Step 3: Generate error feedback
	feedback := GenerateToolCallErrorFeedback(allErrors)
	if !strings.Contains(feedback, "Bash") {
		t.Error("feedback should mention Bash tool")
	}
	if !strings.Contains(feedback, "NOT executed") {
		t.Error("feedback should mention tool calls were NOT executed")
	}

	// Step 4: Build retry request
	original := &providers.ChatRequest{
		Model: "test-model",
		Messages: []providers.Message{
			{Role: "user", Content: "run ls -la"},
		},
		Tools: tools,
	}

	retryReq := BuildRetryRequest(original, feedback)

	// Step 5: Verify retry request
	if len(retryReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(retryReq.Messages))
	}
	if retryReq.Messages[0].Role != "system" {
		t.Errorf("first message should be system, got %s", retryReq.Messages[0].Role)
	}
	if !strings.Contains(string(retryReq.Messages[0].Content), "NOT executed") {
		t.Errorf("first message should contain feedback, got %s", retryReq.Messages[0].Content)
	}
}

// TestE2E_NoToolsNoValidation tests that requests without tools skip validation
func TestE2E_NoToolsNoValidation(t *testing.T) {
	// When no tools are provided, validation should be skipped
	calls := []providers.ToolCall{
		{
			Function: providers.ToolCallFunction{
				Name:      "Bash",
				Arguments: `{"command":"ls"}`,
			},
		},
	}

	errors := ValidateToolCallsWithErrors(calls, nil)
	if len(errors) > 0 {
		t.Errorf("expected no validation errors when no tools provided, got %d", len(errors))
	}
}

// TestE2E_ValidToolCallNoFeedback tests that valid tool calls don't generate feedback
func TestE2E_ValidToolCallNoFeedback(t *testing.T) {
	tools := []providers.Tool{
		{
			Type: "function",
			Function: providers.ToolFunction{
				Name: "Bash",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	calls := []providers.ToolCall{
		{
			Function: providers.ToolCallFunction{
				Name:      "Bash",
				Arguments: `{"command":"ls -la"}`,
			},
		},
	}

	errors := ValidateToolCallsWithErrors(calls, tools)
	if len(errors) > 0 {
		t.Errorf("expected no validation errors for valid tool call, got %d", len(errors))
	}

	// No feedback should be generated
	feedback := GenerateToolCallErrorFeedback(errors)
	if feedback != "" {
		t.Errorf("expected empty feedback for valid tool calls, got %s", feedback)
	}
}
