package mimo

import (
	"context"
	"fmt"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// CreateChatCompletion creates a non-streaming chat completion.
// Buffers all SSE events and returns a single response.
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	query, thinking := buildQuery(req)
	model := req.Model

	chatReq := c.buildChatRequest(query, thinking, model)
	eventChan, err := c.streamChat(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("mimo: chat: %w", err)
	}

	var contentBuf strings.Builder
	var usage providers.Usage

	for event := range eventChan {
		switch event.Type {
		case "text":
			contentBuf.WriteString(event.Content)
		default:
			if event.PromptTokens > 0 || event.CompletionTokens > 0 {
				usage = providers.Usage{
					PromptTokens:     event.PromptTokens,
					CompletionTokens: event.CompletionTokens,
					TotalTokens:      event.TotalTokens,
				}
				if usage.TotalTokens == 0 {
					usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				}
			}
		}
	}

	fullContent := contentBuf.String()
	fullContent = strings.ReplaceAll(fullContent, "\x00", "")
	content, thinkingContent := splitThinkTags(fullContent)

	// Extract tool calls if tools were provided
	var toolCalls []providers.ToolCall
	if len(req.Tools) > 0 {
		toolNames := getToolNames(req.Tools)
		tc, cleaned := extractToolCall(content, toolNames)
		if tc != nil {
			toolCalls = tc
			content = cleaned
		}
	}

	created := time.Now().Unix()
	resp := &providers.ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []providers.Choice{
			{
				Index: 0,
				Message: providers.Message{
					Role: "assistant",
				},
				FinishReason: "stop",
			},
		},
		Usage: usage,
	}

	if len(toolCalls) > 0 {
		resp.Choices[0].Message.ToolCalls = toolCalls
		resp.Choices[0].FinishReason = "tool_calls"
	} else {
		combined := content
		if thinkingContent != "" {
			combined = thinkOpen + thinkingContent + thinkClose + "\n" + content
		}
		resp.Choices[0].Message.Content = providers.MessageContent(combined)
		resp.Choices[0].Message.ReasoningContent = thinkingContent
	}

	return resp, nil
}

// CreateChatCompletionStream creates a streaming chat completion.
// Pipes SSE events from MiMo through to the OpenAI streaming format.
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	query, thinking := buildQuery(req)
	model := req.Model

	chatReq := c.buildChatRequest(query, thinking, model)
	eventChan, err := c.streamChat(ctx, chatReq)
	if err != nil {
		return nil, fmt.Errorf("mimo: stream chat: %w", err)
	}

	outChan := make(chan providers.StreamResponse, 50)

	go func() {
		defer close(outChan)

		msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()
		hasTools := len(req.Tools) > 0
		var toolNames []string
		if hasTools {
			toolNames = getToolNames(req.Tools)
		}

		// Send initial role delta
		outChan <- providers.StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []providers.StreamChoice{{
				Index: 0,
				Delta: providers.Delta{Role: "assistant"},
			}},
		}

		var buffer strings.Builder
		inThink := false
		var contentBuilder strings.Builder
		var collectedToolCalls []providers.ToolCall

		for event := range eventChan {
			switch event.Type {
			case "text":
				chunk := strings.ReplaceAll(event.Content, "\x00", "")
				contentBuilder.WriteString(chunk)
				buffer.WriteString(chunk)
			default:
				// usage events are ignored for streaming
			}

			processBuffer(&buffer, &inThink, msgID, model, created, hasTools, outChan)
		}

		// If tools were provided, extract tool calls from full response
		if hasTools {
			fullContent := contentBuilder.String()
			tc, cleaned := extractToolCall(fullContent, toolNames)
			if tc != nil {
				collectedToolCalls = tc
				// Emit reasoning + content before tool calls
				c, reasoning := splitThinkTags(cleaned)
				if reasoning != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{ReasoningContent: reasoning}}},
					}
				}
				if c != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Content: c}}},
					}
				}
			} else {
				// No tool calls found — stream the content with think/reasoning split
				content, reasoning := splitThinkTags(cleaned)
				if reasoning != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{ReasoningContent: reasoning}}},
					}
				}
				if content != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Content: content}}},
					}
				}
			}
		} else {
			// No tools — stream remaining buffer directly
			buf := buffer.String()
			if buf != "" {
				content, reasoning := splitThinkTags(buf)
				if reasoning != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{ReasoningContent: reasoning}}},
					}
				}
				if content != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Content: content}}},
					}
				}
			}
		}

		// Send final chunk
		finishReason := "stop"
		if len(collectedToolCalls) > 0 {
			finishReason = "tool_calls"
			outChan <- providers.StreamResponse{
				ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []providers.StreamChoice{{
					Index: 0,
					Delta: providers.Delta{ToolCalls: collectedToolCalls},
				}},
			}
		}

		outChan <- providers.StreamResponse{
			ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []providers.StreamChoice{{
				Index:        0,
				Delta:        providers.Delta{},
				FinishReason: &finishReason,
			}},
		}
	}()

	return outChan, nil
}

// processBuffer handles think tag splitting and streaming output.
// When tools are active, content is buffered for tool call detection at the end.
func processBuffer(
	buffer *strings.Builder,
	inThink *bool,
	msgID, model string,
	created int64,
	hasTools bool,
	outChan chan<- providers.StreamResponse,
) {
	buf := buffer.String()
	if buf == "" {
		return
	}

	if hasTools {
		return
	}

	// Without tools, stream content with think tag splitting
	buffer.Reset()

	for {
		if *inThink {
			idx := strings.Index(buf, thinkClose)
			if idx >= 0 {
				thinkContent := buf[:idx]
				if strings.TrimSpace(thinkContent) != "" {
					outChan <- providers.StreamResponse{
						ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
						Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{ReasoningContent: thinkContent}}},
					}
				}
				buf = buf[idx+len(thinkClose):]
				*inThink = false
				continue
			}
			// No closing tag yet — hold the buffer
			buffer.WriteString(buf)
			return
		}

		idx := strings.Index(buf, thinkOpen)
		if idx >= 0 {
			// Emit content before think tag
			before := buf[:idx]
			if strings.TrimSpace(before) != "" {
				outChan <- providers.StreamResponse{
					ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Content: before}}},
				}
			}
			buf = buf[idx+len(thinkOpen):]
			*inThink = true
			continue
		}

		// No think tag — emit remaining content
		if strings.TrimSpace(buf) != "" {
			outChan <- providers.StreamResponse{
				ID: msgID, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []providers.StreamChoice{{Index: 0, Delta: providers.Delta{Content: buf}}},
			}
		}
		return
	}
}

// buildQuery constructs the MiMo query string from the chat request.
// Messages are concatenated with role prefixes (system:/user:/assistant:/tool:).
// Tool definitions are injected into the system message.
func buildQuery(req *providers.ChatRequest) (query string, thinking bool) {
	thinking = false
	var parts []string
	var systemText string

	for _, msg := range req.Messages {
		content := string(msg.Content)

		switch msg.Role {
		case "system":
			systemText = content
			continue
		case "user":
			parts = append(parts, "user: "+content)
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var tcLines []string
				for _, tc := range msg.ToolCalls {
					tcLines = append(tcLines, fmt.Sprintf("TOOL_CALL: %s(%s)", tc.Function.Name, tc.Function.Arguments))
				}
				if content != "" {
					parts = append(parts, "assistant: "+content+"\n"+strings.Join(tcLines, "\n"))
				} else {
					parts = append(parts, "assistant: "+strings.Join(tcLines, "\n"))
				}
			} else {
				parts = append(parts, "assistant: "+content)
			}
		case "tool":
			clean := strings.TrimPrefix(content, "[TOOL_RESULT]")
			parts = append(parts, "user: [tool_result id="+msg.ToolCallID+"] "+clean)
		}
	}

	// Inject tool definitions into system prompt
	if len(req.Tools) > 0 {
		toolPrompt := buildToolPrompt(req.Tools)
		if toolPrompt != "" {
			if systemText != "" {
				systemText = systemText + "\n\n" + toolPrompt
			} else {
				systemText = toolPrompt
			}
		}
	}

	// Prepend system message if present
	if systemText != "" {
		parts = append([]string{"system: " + systemText}, parts...)
	}

	query = strings.Join(parts, "\n")
	if query == "" {
		query = "Hello"
	}

	return query, thinking
}

// splitThinkTags extracts <think>...</think> blocks from the response.
func splitThinkTags(text string) (content, thinkingContent string) {
	start := strings.Index(text, thinkOpen)
	if start == -1 {
		return text, ""
	}

	end := strings.Index(text, thinkClose)
	if end == -1 {
		return text, ""
	}

	thinkingContent = text[start+len(thinkOpen) : end]
	content = text[:start] + text[end+len(thinkClose):]
	return content, thinkingContent
}
