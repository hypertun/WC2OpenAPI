package toolcall

import (
	"strings"
)

// SieveEvent represents an emitted event from the streaming sieve.
type SieveEvent struct {
	Type string // "text" or "tool_calls"
	Data any    // string for text, []SieveToolCall for tool_calls
}

// SieveToolCall represents a tool call extracted during streaming.
type SieveToolCall struct {
	ID       string
	Type     string
	Function SieveToolCallFunction
}

// SieveToolCallFunction represents the function call details from the sieve.
type SieveToolCallFunction struct {
	Name      string
	Arguments string
}

// StreamSieve performs real-time character-by-character separation of
// normal text from tool call markup in a streaming response. It does NOT
// buffer the full response — text is emitted as soon as it is confirmed
// to not be part of a tool call prefix.
type StreamSieve struct {
	parseFn func(text string, toolNames []string) ([]SieveToolCall, string)

	pending    string // normal-mode buffer
	captureBuf string // capture-mode buffer
	capturing  bool
}

// NewStreamSieve creates a new StreamSieve.
func NewStreamSieve(
	parseFn func(text string, toolNames []string) ([]SieveToolCall, string),
) *StreamSieve {
	return &StreamSieve{parseFn: parseFn}
}

// Feed ingests a chunk of text and returns zero or more events.
func (s *StreamSieve) Feed(chunk string, toolNames []string) []SieveEvent {
	var events []SieveEvent

	if s.capturing {
		s.captureBuf += chunk
		result := s.tryFinishCapture(toolNames)
		if result != nil {
			if result.Prefix != "" {
				events = append(events, SieveEvent{Type: "text", Data: result.Prefix})
			}
			if len(result.Calls) > 0 {
				events = append(events, SieveEvent{Type: "tool_calls", Data: result.Calls})
			}
			if result.Suffix != "" {
				s.pending = result.Suffix
			}
			s.captureBuf = ""
			s.capturing = false
			if result.Suffix != "" {
				events = append(events, s.Feed("", toolNames)...)
			}
		}
		return events
	}

	s.pending += chunk
	startIdx := findToolStart(s.pending)

	if startIdx >= 0 {
		prefix := s.pending[:startIdx]
		rest := s.pending[startIdx:]
		s.pending = ""

		if prefix != "" {
			events = append(events, SieveEvent{Type: "text", Data: prefix})
		}

		s.captureBuf = rest
		s.capturing = true

		result := s.tryFinishCapture(toolNames)
		if result != nil {
			if result.Prefix != "" {
				events = append(events, SieveEvent{Type: "text", Data: result.Prefix})
			}
			if len(result.Calls) > 0 {
				events = append(events, SieveEvent{Type: "tool_calls", Data: result.Calls})
			}
			if result.Suffix != "" {
				s.pending = result.Suffix
			}
			s.captureBuf = ""
			s.capturing = false
		}
	} else {
		safe, hold := splitSafe(s.pending)
		if safe != "" {
			events = append(events, SieveEvent{Type: "text", Data: safe})
		}
		s.pending = hold
	}

	return events
}

// captureResult holds the typed result from tryFinishCapture.
type captureResult struct {
	Prefix string
	Calls  []SieveToolCall
	Suffix string
}

// Flush releases any remaining buffered content at stream end.
func (s *StreamSieve) Flush(toolNames []string) []SieveEvent {
	var events []SieveEvent

	if s.capturing {
		result := s.tryFinishCapture(toolNames)
		if result != nil {
			if result.Prefix != "" {
				events = append(events, SieveEvent{Type: "text", Data: result.Prefix})
			}
			if len(result.Calls) > 0 {
				events = append(events, SieveEvent{Type: "tool_calls", Data: result.Calls})
			}
			if result.Suffix != "" {
				events = append(events, SieveEvent{Type: "text", Data: result.Suffix})
			}
		} else {
			if s.captureBuf != "" {
				events = append(events, SieveEvent{Type: "text", Data: s.captureBuf})
			}
		}
		s.captureBuf = ""
		s.capturing = false
	}

	if s.pending != "" {
		events = append(events, SieveEvent{Type: "text", Data: s.pending})
		s.pending = ""
	}

	return events
}

// ── Internal ──────────────────────────────────────────────────────

func (s *StreamSieve) tryFinishCapture(toolNames []string) *captureResult {
	if s.captureBuf == "" || s.parseFn == nil {
		return nil
	}

	if !isCaptureComplete(s.captureBuf) {
		return nil
	}

	calls, cleaned := s.parseFn(s.captureBuf, toolNames)
	if calls == nil {
		return nil
	}

	prefix, suffix := extractNonToolParts(s.captureBuf)

	if len(calls) > 0 {
		return &captureResult{Prefix: prefix, Calls: calls, Suffix: suffix}
	}

	textToEmit := cleaned
	if cleaned == "" {
		textToEmit = s.captureBuf
	}
	return &captureResult{Prefix: textToEmit, Suffix: ""}
}

var toolStartMarkers = []string{
	"##TOOL_CALL##",
	"<|MiMoML|tool_calls>",
	"<｜MiMoML｜tool_calls>",
	"<|MiMoML|function_calls>",
	"<｜MiMoML｜function_calls>",
	"<|DSML|tool_calls>",
	"<｜DSML｜tool_calls>",
	"<|DSML|function_calls>",
	"<｜DSML｜function_calls>",
	"TOOL_CALL:",
	"<tool_call",
	"<function_call",
	"<function=",
	"[调用工具:",
	"<tool_calls>",
	"<function_calls>",
}

func findToolStart(text string) int {
	best := -1
	for _, tag := range toolStartMarkers {
		pos := strings.Index(text, tag)
		if pos >= 0 && (best < 0 || pos < best) {
			best = pos
		}
	}
	return best
}

func splitSafe(text string) (safe string, hold string) {
	if len(text) == 0 {
		return "", ""
	}

	startChars := map[byte]bool{
		'T': true, 't': true, '<': true, '[': true,
		'O': true, 'o': true, 'F': true, 'f': true,
		'C': true, 'c': true, '|': true, '#': true,
	}

	bestIdx := -1
	var bestHold string

	for i := len(text) - 1; i >= max(len(text)-25, 0); i-- {
		ch := text[i]
		if !startChars[ch] {
			continue
		}
		tail := text[i:]
		tailLower := strings.ToLower(tail)

		// Skip think tags
		isThink := false
		for _, tp := range []string{"<think", "<thinking", "<th", "<thi", "<thin", "</think", "</thinking"} {
			if strings.HasPrefix(tailLower, tp) {
				isThink = true
				break
			}
		}
		if isThink {
			continue
		}

		for _, tag := range toolStartMarkers {
			tagLower := strings.ToLower(tag)
			if strings.HasPrefix(tagLower, tailLower) || strings.HasPrefix(tailLower, tagLower) {
				// Prefer the lowest index (earliest in text = longest hold)
				if bestIdx < 0 || i < bestIdx {
					bestIdx = i
					bestHold = tail
				}
				break
			}
		}
	}

	if bestIdx >= 0 {
		return text[:bestIdx], bestHold
	}
	return text, ""
}

func isCaptureComplete(buf string) bool {
	trimmed := strings.TrimSpace(buf)

	// ##TOOL_CALL## marker format
	if strings.HasPrefix(trimmed, "##TOOL_CALL##") {
		return strings.Contains(buf, "##END_CALL##")
	}

	if strings.HasPrefix(strings.ToUpper(trimmed), "TOOL_CALL:") {
		return strings.Contains(buf, ")") || strings.Contains(buf, "\n")
	}

	if strings.Contains(buf, "[调用工具:") {
		return strings.Contains(buf, "\n") || strings.Contains(buf, "]")
	}

	if strings.HasPrefix(trimmed, "<") {
		if strings.Contains(buf, "<function=") {
			if strings.Contains(buf, "<tool_call") && !strings.Contains(buf, "</tool_call>") {
				return false
			}
			return strings.Contains(buf, "</function>")
		}
		if strings.Contains(buf, "<tool_call") && strings.Contains(buf, "</tool_call>") {
			return true
		}
		if strings.Contains(buf, "<function_call") && strings.Contains(buf, "</function_call>") {
			return true
		}
		if strings.Contains(buf, "<tool_calls>") && strings.Contains(buf, "</tool_calls>") {
			return true
		}
		if strings.Contains(buf, "<|MiMoML|tool_calls>") && strings.Contains(buf, "</|MiMoML|tool_calls>") {
			return true
		}
		if strings.Contains(buf, "<|DSML|tool_calls>") && strings.Contains(buf, "</|DSML|tool_calls>") {
			return true
		}
		return false
	}

	return false
}

func extractNonToolParts(text string) (prefix string, suffix string) {
	best := -1
	for _, tag := range toolStartMarkers {
		pos := strings.Index(text, tag)
		if pos >= 0 && (best < 0 || pos < best) {
			best = pos
		}
	}

	if best < 0 {
		return text, ""
	}

	prefix = text[:best]
	rest := text[best:]
	end := -1
	trimmed := strings.TrimSpace(rest)

	// Use if-else chain to avoid switch/case parsing issues with return
	if strings.HasPrefix(trimmed, "##TOOL_CALL##") {
		ec := strings.Index(text[best:], "##END_CALL##")
		if ec >= 0 {
			end = best + ec + len("##END_CALL##")
		}
	} else if strings.HasPrefix(strings.ToUpper(trimmed), "TOOL_CALL:") {
		nl := strings.Index(rest, "\n")
		if nl >= 0 {
			end = best + nl + 1
		}
	} else if strings.Contains(rest, "[调用工具:") {
		bracket := strings.Index(rest, "]")
		if bracket >= 0 {
			end = best + bracket + 1
		}
	} else if strings.Contains(rest, "<tool_call") {
		closePos := strings.Index(rest, "</tool_call>")
		if closePos >= 0 {
			end = best + closePos + len("</tool_call>")
		}
	} else if strings.Contains(rest, "<function=") {
		closePos := strings.Index(rest, "</function>")
		if closePos >= 0 {
			end = best + closePos + len("</function>")
		}
	} else if strings.Contains(rest, "<function_call") {
		closePos := strings.Index(rest, "</function_call>")
		if closePos >= 0 {
			end = best + closePos + len("</function_call>")
		}
	} else if strings.Contains(rest, "<tool_calls>") {
		closePos := strings.Index(rest, "</tool_calls>")
		if closePos >= 0 {
			end = best + closePos + len("</tool_calls>")
		}
	} else if strings.Contains(rest, "<|MiMoML|tool_calls>") {
		closePos := strings.Index(rest, "</|MiMoML|tool_calls>")
		if closePos >= 0 {
			end = best + closePos + len("</|MiMoML|tool_calls>")
		}
	} else if strings.Contains(rest, "<|DSML|tool_calls>") {
		closePos := strings.Index(rest, "</|DSML|tool_calls>")
		if closePos >= 0 {
			end = best + closePos + len("</|DSML|tool_calls>")
		}
	}

	if end < 0 {
		return prefix, ""
	}

	suffix = text[end:]
	return prefix, suffix
}
