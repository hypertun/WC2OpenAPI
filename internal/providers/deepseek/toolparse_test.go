package deepseek

import (
	"encoding/json"
	"testing"
)

func TestParseToolCallsFromText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int // number of tool calls expected
		hasErr   bool
	}{
		{
			name:     "No tool calls",
			text:     "Just a regular response without any tool calls.",
			expected: 0,
		},
		{
			name: "Single tool call with CDATA",
			text: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git status]]></|DSML|parameter>
<|DSML|parameter name="description"><![CDATA[Show status]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 1,
		},
		{
			name: "Multiple tool calls",
			text: `<|DSML|tool_calls>
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
			calls, err := parseToolCallsFromText(tt.text)
			if tt.hasErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.hasErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(calls) != tt.expected {
				t.Errorf("expected %d tool calls, got %d", tt.expected, len(calls))
			}
			// Verify each call has proper structure
			for i, call := range calls {
				if call.Function.Name == "" {
					t.Errorf("tool call %d: expected non-empty name", i)
				}
				if call.Function.Arguments == "" {
					t.Errorf("tool call %d: expected non-empty arguments", i)
				}
				// Verify arguments are valid JSON
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					t.Errorf("tool call %d: invalid JSON arguments: %v", i, err)
				}
			}
		})
	}
}

func TestParseToolCallsFromText_UserExample(t *testing.T) {
	userDSML := `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git status]]></|DSML|parameter>
<|DSML|parameter name="description"><![CDATA[Shows working tree status]]></|DSML|parameter>
</|DSML|invoke>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git diff]]></|DSML|parameter>
<|DSML|parameter name="description"><![CDATA[Shows unstaged changes]]></|DSML|parameter>
</|DSML|invoke>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git log --oneline -10]]></|DSML|parameter>
<|DSML|parameter name="description"><![CDATA[Shows recent commit history]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`

	toolCalls, err := parseToolCallsFromText(userDSML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolCalls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(toolCalls))
	}

	expectedCommands := []string{"git status", "git diff", "git log --oneline -10"}
	for i, tc := range toolCalls {
		if tc.Function.Name != "bash" {
			t.Errorf("tool call %d: expected name 'bash', got '%s'", i, tc.Function.Name)
		}
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			t.Errorf("tool call %d: failed to parse arguments JSON: %v", i, err)
			continue
		}
		if cmd, ok := args["command"].(string); !ok || cmd != expectedCommands[i] {
			t.Errorf("tool call %d: expected command '%s', got '%v'", i, expectedCommands[i], args["command"])
		}
	}
}
