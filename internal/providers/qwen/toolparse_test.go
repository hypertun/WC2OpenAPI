package qwen

import (
	"testing"
)

func TestParseToolCallsMultiple(t *testing.T) {
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL## ##TOOL_CALL##{"name":"Bash","input":{"command":"ls"}}##END_CALL##`

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
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

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
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

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoLeading(t *testing.T) {
	text := `TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL##`

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoTrailing(t *testing.T) {
	text := `##TOOL_CALL##{"name":"calculator","input":{"expr":"2+2"}}##END_CALL`

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsTypoSpace(t *testing.T) {
	text := `##END CALL##{"name":"calculator","input":{"expr":"2+2"}}##TOOL_CALL##`

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(calls))
	}
}

func TestParseToolCallsNoToolCalls(t *testing.T) {
	text := "Just a regular response without tool calls"

	calls, err := parseToolCallsFromText(text, nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if calls != nil && len(calls) > 0 {
		t.Errorf("expected no tool calls, got %d", len(calls))
	}
}

func TestParseToolCallsEmpty(t *testing.T) {
	calls, err := parseToolCallsFromText("", nil)
	if err != nil {
		t.Fatalf("parseToolCallsFromText error: %v", err)
	}
	if calls != nil {
		t.Errorf("expected nil calls for empty input, got %v", calls)
	}
}
