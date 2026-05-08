package toolcall

import (
	"strings"
	"testing"

	"github.com/user/wc2api/internal/providers"
)

func TestBuildParameterHints(t *testing.T) {
	tests := []struct {
		name     string
		schema   map[string]interface{}
		expected string
	}{
		{
			name:     "nil schema",
			schema:   nil,
			expected: "",
		},
		{
			name:     "no properties",
			schema:   map[string]interface{}{"type": "object"},
			expected: "",
		},
		{
			name: "single required param",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"file_path"},
			},
			expected: "file_path: string (required)",
		},
		{
			name: "mixed required and optional",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{"type": "string"},
					"content":   map[string]interface{}{"type": "string"},
					"mode":      map[string]interface{}{"type": "number"},
				},
				"required": []interface{}{"file_path", "content"},
			},
			expected: "content: string (required), file_path: string (required), mode: number (optional)",
		},
		{
			name: "no type specified",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"data": map[string]interface{}{},
				},
			},
			expected: "data: any (optional)",
		},
		{
			name: "array type",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{"type": "array"},
				},
				"required": []interface{}{"items"},
			},
			expected: "items: array (required)",
		},
		{
			name: "empty properties",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []interface{}{},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildParameterHints(tt.schema)
			if got != tt.expected && !(tt.expected == "" && got == "") {
				if got != tt.expected {
					t.Errorf("buildParameterHints() = %q, want %q", got, tt.expected)
				}
			}
		})
	}
}

func TestToolDescriptionWithHints(t *testing.T) {
	tool := makeTool("Read", "reads file contents",
		map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}},
		[]interface{}{"file_path"})

	desc := toolDescriptionWithHints(tool)

	if !strings.Contains(desc, "Read") {
		t.Error("expected tool name")
	}
	if !strings.Contains(desc, "file_path: string (required)") {
		t.Error("expected parameter hint")
	}
	if !strings.Contains(desc, "reads file contents") {
		t.Error("expected description")
	}
}

func TestBuildToolCallInstructions_NoTools(t *testing.T) {
	instructions := BuildToolCallInstructions(nil)
	if !strings.Contains(instructions, "TOOL CALL FORMAT") {
		t.Error("expected format instructions even with no tools")
	}
}

func TestBuildQwenToolCallInstructions_NoTools(t *testing.T) {
	instructions := BuildQwenToolCallInstructions(nil)
	if !strings.Contains(instructions, "ACTION MARKER PROTOCOL") {
		t.Error("expected protocol instructions even with no tools")
	}
}

func TestBuildToolCallInstructions_Content(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Read", "", map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}}, []interface{}{"file_path"}),
		makeTool("Write", "", map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}, "content": map[string]interface{}{"type": "string"}}, []interface{}{"file_path", "content"}),
	}

	instructions := BuildToolCallInstructions(tools)

	sections := []string{
		"TOOL CALL FORMAT",
		"AVAILABLE TOOLS",
		"CORRECT EXAMPLE",
		"RULES",
		"COMMON MISTAKES",
		"file_path: string (required)",
		"content: string (required)",
	}
	for _, s := range sections {
		if !strings.Contains(instructions, s) {
			t.Errorf("expected instructions to contain %q", s)
		}
	}
}

func TestBuildQwenToolCallInstructions_Content(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Bash", "run shell command", map[string]interface{}{"command": map[string]interface{}{"type": "string"}}, []interface{}{"command"}),
	}

	instructions := BuildQwenToolCallInstructions(tools)

	sections := []string{
		"ACTION MARKER PROTOCOL",
		"AVAILABLE ACTIONS",
		"CORRECT EXAMPLE",
		"STRICT RULES",
		"VALIDATION CHECKLIST",
		"COMMON MISTAKES",
		"command: string (required)",
	}
	for _, s := range sections {
		if !strings.Contains(instructions, s) {
			t.Errorf("expected instructions to contain %q", s)
		}
	}
}

func TestBuildQwenToolCallInstructions_ValidationChecklist(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Read", "", map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}}, []interface{}{"file_path"}),
	}

	instructions := BuildQwenToolCallInstructions(tools)

	checklistItems := []string{
		"required parameters are present",
		"Parameter names match",
		"Parameter types are correct",
		"No extra parameters",
	}
	for _, item := range checklistItems {
		if !strings.Contains(instructions, item) {
			t.Errorf("expected checklist to contain %q", item)
		}
	}
}

func TestBuildQwenToolCallInstructions_NegativeExamples(t *testing.T) {
	tools := []providers.Tool{
		makeTool("Read", "", map[string]interface{}{"file_path": map[string]interface{}{"type": "string"}}, []interface{}{"file_path"}),
	}

	instructions := BuildQwenToolCallInstructions(tools)

	negatives := []string{"Wrong:", "not"}
	for _, n := range negatives {
		if !strings.Contains(instructions, n) {
			t.Errorf("expected instructions to contain negative example marker %q", n)
		}
	}
}
