package toolcall

import (
	"encoding/json"
	"testing"
)

func TestParseToolCalls(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "No tool calls",
			input:    "Hello, world!",
			expected: 0,
		},
		{
			name: "Single tool call",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[ls -la]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 1,
		},
		{
			name: "Multiple tool calls",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git status]]></|DSML|parameter>
</|DSML|invoke>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git diff]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := ParseToolCalls(tt.input)
			if len(calls) != tt.expected {
				t.Errorf("expected %d calls, got %d", tt.expected, len(calls))
			}
			if tt.expected > 0 && len(calls) > 0 {
				// Verify first call has name and input
				if calls[0].Name == "" {
					t.Error("expected non-empty tool name")
				}
				if calls[0].Input == nil {
					t.Error("expected non-nil input map")
				}
				// Check that input can be marshaled to JSON
				if _, err := json.Marshal(calls[0].Input); err != nil {
					t.Errorf("failed to marshal input: %v", err)
				}
			}
		})
	}
}

func TestBuildToolCallInstructions(t *testing.T) {
	toolNames := []string{"bash", "read_file", "write_file"}
	instructions := BuildToolCallInstructions(toolNames)

	if instructions == "" {
		t.Error("expected non-empty instructions")
	}
	if !contains(instructions, "<|DSML|tool_calls|>") {
		t.Error("expected DSML tool_calls tag in instructions")
	}
	if !contains(instructions, "bash") {
		t.Error("expected tool name in instructions")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsString(s, substr))
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
