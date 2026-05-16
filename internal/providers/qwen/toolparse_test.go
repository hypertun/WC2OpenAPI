package qwen

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/user/wc2api/internal/providers"
	testutil "github.com/user/wc2api/internal/testutil"
)

func TestParseToolCallsMultiple(t *testing.T) {
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL## ##TOOL_CALL##{"name":"Bash","input":{"command":"ls"}}##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "calculator" {
		t.Errorf("expected first call name 'calculator', got %s", calls[0].Function.Name)
	}
	if calls[1].Function.Name != "Bash" {
		t.Errorf("expected second call name 'Bash', got %s", calls[1].Function.Name)
	}
}

func TestParseToolCallsSingleLine(t *testing.T) {
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "calculator" {
		t.Errorf("expected name 'calculator', got %s", calls[0].Function.Name)
	}
}

func TestParseToolCallsMultiline(t *testing.T) {
	text := `##TOOL_CALL##
{"name":"calculator","input":{"expr":"2+2"}}
##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoLeading(t *testing.T) {
	text := `TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoTrailing(t *testing.T) {
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoSpace(t *testing.T) {
	text := `##END CALL##{"name":"calculator","input":{"expr":"2+2"}}##TOOL_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsNoToolCalls(t *testing.T) {
	text := "Just a regular response without tool calls"

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if calls != nil && len(calls) > 0 {
		t.Errorf("expected no tool calls, got %d", len(calls))
	}
}

func TestParseToolCallsEmpty(t *testing.T) {
	calls, parseErrors := parseToolCallsFromText("", nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}
	if calls != nil {
		t.Errorf("expected nil calls for empty input, got %v", calls)
	}
}

func TestValidateSingleToolCall_LogsResults(t *testing.T) {
	captor := &testutil.LogCaptor{}
	logger := slog.New(captor)

	tools := []providers.Tool{
		{Type: "function", Function: providers.ToolFunction{
			Name: "calculator",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"expr": map[string]interface{}{"type": "string"},
				},
			},
		}},
	}

	// Temporarily set the default logger to capture logs
	oldLogger := slog.Default()
	slog.SetDefault(logger)
	// validateSingleToolCall is not exported, so we test via parseToolCallsFromText
	// which calls validation internally
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":123}}##END_CALL##`
	parseToolCallsFromText(text, tools)
	slog.SetDefault(oldLogger)

	// Should log validation result
	records := captor.Records()
	found := false
	for _, r := range records {
		if strings.Contains(r.Message, "Validation result") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Validation result' log message")
	}
}

func TestApplyToolSpecificFixes_CaseInsensitiveRead(t *testing.T) {
	// Test that lowercase "read" tool name maps file_path to filePath
	input := map[string]interface{}{
		"file_path": "/path/to/file",
	}

	result := applyToolSpecificFixes("read", input)

	// Should have filePath, not file_path
	if _, hasFilePath := result["filePath"]; !hasFilePath {
		t.Errorf("expected 'filePath' in result, got keys: %v", result)
	}
	if _, hasFilePath := result["file_path"]; hasFilePath {
		t.Errorf("expected 'file_path' to be removed, but it's still present")
	}
	if result["filePath"] != "/path/to/file" {
		t.Errorf("expected filePath=/path/to/file, got %v", result["filePath"])
	}
}

func TestApplyToolSpecificFixes_CaseInsensitiveBash(t *testing.T) {
	// Test that lowercase "bash" tool name still works
	input := map[string]interface{}{
		"cmd": "ls -la",
	}

	result := applyToolSpecificFixes("bash", input)

	if _, hasCommand := result["command"]; !hasCommand {
		t.Errorf("expected 'command' in result for bash tool")
	}
	if _, hasCmd := result["cmd"]; hasCmd {
		t.Errorf("expected 'cmd' to be removed after mapping to 'command'")
	}
	if result["command"] != "ls -la" {
		t.Errorf("expected command=ls -la, got %v", result["command"])
	}
	if result["timeout"] != float64(30000) {
		t.Errorf("expected default timeout 30000, got %v", result["timeout"])
	}
}

func TestParseToolCallMarkers_QwenObfuscatedRead(t *testing.T) {
	// Simulate the exact issue: Qwen returns u_read with file_path param
	// which should be mapped to filePath for OpenCode's read tool
	text := `##TOOL_CALL##
{"name": "u_read", "input": {"file_path": "/Users/ivanyeo/Progects/WC2OpenAPI/TOOLCALL_GAPS.md"}}
##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}

	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
		return
	}

	call := calls[0]

	// Tool name should be de-obfuscated from u_read to read
	if call.Function.Name != "read" {
		t.Errorf("expected tool name 'read', got %s", call.Function.Name)
	}

	// Parse the arguments to check the parameter transformation
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("failed to unmarshal arguments: %v", err)
	}

	// The parameter should be filePath (not file_path)
	// because applyToolSpecificFixes maps file_path -> filePath for "read" tool
	if filePath, ok := args["filePath"]; !ok {
		t.Errorf("expected 'filePath' in arguments, got keys: %v", args)
	} else if filePath != "/Users/ivanyeo/Progects/WC2OpenAPI/TOOLCALL_GAPS.md" {
		t.Errorf("expected filePath=/Users/ivanyeo/Progects/WC2OpenAPI/TOOLCALL_GAPS.md, got %v", filePath)
	}

	// The old file_path key should be removed
	if _, hasFilePath := args["file_path"]; hasFilePath {
		t.Errorf("expected 'file_path' to be removed after mapping to 'filePath'")
	}
}

func TestParseToolCallMarkers_WriteWithFilePathToFilePath(t *testing.T) {
	// Test that write tool also maps file_path -> filePath
	text := `##TOOL_CALL##
{"name": "fs_put_file", "input": {"file_path": "/tmp/test.txt", "content": "hello"}}
##END_CALL##`

	calls, parseErrors := parseToolCallsFromText(text, nil)
	if len(parseErrors) > 0 {
		t.Fatalf("parseToolCallsFromText errors: %v", parseErrors)
	}

	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
		return
	}

	call := calls[0]

	// fs_put_file should de-obfuscate to Write (capital W, from explicit alias reverse lookup)
	if call.Function.Name != "Write" {
		t.Errorf("expected tool name 'Write', got %s", call.Function.Name)
	}

	// Check arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("failed to unmarshal arguments: %v", err)
	}

	// Both filePath and content should be present
	if _, ok := args["filePath"]; !ok {
		t.Errorf("expected 'filePath' in arguments, got keys: %v", args)
	}
	if _, ok := args["content"]; !ok {
		t.Errorf("expected 'content' in arguments, got keys: %v", args)
	}
}
