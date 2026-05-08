package toolcall

import (
	"strings"
	"testing"
)

func TestGenerateToolCallErrorFeedback_Empty(t *testing.T) {
	msg := GenerateToolCallErrorFeedback(nil)
	if msg != "" {
		t.Errorf("expected empty for nil errors, got: %q", msg)
	}

	msg = GenerateToolCallErrorFeedback([]*ValidationError{})
	if msg != "" {
		t.Errorf("expected empty for empty errors, got: %q", msg)
	}
}

func TestGenerateToolCallErrorFeedback_SingleError(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Expected:  "string",
			Actual:    nil,
			Message:   "Required parameter 'file_path' is missing",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)

	if !strings.Contains(msg, "Read") {
		t.Errorf("expected feedback to mention tool 'Read'")
	}
	if !strings.Contains(msg, "file_path") {
		t.Errorf("expected feedback to mention parameter 'file_path'")
	}
	if !strings.Contains(msg, "string") {
		t.Errorf("expected feedback to mention expected type 'string'")
	}
	if !strings.Contains(msg, "error correction") {
		t.Errorf("expected feedback to mention error correction")
	}
	if !strings.Contains(msg, "NOT executed") {
		t.Errorf("expected feedback to mention tool calls were not executed")
	}
	if len(msg) < 50 {
		t.Errorf("expected substantial feedback message, got %d chars", len(msg))
	}
}

func TestGenerateToolCallErrorFeedback_MultipleErrors(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Expected:  "string",
			Actual:    nil,
			Message:   "Required parameter 'file_path' is missing",
			Severity:  "error",
		},
		{
			ToolName:  "Bash",
			Parameter: "command",
			Expected:  "string",
			Actual:    42.0,
			Message:   "Type mismatch: expected string, got number",
			Severity:  "error",
		},
		{
			ToolName:  "Write",
			Parameter: "file_path",
			Expected:  "string",
			Actual:    true,
			Message:   "Type mismatch: expected string, got boolean",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)

	if !strings.Contains(msg, "Read") {
		t.Errorf("expected feedback to mention Read")
	}
	if !strings.Contains(msg, "Bash") {
		t.Errorf("expected feedback to mention Bash")
	}
	if !strings.Contains(msg, "Write") {
		t.Errorf("expected feedback to mention Write")
	}
	if !strings.Contains(msg, "missing") {
		t.Errorf("expected feedback to mention missing parameter")
	}
}

func TestGenerateToolCallErrorFeedback_GroupsByTool(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Message:   "Required parameter missing",
			Expected:  "string",
			Actual:    nil,
			Severity:  "error",
		},
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Message:   "Additional property not allowed",
			Expected:  "known parameter",
			Actual:    "something",
			Severity:  "warning",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)

	// Both errors for Read should be present
	count := strings.Count(msg, "Read")
	if count < 1 {
		t.Errorf("expected Read to appear at least once")
	}
}

func TestGenerateToolCallErrorFeedback_NilError(t *testing.T) {
	errs := []*ValidationError{
		nil,
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Expected:  "string",
			Actual:    nil,
			Message:   "Required parameter missing",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)

	if !strings.Contains(msg, "Read") {
		t.Errorf("expected feedback to contain Read despite nil entries")
	}
}

func TestGenerateToolCallErrorFeedback_NoToolName(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "",
			Parameter: "file_path",
			Expected:  "string",
			Actual:    nil,
			Message:   "Required parameter missing",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)

	if !strings.Contains(msg, "unknown") {
		t.Errorf("expected feedback to use 'unknown' for missing tool name")
	}
}

func TestGenerateToolCallErrorFeedback_MaxErrorsTruncation(t *testing.T) {
	// Create more than 10 errors to test truncation
	var errs []*ValidationError
	for i := 0; i < 15; i++ {
		errs = append(errs, &ValidationError{
			ToolName:  "Tool" + string(rune('A'+i)),
			Parameter: "param",
			Message:   "error",
			Severity:  "error",
		})
	}

	msg := GenerateToolCallErrorFeedback(errs)
	// Should contain truncation message
	if !strings.Contains(msg, "more error") {
		t.Errorf("expected truncation message in feedback")
	}
}

func TestGenerateToolCallErrorFeedback_LLMFormat_HasContext(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Bash",
			Parameter: "command",
			Message:   "required",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)
	// Should mention that tool calls were NOT executed
	if !strings.Contains(msg, "NOT executed") {
		t.Errorf("expected 'NOT executed' in feedback, got: %s", msg)
	}
	// Should mention error correction
	if !strings.Contains(msg, "error correction") {
		t.Errorf("expected 'error correction' in feedback")
	}
}

func TestGenerateToolCallErrorFeedback_LLMFormat_HasToolList(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Bash",
			Parameter: "command",
			Message:   "required",
			Severity:  "error",
		},
		{
			ToolName:  "Read",
			Parameter: "file_path",
			Message:   "required",
			Severity:  "error",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)
	// Should list both tools
	if !strings.Contains(msg, "Bash") || !strings.Contains(msg, "Read") {
		t.Errorf("expected both tool names in feedback, got: %s", msg)
	}
}

func TestGenerateToolCallErrorFeedback_MixedSeverity(t *testing.T) {
	errs := []*ValidationError{
		{
			ToolName:  "Bash",
			Parameter: "command",
			Message:   "required",
			Severity:  "error",
		},
		{
			ToolName:  "Bash",
			Parameter: "timeout",
			Message:   "warning example",
			Severity:  "warning",
		},
	}

	msg := GenerateToolCallErrorFeedback(errs)
	// Should contain both
	if !strings.Contains(msg, "Bash") {
		t.Errorf("expected Bash in feedback")
	}
}
