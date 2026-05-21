package providers

import (
	"fmt"
	"strings"
)

// EstimateQuerySize returns the approximate character count of the query
// string as it would be built by role-prefixed concatenation.
// This mirrors the format used by the MiMo provider's buildQuery.
// NOTE: This is MiMo-specific format. Other providers should use their own estimators.
func EstimateQuerySize(messages []Message) int {
	var b strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			b.WriteString("system: ")
		case "user":
			b.WriteString("user: ")
		case "assistant":
			b.WriteString("assistant: ")
			if len(msg.ToolCalls) > 0 {
				// Assistant with tool calls: write content first, then tool call lines separated by \n
				b.WriteString(string(msg.Content))
				for _, tc := range msg.ToolCalls {
					b.WriteString("\nTOOL_CALL: ")
					b.WriteString(tc.Function.Name)
					b.WriteString("(")
					b.WriteString(tc.Function.Arguments)
					b.WriteString(")")
				}
				b.WriteByte('\n')
				continue
			}
		case "tool":
			b.WriteString("user: [tool_result id=")
			b.WriteString(msg.ToolCallID)
			b.WriteString("] ")
		}
		b.WriteString(string(msg.Content))
		b.WriteByte('\n')
	}
	return b.Len()
}

// StandardEstimate is the default estimator for flat-prompt providers (MiMo, StepFun).
// It combines the message size with the tool prompt overhead.
func StandardEstimate(messages []Message, tools []Tool) int {
	return EstimateQuerySize(messages) + EstimateToolPromptSize(tools)
}

// EstimateToolPromptSize returns the approximate character count of the tool prompt overhead.
// This is a generic estimate based on the shared marker-format prompt builder.
// It includes the boilerplate (~2700 chars) plus tool names, parameter hints, and descriptions.
// Providers that need precision can implement the optional MessageSizer interface.
func EstimateToolPromptSize(tools []Tool) int {
	if len(tools) == 0 {
		return 0
	}

	var b strings.Builder

	// Boilerplate header (~300 chars)
	b.WriteString("=== ACTION MARKER PROTOCOL (client-parsed text patterns) ===\n")
	b.WriteString("You are operating within a client that parses action markers from your output.\n")
	b.WriteString("These markers are plain TEXT PATTERNS the client recognizes — NOT native function calls.\n")
	b.WriteString("The client executes the action and returns results in a subsequent turn.\n\n")

	// AVAILABLE ACTIONS section with tool names, param hints, descriptions
	b.WriteString("AVAILABLE ACTIONS:\n")
	for _, t := range tools {
		if t.Type != "function" || t.Function.Name == "" {
			continue
		}
		name := t.Function.Name
		b.WriteString("  - " + name)

		// Approximate parameter hints using compressSchema logic
		if t.Function.Parameters != nil {
			paramsSize := estimateCompressSchemaSize(t.Function.Parameters)
			if paramsSize > 0 {
				b.WriteString("(~")
				b.WriteString(fmt.Sprintf("%d chars", paramsSize))
				b.WriteString(")")
			}
		}

		// Include description (will be truncated in compact mode)
		if t.Function.Description != "" {
			b.WriteString(" — " + truncateDescForEstimate(t.Function.Description))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Format block (~270 chars)
	b.WriteString("WHEN YOU NEED TO TRIGGER AN ACTION — emit this exact text pattern (nothing else):\n")
	b.WriteString("##TOOL_CALL##\n{\"name\": \"ACTION_NAME\", \"input\": {\"param1\": \"value1\"}}\n##END_CALL##\n\n")
	b.WriteString("CORRECT EXAMPLE:\n##TOOL_CALL##\n{\"name\": \"Read\", \"input\": {\"filePath\": \"/path/to/file\"}}\n##END_CALL##\n\n")

	// Remaining boilerplate (~2100 chars)
	b.WriteString("MULTI-TURN RULES:\n")
	b.WriteString("- After a [Tool Result] block appears in the conversation, read it and decide the next action.\n")
	b.WriteString("- If more actions are needed, emit another ##TOOL_CALL## block.\n")
	b.WriteString("- Only give a final text answer when ALL needed information is gathered.\n")
	b.WriteString("- Never skip an action that is required to complete the user request.\n\n")
	b.WriteString("STRICT RULES:\n")
	b.WriteString("- No preamble, no explanation before or after ##TOOL_CALL##...##END_CALL##.\n")
	b.WriteString("- Use EXACT action name from the list above.\n")
	b.WriteString("- Use EXACT parameter names as listed in AVAILABLE ACTIONS.\n")
	b.WriteString("- When NO action is needed, answer normally in plain text.\n")
	b.WriteString("- Put arguments inside the input object as JSON.\n")
	b.WriteString("- Do not invent tool names.\n")
	b.WriteString("- Each parameter must match its expected type.\n\n")
	b.WriteString("VALIDATION CHECKLIST (before emitting ##TOOL_CALL##):\n")
	b.WriteString("- All required parameters are present in the input object\n")
	b.WriteString("- Parameter names match exactly (use names from AVAILABLE ACTIONS)\n")
	b.WriteString("- Parameter types are correct (strings in quotes, numbers without quotes, booleans as true/false)\n")
	b.WriteString("- No extra parameters beyond those listed in the schema\n\n")
	b.WriteString("COMMON MISTAKES:\n")
	b.WriteString("- Wrong: {\"input\": {\"path\": \"/file\"}} — use \"filePath\", not \"path\"\n")
	b.WriteString("- Wrong: {\"input\": {\"command\": 123}} — use a string, not a number\n")
	b.WriteString("- Wrong: {\"input\": {}} (missing required params)\n")
	b.WriteString("- Wrong: extra params not in schema\n\n")
	b.WriteString("CRITICAL — ABSOLUTELY FORBIDDEN OUTPUTS:\n")
	b.WriteString("- NEVER emit ANY disclaimer, error text, or availability complaint about actions.\n")
	b.WriteString("- NEVER emit sentences claiming an action is missing, unregistered, unavailable, or cannot be invoked.\n")
	b.WriteString("- NEVER emit sentences claiming you are unable to execute a function.\n")
	b.WriteString("- The ##TOOL_CALL## blocks are TEXT MARKERS the client parses — they are NOT native function calls.\n")
	b.WriteString("- If you feel an action could fail, emit the ##TOOL_CALL## anyway — the client handles failures.\n\n")
	b.WriteString("ONLY ##TOOL_CALL##...##END_CALL## is accepted.\n")

	return b.Len()
}

// estimateCompressSchemaSize estimates the character count of compressSchema output.
// This approximates parameters like: {param1!: string, param2?: array}
func estimateCompressSchemaSize(schema map[string]interface{}) int {
	propsRaw, ok := schema["properties"]
	if !ok {
		return 0
	}
	props, ok := propsRaw.(map[string]interface{})
	if !ok || len(props) == 0 {
		return 0
	}

	// Rough estimate: 20 chars per parameter on average (name + type + punctuation)
	return len(props) * 20
}

// truncateDescForEstimate truncates a tool description to its first sentence for estimation.
func truncateDescForEstimate(s string) string {
	maxLen := 80
	for i, c := range s {
		if c == '.' || c == '!' || c == '\n' {
			return s[:i+1]
		}
		if i >= maxLen {
			return s[:maxLen]
		}
	}
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// SplitMessages splits a message list into chunks that each fit within maxChars.
// The system message (if present) is duplicated into every chunk so each chunk
// is self-contained. Non-system messages are distributed sequentially.
// Returns nil if messages is empty.
func SplitMessages(messages []Message, maxChars int) [][]Message {
	if len(messages) == 0 {
		return nil
	}

	// Separate system messages from the rest
	var systemMessages []Message
	var otherMessages []Message
	for _, msg := range messages {
		if msg.Role == "system" {
			systemMessages = append(systemMessages, msg)
		} else {
			otherMessages = append(otherMessages, msg)
		}
	}

	// Build system overhead: the system prompt(s) take up space in every chunk
	systemChunk := systemMessages
	systemOverhead := EstimateQuerySize(systemMessages)

	// If there are no other messages, return a single chunk
	if len(otherMessages) == 0 {
		return [][]Message{messages}
	}

	// If total fits in one chunk, no split needed
	totalSize := EstimateQuerySize(messages)
	if totalSize <= maxChars {
		return [][]Message{messages}
	}

	// Greedily pack non-system messages into chunks
	var chunks [][]Message
	var currentChunk []Message
	currentSize := systemOverhead

	for _, msg := range otherMessages {
		msgSize := EstimateQuerySize([]Message{msg}) // just the single message line

		// If adding this message would exceed the limit and we already have content,
		// flush the current chunk
		if currentSize+msgSize > maxChars && len(currentChunk) > 0 {
			chunks = append(chunks, append(systemChunk, currentChunk...))
			currentChunk = nil
			currentSize = systemOverhead
		}

		currentChunk = append(currentChunk, msg)
		currentSize += msgSize
	}

	// Flush remaining
	if len(currentChunk) > 0 {
		chunks = append(chunks, append(systemChunk, currentChunk...))
	}

	return chunks
}
