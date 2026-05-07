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

	var body string
	if len(matches) >= 2 {
		body = matches[1]
	} else {
		// D-2b: Partial wrapper recovery - try to extract content from incomplete wrapper
		body = extractPartialWrapperContent(normalized)
		if body == "" {
			return nil
		}
	}

	// Extract invoke blocks
	invokePattern := regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"[^>]*>(.*?)</invoke>`)
	invokeMatches := invokePattern.FindAllStringSubmatch(body, -1)

	// D-2c: Try to recover partial invoke blocks (split across chunks, missing closing tag)
	invokeMatches = append(invokeMatches, extractPartialInvokeBlocks(body)...)

	// D-2d: Fallback regex extraction if standard pattern fails
	if len(invokeMatches) == 0 {
		invokeMatches = extractInvokeBlocksFallback(body)
		if len(invokeMatches) == 0 {
			return nil
		}
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

	// Check for special case: single "arguments" parameter containing JSON object
	// This handles when model wraps all args in {"arguments": {...}} instead of individual params
	if len(matches) == 1 && matches[0][1] == "arguments" {
		paramValue := matches[0][2]

		// Strip CDATA if present (]]> not ]]>)
		paramValue = strings.TrimSpace(paramValue)
		if strings.HasPrefix(paramValue, "<![CDATA[") && strings.HasSuffix(paramValue, "]]>") {
			paramValue = paramValue[9 : len(paramValue)-3] // Remove <![CDATA[ and ]]>
		}

		// Try to parse as JSON object - if successful, return it directly (unwrap)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(paramValue), &parsed); err == nil {
			return parsed
		}
	}

	// Standard multi-parameter parsing
	for _, match := range matches {
		paramName := match[1]
		paramValue := match[2]

		// Strip CDATA if present (]]> not ]]>)
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

// extractPartialWrapperContent recovers invoke blocks from incomplete tool_calls wrappers
// This handles cases where the closing </tool_calls> tag is missing (e.g., truncated in streaming)
func extractPartialWrapperContent(normalized string) string {
	trimmed := strings.TrimSpace(normalized)

	// Check if it starts with <tool_calls but has no closing tag
	hasOpening := strings.HasPrefix(trimmed, "<tool_calls") ||
		strings.Contains(trimmed, "<tool_calls")
	hasClosing := strings.Contains(trimmed, "</tool_calls>")

	if hasOpening && !hasClosing {
		// Extract content after <tool_calls> opening tag
		idx := strings.Index(trimmed, "<tool_calls")
		if idx >= 0 {
			// Find the end of the opening tag
			tagStart := trimmed[idx:]
			tagEnd := strings.Index(tagStart, ">")
			if tagEnd >= 0 {
				return trimmed[idx+tagEnd+1:]
			}
		}
	}

	// Also check for standalone invoke blocks without any wrapper
	if strings.Contains(trimmed, "<invoke") {
		return trimmed
	}

	return ""
}

// extractPartialInvokeBlocks recovers invoke blocks that may be truncated or split across chunks
// This handles cases like <invoke name="bash">... (missing </invoke>) which can happen in streaming
// For complete blocks, returns empty slice to avoid duplication with main pattern
func extractPartialInvokeBlocks(body string) [][]string {
	var results [][]string

	// Count how many closing tags we expect vs have
	openCount := strings.Count(body, "<invoke")
	closeCount := strings.Count(body, "</invoke>")

	// If all blocks are complete, don't duplicate
	if openCount <= closeCount {
		return results
	}

	// There are more opens than closes, find the incomplete ones
	// Scan through the body to find invoke blocks without matching closes
	idx := 0
	for idx < len(body) {
		// Find next invoke opening
		openIdx := strings.Index(body[idx:], "<invoke")
		if openIdx == -1 {
			break
		}
		openIdx += idx

		// Find the closing > of the opening tag
		tagEnd := strings.Index(body[openIdx:], ">")
		if tagEnd == -1 {
			break
		}
		tagEnd += openIdx + 1

		// Extract the opening tag to get the name
		openTag := body[openIdx:tagEnd]
		namePattern := regexp.MustCompile(`name="([^"]+)"`)
		nameMatch := namePattern.FindStringSubmatch(openTag)
		if len(nameMatch) < 2 {
			idx = tagEnd
			continue
		}
		name := nameMatch[1]

		// Look for the closing </invoke> tag after this opening
		remaining := body[tagEnd:]
		closeIdx := strings.Index(remaining, "</invoke>")

		if closeIdx == -1 {
			// No closing tag found - this is a partial block
			// Extract content until end of string or next opening tag
			content := remaining
			nextOpen := strings.Index(remaining, "<invoke")
			if nextOpen > 0 {
				content = remaining[:nextOpen]
			}
			content = strings.TrimSpace(content)

			if len(content) > 0 {
				results = append(results, []string{
					body[openIdx : tagEnd+len(content)], // Full match
					name,                               // Group 1: name
					content,                            // Group 2: content
				})
			}
			idx = len(body) // Done processing
		} else {
			// This block has a closing tag - let the main pattern handle it
			idx = tagEnd + closeIdx + 10 // Move past </invoke>
		}
	}

	return results
}

// extractInvokeBlocksFallback provides regex-based fallback extraction for malformed invoke blocks
// This handles cases where xml.Unmarshal or strict regex would fail due to minor malformations
func extractInvokeBlocksFallback(body string) [][]string {
	var results [][]string

	// Tolerant pattern: <invoke name="..." or name='...' or name=...
	// Captures name and everything up to </invoke> or end of block
	invokePattern := regexp.MustCompile(`(?s)<invoke\s+name=["']?([^"'>\s]+)["']?[^>]*>(.*?)</invoke>`)
	matches := invokePattern.FindAllStringSubmatch(body, -1)

	for _, match := range matches {
		if len(match) >= 3 {
			results = append(results, match)
		}
	}

	return results
}

func normalizeDSML(text string) string {
	result := text
	// Convert DSML tags to standard XML
	result = strings.ReplaceAll(result, "<|DSML|", "<")
	result = strings.ReplaceAll(result, "</|DSML|", "</")
	result = strings.ReplaceAll(result, "|>", ">")
	result = strings.ReplaceAll(result, "| >", ">") // spacing variant

	// Accept single quotes in XML attributes: name='tool' → name="tool"
	reSingleQuote := regexp.MustCompile(`(\w+)='([^']+)'`)
	result = reSingleQuote.ReplaceAllString(result, `${1}="${2}"`)

	// Accept backticks: name=`tool` → name="tool"
	reBacktick := regexp.MustCompile(`(\w+)=` + "`([^`]+)`")
	result = reBacktick.ReplaceAllString(result, `${1}="${2}"`)

	// Accept unquoted attribute values: name=tool → name="tool" (simple identifiers)
	// Note: Go regexp doesn't support negative lookahead, so we use word boundary approach
	// Match unquoted value followed by space, /, or > and preserve the delimiter
	reUnquoted := regexp.MustCompile(`(\w+)=([a-zA-Z0-9_]+)([\s/>])`)
	result = reUnquoted.ReplaceAllString(result, `${1}="${2}"${3}`)
	// Handle case at end of string (no trailing character)
	reUnquotedEnd := regexp.MustCompile(`(\w+)=([a-zA-Z0-9_]+)$`)
	result = reUnquotedEnd.ReplaceAllString(result, `${1}="${2}"`)

	return result
}
