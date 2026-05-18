package toolcall

import (
	"strings"
	"testing"
)

func sieveParseFn(text string, toolNames []string) ([]SieveToolCall, string) {
	// ParseAllToolCalls returns []SieveToolCall directly
	calls, cleaned := ParseAllToolCalls(text, toolNames)
	return calls, cleaned
}

func TestStreamSieve_PlainText(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	events := s.Feed("Hello world, no tools here.", []string{"Read"})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "text" {
		t.Fatalf("expected text event, got %s", events[0].Type)
	}
	if events[0].Data != "Hello world, no tools here." {
		t.Fatalf("unexpected data: %v", events[0].Data)
	}
}

func TestStreamSieve_MarkerInSingleChunk(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	input := `Some text.##TOOL_CALL##{"name":"Read","input":{"file_path":"/tmp/x"}}##END_CALL##`
	events := s.Feed(input, []string{"Read"})

	foundText := false
	foundTool := false
	for _, e := range events {
		if e.Type == "text" {
			foundText = true
		}
		if e.Type == "tool_calls" {
			foundTool = true
			calls := e.Data.([]SieveToolCall)
			if len(calls) != 1 || calls[0].Function.Name != "Read" {
				t.Fatalf("unexpected tool call: %v", calls)
			}
		}
	}
	if !foundText {
		t.Error("expected text event")
	}
	if !foundTool {
		t.Error("expected tool_calls event")
	}
}

func TestStreamSieve_MarkerSplitAcrossChunks(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	toolNames := []string{"Read"}

	// Feed text up to the marker
	events := s.Feed("Prefix text. ##TOOL_", toolNames)
	// Should emit "Prefix text. " as text, hold "##TOOL_" as pending
	for _, e := range events {
		if e.Type == "text" && e.Data != "Prefix text. " {
			t.Fatalf("expected 'Prefix text. ', got %q", e.Data)
		}
	}

	// Feed rest of marker
	events = s.Feed("CALL##\n{\"name\": \"Read\", \"input\": {\"file_path\": \"/tmp/x\"}}\n##END_CALL##", toolNames)
	foundTool := false
	for _, e := range events {
		if e.Type == "tool_calls" {
			foundTool = true
			calls := e.Data.([]SieveToolCall)
			if len(calls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(calls))
			}
		}
	}
	if !foundTool {
		t.Error("expected tool_calls event after split chunk")
	}
}

func TestStreamSieve_XMLSplitAcrossChunks(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	toolNames := []string{"Read"}

	events := s.Feed("Text before <|DSML|tool_calls>\n", toolNames)
	foundToolText := false
	for _, e := range events {
		if e.Type == "text" && strings.Contains(e.Data.(string), "Text before") {
			foundToolText = true
		}
	}
	if !foundToolText {
		t.Error("expected text before XML marker")
	}

	events = s.Feed("  <|DSML|invoke name=\"Read\">\n    <|DSML|parameter name=\"file_path\"><![CDATA[/tmp/x]]></|DSML|parameter>\n  </|DSML|invoke>\n</|DSML|tool_calls>", toolNames)
	foundTool := false
	for _, e := range events {
		if e.Type == "tool_calls" {
			foundTool = true
			calls := e.Data.([]SieveToolCall)
			if len(calls) != 1 || calls[0].Function.Name != "Read" {
				t.Fatalf("unexpected: %v", calls)
			}
		}
	}
	if !foundTool {
		t.Error("expected tool_calls from XML")
	}
}

func TestStreamSieve_FlushReleasesBuffer(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	s.Feed("Incomplete data without tool call", []string{"Read"})
	events := s.Flush([]string{"Read"})
	// Plain text with no tool marker is emitted immediately by Feed,
	// so Flush may have nothing left. Either 0 or 1 events is fine.
	if len(events) > 1 {
		t.Fatalf("expected 0-1 flush events, got %d", len(events))
	}
	if len(events) == 1 && events[0].Type != "text" {
		t.Fatalf("expected text, got %s", events[0].Type)
	}
}

func TestStreamSieve_TOOL_CALLFormat(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	input := "Let me read that file. TOOL_CALL: Read(file_path=\"/tmp/test.txt\")"
	events := s.Feed(input, []string{"Read"})

	foundTool := false
	for _, e := range events {
		if e.Type == "tool_calls" {
			foundTool = true
			calls := e.Data.([]SieveToolCall)
			if len(calls) != 1 || calls[0].Function.Name != "Read" {
				t.Fatalf("unexpected: %v", calls)
			}
		}
	}
	if !foundTool {
		t.Error("expected tool_calls from TOOL_CALL: format")
	}
}

func TestStreamSieve_MultipleChunks(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	toolNames := []string{"Read"}

	chunks := []string{
		"First chunk. ",
		"Second chunk. ",
		"##TOOL_CALL##",
		`{"name":"Read","input":{"file_path":"/x"}}`,
		"##END_CALL##",
		" Final text.",
	}

	var allEvents []SieveEvent
	for _, chunk := range chunks {
		allEvents = append(allEvents, s.Feed(chunk, toolNames)...)
	}
	allEvents = append(allEvents, s.Flush(toolNames)...)

	textCount := 0
	toolCount := 0
	for _, e := range allEvents {
		switch e.Type {
		case "text":
			textCount++
		case "tool_calls":
			toolCount++
		}
	}

	if textCount == 0 {
		t.Error("expected some text events")
	}
	if toolCount != 1 {
		t.Errorf("expected 1 tool_calls event, got %d", toolCount)
	}
}

func TestStreamSieve_ThinkTagNotConfused(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	input := "<think>I should read the file</think>Let me do that."
	events := s.Feed(input, []string{"Read"})

	// Should all be text, no false tool detection
	for _, e := range events {
		if e.Type == "tool_calls" {
			t.Error("should not detect tool calls inside think tags")
		}
	}
}

func TestStreamSieve_EmptyInput(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	events := s.Feed("", []string{"Read"})
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestStreamSieve_TOOL_CALLWithParens(t *testing.T) {
	s := NewStreamSieve(sieveParseFn)
	input := `TOOL_CALL: Bash(command="echo \"hello(world)\"")`
	events := s.Feed(input, []string{"Bash"})

	foundTool := false
	for _, e := range events {
		if e.Type == "tool_calls" {
			foundTool = true
			calls := e.Data.([]SieveToolCall)
			if len(calls) != 1 || calls[0].Function.Name != "Bash" {
				t.Fatalf("unexpected: %v", calls)
			}
		}
	}
	if !foundTool {
		t.Error("expected tool_calls with nested parens")
	}
}
