package qwen

import (
	"testing"
)

func TestParseToolCallsFromText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		wantName string
	}{
		{
			name:     "##TOOL_CALL## markers",
			input:    "Some text\n##TOOL_CALL##\n{\"name\": \"Read\", \"input\": {\"file_path\": \"/test.txt\"}}\n##END_CALL##\nMore text",
			wantLen:  1,
			wantName: "Read",
		},
		{
			name:     "native tool_call phase format",
			input:    "{\"type\":\"tool_use\",\"id\":\"tc_1\",\"name\":\"Bash\",\"input\":{\"command\":\"ls\"}}",
			wantLen:  1,
			wantName: "Bash",
		},
		{
			name:    "no tool calls",
			input:   "Just plain text without any tool calls",
			wantLen: 0,
		},
		{
			name:     "fragmented - missing markers",
			input:    "Some text before\n{\"name\": \"Write\", \"input\": {\"file_path\": \"/test.txt\", \"content\": \"hello\"}}\nSome text after",
			wantLen:  1,
			wantName: "Write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, err := parseToolCallsFromText(tt.input)
			if err != nil {
				t.Errorf("parseToolCallsFromText() error = %v", err)
				return
			}
			if len(calls) != tt.wantLen {
				t.Errorf("parseToolCallsFromText() got %d calls, want %d", len(calls), tt.wantLen)
				return
			}
			if tt.wantLen > 0 && calls[0].Function.Name != tt.wantName {
				t.Errorf("parseToolCallsFromText() got name %q, want %q", calls[0].Function.Name, tt.wantName)
			}
		})
	}
}

func TestNormalizeFragmentedToolCall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already has markers",
			input: "##TOOL_CALL##\n{\"name\": \"test\"}\n##END_CALL##",
			want:  "##TOOL_CALL##\n{\"name\": \"test\"}\n##END_CALL##",
		},
		{
			name:  "fix END_CALL marker",
			input: "{\"name\": \"test\", \"input\": {}}\nEND_CALL##",
			want:  "##TOOL_CALL##\n{\"name\": \"test\", \"input\": {}}\n##END_CALL##",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeFragmentedToolCall(tt.input)
			if got != tt.want {
				t.Errorf("normalizeFragmentedToolCall() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFixToolCallArguments(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		wantKey  string
		wantVal  interface{}
	}{
		{
			name:     "Read path to file_path",
			toolName: "Read",
			input:    map[string]interface{}{"path": "/test.txt"},
			wantKey:  "file_path",
			wantVal:  "/test.txt",
		},
		{
			name:     "Bash cmd to command",
			toolName: "Bash",
			input:    map[string]interface{}{"cmd": "ls -la"},
			wantKey:  "command",
			wantVal:  "ls -la",
		},
		{
			name:     "AskUserQuestion question to questions",
			toolName: "AskUserQuestion",
			input:    map[string]interface{}{"question": "Continue?"},
			wantKey:  "questions",
			wantVal:  []interface{}{}, // questions is converted to an array
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fixToolCallArguments(tt.toolName, tt.input)
			if tt.wantKey == "questions" {
				// questions should be an array
				if _, ok := got[tt.wantKey].([]interface{}); !ok {
					t.Errorf("fixToolCallArguments()[%q] should be []interface{}, got %T", tt.wantKey, got[tt.wantKey])
				}
			} else if got[tt.wantKey] != tt.wantVal {
				t.Errorf("fixToolCallArguments()[%q] = %v, want %v", tt.wantKey, got[tt.wantKey], tt.wantVal)
			}
		})
	}
}