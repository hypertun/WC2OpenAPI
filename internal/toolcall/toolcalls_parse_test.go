package toolcall

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/user/wc2api/internal/providers"
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
		{
			name: "Tool call with arguments wrapper",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="arguments"><![CDATA[{"command":"git push origin main","description":"Push to origin main"}]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 1,
		},
		{
			name: "Truncated wrapper - missing closing tag",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[ls -la]]></|DSML|parameter>
</|DSML|invoke>`,
			expected: 1,
		},
		{
			name: "Fallback extraction - single quotes",
			input: `<|DSML|tool_calls>
<|DSML|invoke name='bash'>
<|DSML|parameter name='command'><![CDATA[ls -la]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 1,
		},
		{
			name: "Fallback extraction - unquoted attributes",
			input: `<|DSML|tool_calls>
<|DSML|invoke name=bash>
<|DSML|parameter name=command><![CDATA[ls -la]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 1,
		},
		{
			name: "Fallback extraction - mixed malformed",
			input: `<|DSML|tool_calls>
<|DSML|invoke name='bash' type='shell'>
<|DSML|parameter name="command"><![CDATA[pwd]]></|DSML|parameter>
</|DSML|invoke>
<|DSML|invoke name=git>
<|DSML|parameter name='command'><![CDATA[git status]]></|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`,
			expected: 2,
		},
		{
			name: "Chunk split - partial invoke without closing tag",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[ls -la]]></|DSML|parameter>`,
			expected: 1,
		},
		{
			name: "Chunk split - complete invoke followed by partial",
			input: `<|DSML|tool_calls>
<|DSML|invoke name="bash">
<|DSML|parameter name="command"><![CDATA[git status]]></|DSML|parameter>
</|DSML|invoke>
<|DSML|invoke name="git">
<|DSML|parameter name="command"><![CDATA[git log]]></|DSML|parameter>`,
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
				
				// Special check for arguments wrapper case
				if tt.name == "Tool call with arguments wrapper" {
					// Verify that the arguments were unwrapped
					if _, hasArgsKey := calls[0].Input["arguments"]; hasArgsKey {
						t.Error("arguments should have been unwrapped, found 'arguments' key in input")
					}
					if calls[0].Input["command"] != "git push origin main" {
						t.Errorf("expected command='git push origin main', got %v", calls[0].Input["command"])
					}
					if calls[0].Input["description"] != "Push to origin main" {
						t.Errorf("expected description='Push to origin main', got %v", calls[0].Input["description"])
					}
				}
			}
		})
	}
}

func makeTool(name, desc string, props map[string]interface{}, req []interface{}) providers.Tool {
	return providers.Tool{
		Type: "function",
		Function: providers.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": props,
				"required":   req,
			},
		},
	}
}

func TestBuildMarkerPrompt(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Read", "read a file",
			map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}},
			[]interface{}{"file_path"}),
	}
	instructions := BuildMarkerPrompt(tools, false)

	if instructions == "" {
		t.Error("expected non-empty instructions")
	}
	if !strings.Contains(instructions, "##TOOL_CALL##") {
		t.Error("expected TOOL_CALL marker")
	}
	if !strings.Contains(instructions, "Read") {
		t.Error("expected tool name in instructions")
	}
	if !strings.Contains(instructions, "file_path") {
		t.Error("expected parameter name in instructions")
	}
	if !strings.Contains(instructions, "required") {
		t.Error("expected required marker in instructions")
	}
	if !strings.Contains(instructions, "VALIDATION CHECKLIST") {
		t.Errorf("expected VALIDATION CHECKLIST in instructions, got:\n%s", instructions)
	}
	if strings.Contains(instructions, "you may also output XML") {
		t.Error("expected no flexibility note when includeFlexibilityNote=false")
	}
}

func TestBuildMarkerPrompt_WithFlexibilityNote(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Read", "read a file",
			map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}},
			[]interface{}{"file_path"}),
	}
	instructions := BuildMarkerPrompt(tools, true)

	if !strings.Contains(instructions, "you may also output XML") {
		t.Error("expected flexibility note when includeFlexibilityNote=true")
	}
}


