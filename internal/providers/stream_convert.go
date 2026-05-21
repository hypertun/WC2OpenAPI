package providers

import (
	"context"
	"fmt"
	"time"
)

// EmitCompletionAsStream converts a completed ChatResponse into a StreamResponse channel.
// Used by providers that only support request-response (not native streaming) but need to
// present results in the streaming interface. The response is emitted as a series of chunks:
// 1. Role chunk (role: "assistant")
// 2. Reasoning chunk (if reasoning is present)
// 3. Content chunk (if content is present)
// 4. Tool calls chunk (if tool calls are present)
// 5. Finish chunk (with finish_reason)
//
// This pattern is used by MiMo and StepFun when their responses are too large to fit in
// a single request and must be split, then converted back to streaming format.
func EmitCompletionAsStream(ctx context.Context, resp *ChatResponse, model string) <-chan StreamResponse {
	outChan := make(chan StreamResponse, 10)

	go func() {
		defer close(outChan)

		msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()

		// Emit role
		outChan <- StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			}},
		}

		// Emit content
		if len(resp.Choices) > 0 {
			content := string(resp.Choices[0].Message.Content)
			reasoning := resp.Choices[0].Message.ReasoningContent

			if reasoning != "" {
				outChan <- StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: Delta{ReasoningContent: reasoning},
					}},
				}
			}

			if content != "" {
				outChan <- StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: Delta{Content: content},
					}},
				}
			}

			// Emit tool calls if present
			if len(resp.Choices[0].Message.ToolCalls) > 0 {
				outChan <- StreamResponse{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: Delta{ToolCalls: resp.Choices[0].Message.ToolCalls},
					}},
				}
			}
		}

		// Emit finish
		finishReason := "stop"
		if len(resp.Choices) > 0 && len(resp.Choices[0].Message.ToolCalls) > 0 {
			finishReason = "tool_calls"
		}
		outChan <- StreamResponse{
			ID:      msgID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []StreamChoice{{
				Index:        0,
				Delta:        Delta{},
				FinishReason: &finishReason,
			}},
		}
	}()

	return outChan
}
