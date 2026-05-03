package toolcall

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ParseToolCalls parses tool calls from text containing DSML markup
func ParseToolCalls(text string) []ParsedToolCall {
	if text == "" {
		return nil
	}

	// Normalize DSML tags
	normalized := normalizeDSML(text)

	// Extract tool_calls blocks
	wrapperPattern := regexp.MustCompile(`(?s)<tool_calls[^>]*>(.*?)</tool_calls>`)
	matches := wrapperPattern.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return nil
	}

	body := matches[1]

	// Extract invoke blocks
	invokePattern := regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"[^>]*>(.*?)</invoke>`)
	invokeMatches := invokePattern.FindAllStringSubmatch(body, -1)

	if len(invokeMatches) == 0 {
		return nil
	}

	var calls []ParsedToolCall
	for _, match := range invokeMatches {
		name := match[1]
		invokeBody := match[2]

		input := parseParameters(invokeBody)

		calls = append(calls, ParsedToolCall{
			Name:  name,
			Input: input,
		})
	}

	return calls
}

func parseParameters(body string) map[string]any {
	params := make(map[string]any)

	// Match parameter tags
	paramPattern := regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"[^>]*>(.*?)</parameter>`)
	matches := paramPattern.FindAllStringSubmatch(body, -1)

	for _, match := range matches {
		paramName := match[1]
		paramValue := match[2]

		// Strip CDATA if present
		paramValue = strings.TrimSpace(paramValue)
		if strings.HasPrefix(paramValue, "<![CDATA[") && strings.HasSuffix(paramValue, "]]>") {
			paramValue = paramValue[9 : len(paramValue)-3] // Remove <![CDATA[ and ]]>
		}

		// Try to parse as JSON, otherwise keep as string
		var parsed any
		if err := json.Unmarshal([]byte(paramValue), &parsed); err != nil {
			params[paramName] = paramValue
		} else {
			params[paramName] = parsed
		}
	}

	return params
}

func normalizeDSML(text string) string {
	result := text
	// Convert DSML tags to standard XML
	result = strings.ReplaceAll(result, "<|DSML|", "<")
	result = strings.ReplaceAll(result, "</|DSML|", "</")
	result = strings.ReplaceAll(result, "|>", ">")
	return result
}
