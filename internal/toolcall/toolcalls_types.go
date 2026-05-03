package toolcall

// ParsedToolCall represents a parsed tool call
type ParsedToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}
