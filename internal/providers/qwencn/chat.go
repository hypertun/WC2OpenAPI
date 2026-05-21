package qwencn

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

// CreateChatCompletion creates a chat completion with Qwen CN
func (c *Client) CreateChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	return c.chatWithRetry(ctx, req, 0)
}

// chatWithRetry performs non-streaming chat with tool call validation and retry
func (c *Client) chatWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	// Pre-check: if message size exceeds limit, split proactively
	querySize := qwencnEstimate(req.Messages, req.Tools)
	maxChars := c.maxQueryChars()
	if querySize > maxChars {
		slog.Info("QwenCN: proactively splitting long message",
			"query_size", querySize,
			"max_chars", maxChars,
		)
		return providers.SplitAndSend(ctx, c.sendDirect, req, maxChars, qwencnEstimate)
	}

	return c.sendDirect(ctx, req, retryCount)
}

// sendDirect performs a single chat completion without the size-check guard.
// This is the raw send path used by SplitAndSend to avoid infinite recursion:
// chatWithRetry → SplitAndSend → sendDirect (stops here, no re-check).
// It still handles tool-call validation errors with retry.
func (c *Client) sendDirect(ctx context.Context, req *providers.ChatRequest, retryCount int) (*providers.ChatResponse, error) {
	resp, err := c.doChat(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read all response data
	body, err := decompressStream(resp)
	if err != nil {
		return nil, fmt.Errorf("decompress failed: %w", err)
	}

	// Parse SSE events
	content, reasoning, toolCalls := parseSSE(body)

	// If no native tool calls found, try engine-level parse on combined text
	// This catches ##TOOL_CALL## markers that may have come through the reasoning stream
	if len(toolCalls) == 0 && len(req.Tools) > 0 {
		combined := content
		if reasoning != "" {
			combined = content + "\n" + reasoning
		}
		tc, cleaned, _ := c.toolEngine.Parse(combined, req.Tools)
		if len(tc) > 0 {
			toolCalls = tc
			content = cleaned
			reasoning = ""
		}
	}

	// Validate tool calls if tools are provided
	if len(toolCalls) > 0 && len(req.Tools) > 0 {
		validationErrors := c.toolEngine.Validate(toolCalls, req.Tools)
		if c.toolEngine.ShouldRetry(validationErrors, retryCount) {
			feedback := c.toolEngine.GenerateErrorFeedback(validationErrors)
			backoff := c.toolEngine.CalculateBackoff(retryCount)

			slog.Info("QwenCN: retrying non-streaming with error feedback",
				"retry", retryCount+1, "errors", len(validationErrors),
				"backoff_ms", backoff.Milliseconds())

			time.Sleep(backoff)
			retryReq := toolcall.BuildRetryRequest(req, feedback)
			return c.sendDirect(ctx, retryReq, retryCount+1)
		}
	}

	chatResp := &providers.ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []providers.Choice{
			{
				Index: 0,
				Message: providers.Message{
					Role: "assistant",
				},
				FinishReason: "stop",
			},
		},
		Usage: providers.Usage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0},
	}

	if len(toolCalls) > 0 {
		chatResp.Choices[0].Message.ToolCalls = toolCalls
		chatResp.Choices[0].FinishReason = "tool_calls"
	} else {
		chatResp.Choices[0].Message.Content = providers.MessageContent(content)
		if reasoning != "" {
			chatResp.Choices[0].Message.ReasoningContent = reasoning
		}
	}

	return chatResp, nil
}

// CreateChatCompletionStream creates a streaming chat completion with Qwen CN
func (c *Client) CreateChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (<-chan providers.StreamResponse, error) {
	return c.streamWithRetry(ctx, req, 0)
}

// streamWithRetry performs streaming chat with tool call buffering, validation, and retry
func (c *Client) streamWithRetry(ctx context.Context, req *providers.ChatRequest, retryCount int) (<-chan providers.StreamResponse, error) {
	resp, err := c.doChat(ctx, req)
	if err != nil {
		return nil, err
	}

	outChan := make(chan providers.StreamResponse, 20)

	go func() {
		defer close(outChan)
		defer resp.Body.Close()

		msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()

		// Create streaming decompression reader from response body
		reader, err := newStreamReader(resp.Body, resp.Header.Get("Content-Encoding"))
		if err != nil {
			slog.Error("QwenCN stream decompression init error", "error", err)
			return
		}
		if closer, ok := reader.(io.Closer); ok && reader != resp.Body {
			defer closer.Close()
		}

		// Create adapter for real-time streaming with tool call separation.
		adapter := toolcall.NewStreamSieveAdapter(c.toolEngine, req.Tools, msgID, req.Model, created)
		if len(req.Tools) > 0 {
			adapter.WithRetry(ctx, func(ctx2 context.Context, req2 *providers.ChatRequest, rc int) (<-chan providers.StreamResponse, error) {
				return c.streamWithRetry(ctx2, req2, rc)
			}, req, retryCount)
		}

		var (
			lastContentLen  int
			lastThinkingLen int
		)

		scanner := bufio.NewScanner(reader)
		scanBuf := make([]byte, 0, 64*1024)
		scanner.Buffer(scanBuf, 256*1024)
		scanner.Split(scanDoubleNewline)

		for scanner.Scan() {
			block := scanner.Text()
			if block == "" {
				continue
			}

			eventType, eventData := parseSSEBlock(block)

			if eventData == "" || eventData == "[DONE]" {
				if eventType == "complete" {
					break
				}
				continue
			}

			content, thinking, _, parseErr := parseSSEEvent(eventData)
			if parseErr != nil {
				continue
			}

			// Feed thinking deltas (always streamed immediately via adapter).
			if thinking != "" && len(thinking) > lastThinkingLen {
				chunk := thinking[lastThinkingLen:]
				lastThinkingLen = len(thinking)
				if strings.TrimSpace(chunk) != "" {
					for _, c := range adapter.FeedThinking(chunk) {
						outChan <- c
					}
				}
			}

			// Feed content deltas (streamed immediately; tool markers captured).
			if content != "" && len(content) > lastContentLen {
				chunk := content[lastContentLen:]
				lastContentLen = len(content)
				if strings.TrimSpace(chunk) != "" {
					for _, c := range adapter.FeedText(chunk) {
						outChan <- c
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			slog.Error("QwenCN stream scanner error", "error", err)
		}

		// Flush adapter: handles parsing, validation, optional retry, and final chunks.
		for _, chunk := range adapter.Flush() {
			outChan <- chunk
		}
	}()

	return outChan, nil
}

// doChat builds and sends the chat request, returning the HTTP response
func (c *Client) doChat(ctx context.Context, req *providers.ChatRequest) (*http.Response, error) {
	model := c.mapModel(req.Model)
	sessionID := uuid()
	reqID := uuid()

	// Inject tools into messages if provided
	messages := req.Messages
	if len(req.Tools) > 0 {
		messages = c.toolEngine.InjectTools(req.Messages, req.Tools, req.ToolChoice)
	}

	// Build messages
	var systemPrompt string
	var conversationParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemPrompt = string(msg.Content)
		case "user":
			conversationParts = append(conversationParts, extractTextContent(msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var tcs []string
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, fmt.Sprintf(
						"##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##",
						c.toolEngine.ObfuscateName(tc.Function.Name), tc.Function.Arguments,
					))
				}
				content := extractTextContent(msg.Content)
				if content != "" {
					conversationParts = append(conversationParts, "Assistant: "+content+"\n"+strings.Join(tcs, "\n"))
				} else {
					conversationParts = append(conversationParts, "Assistant: "+strings.Join(tcs, "\n"))
				}
			} else {
				conversationParts = append(conversationParts, "Assistant: "+extractTextContent(msg.Content))
			}
		case "tool":
			conversationParts = append(conversationParts, "Tool Result: "+extractTextContent(msg.Content))
		}
	}

	userContent := strings.Join(conversationParts, "\n\n")

	finalContent := userContent
	if systemPrompt != "" {
		finalContent = systemPrompt + "\n\nUser: " + userContent
	}

	payload := map[string]interface{}{
		"deep_search": "1",
		"req_id":      reqID,
		"model":       model,
		"scene":       "chat",
		"session_id":  sessionID,
		"sub_scene":   "",
		"temporary":   true,
		"messages": []map[string]interface{}{
			{
				"content":   finalContent,
				"mime_type": "text/plain",
				"meta_data": map[string]interface{}{
					"ori_query": finalContent,
				},
			},
		},
		"from":             "default",
		"parent_req_id":    "0",
		"enable_search":    false,
		"messages_merge":   false,
		"scene_param":      "first_turn",
		"chat_client":      "h5",
		"protocol_version": "v2",
		"biz_id":           "ai_qwen",
	}

	return c.doRequest(ctx, payload, reqID)
}

// decompressStream reads response body and decompresses based on content-encoding
func decompressStream(resp *http.Response) ([]byte, error) {
	var reader io.ReadCloser
	var err error

	contentEncoding := strings.ToLower(resp.Header.Get("Content-Encoding"))

	switch contentEncoding {
	case "gzip":
		reader, err = gzip.NewReader(resp.Body)
	case "deflate":
		reader = flate.NewReader(resp.Body)
	case "br":
		reader = io.NopCloser(brotli.NewReader(resp.Body))
	case "zstd":
		zr, err := zstd.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("zstd reader: %w", err)
		}
		defer zr.Close()
		return io.ReadAll(zr)
	case "":
		return io.ReadAll(resp.Body)
	default:
		return nil, fmt.Errorf("unsupported content-encoding: %s", contentEncoding)
	}

	if err != nil {
		return nil, fmt.Errorf("decompression init: %w", err)
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

// zstdReadCloser wraps *zstd.Decoder to implement io.ReadCloser (Close returns nil error).
type zstdReadCloser struct {
	*zstd.Decoder
}

func (z *zstdReadCloser) Close() error {
	z.Decoder.Close()
	return nil
}

// newStreamReader wraps resp.Body in a streaming decompression reader based on Content-Encoding.
// The returned reader must be closed when done (closes only the decompressor, not the underlying body).
func newStreamReader(body io.ReadCloser, encoding string) (io.ReadCloser, error) {
	switch strings.ToLower(encoding) {
	case "gzip":
		return gzip.NewReader(body)
	case "deflate":
		return flate.NewReader(body), nil
	case "br":
		return io.NopCloser(brotli.NewReader(body)), nil
	case "zstd":
		zr, err := zstd.NewReader(body)
		if err != nil {
			return nil, err
		}
		return &zstdReadCloser{zr}, nil
	case "":
		return body, nil
	default:
		return nil, fmt.Errorf("unsupported content-encoding: %s", encoding)
	}
}

// scanDoubleNewline is a bufio split function for SSE events (delimited by \n\n)
func scanDoubleNewline(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		return i + 2, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseSSEBlock extracts event type and data from an SSE block
func parseSSEBlock(block string) (eventType, data string) {
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
		}
	}
	return
}

// sseEvent represents a parsed SSE event from Qwen CN
type sseEvent struct {
	Communication *sseCommunication `json:"communication,omitempty"`
	Data          *sseEventData     `json:"data,omitempty"`
	ErrorCode     int               `json:"error_code,omitempty"`
	ErrorMsg      string            `json:"error_msg,omitempty"`
}

type sseCommunication struct {
	SessionID string `json:"sessionid,omitempty"`
	ReqID     string `json:"reqid,omitempty"`
}

type sseEventData struct {
	Messages []sseMessage `json:"messages,omitempty"`
}

type sseMessage struct {
	MimeType string       `json:"mime_type"`
	Content  string       `json:"content"`
	Status   string       `json:"status,omitempty"`
	MetaData *sseMetaData `json:"meta_data,omitempty"`
}

type sseMetaData struct {
	MultiLoad []sseMultiLoad `json:"multi_load,omitempty"`
}

type sseMultiLoad struct {
	Type    string                 `json:"type"`
	Content map[string]interface{} `json:"content,omitempty"`
}

// parseSSE parses the full SSE response body and extracts content, thinking, and tool calls
func parseSSE(body []byte) (content, reasoning string, toolCalls []providers.ToolCall) {
	var contentBuf, reasoningBuf strings.Builder
	var tcFound []providers.ToolCall

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Split(scanDoubleNewline)

	for scanner.Scan() {
		block := scanner.Text()
		if block == "" {
			continue
		}

		_, eventData := parseSSEBlock(block)
		if eventData == "" || eventData == "[DONE]" {
			continue
		}

		msgContent, msgThinking, tcs, err := parseSSEEvent(eventData)
		if err != nil {
			continue
		}

		if msgThinking != "" {
			reasoningBuf.WriteString(msgThinking)
		}
		if msgContent != "" {
			contentBuf.WriteString(msgContent)
		}
		if len(tcs) > 0 {
			tcFound = tcs
		}
	}

	fullContent := strings.TrimSpace(contentBuf.String())
	fullReasoning := strings.TrimSpace(reasoningBuf.String())

	if len(tcFound) > 0 {
		return "", "", tcFound
	}

	return fullContent, fullReasoning, nil
}

// parseSSEEvent parses a single SSE event data JSON
// content and thinking are accumulated strings (caller tracks deltas).
// They are returned as the cumulative values from this event.
func parseSSEEvent(eventData string) (content, thinking string, toolCalls []providers.ToolCall, err error) {
	var evt sseEvent
	if err := json.Unmarshal([]byte(eventData), &evt); err != nil {
		return "", "", nil, fmt.Errorf("json unmarshal: %w", err)
	}

	if evt.ErrorCode != 0 {
		return "", "", nil, fmt.Errorf("API error: %d %s", evt.ErrorCode, evt.ErrorMsg)
	}

	if evt.Data == nil {
		return "", "", nil, nil
	}

	var contentStr, thinkingStr string

	for _, msg := range evt.Data.Messages {
		// Extract thinking from meta_data.multi_load
		if msg.MetaData != nil {
			for _, load := range msg.MetaData.MultiLoad {
				if load.Type == "deep_think" && load.Content != nil {
					if tc, ok := load.Content["think_content"].(string); ok && tc != "" {
						if len(tc) > len(thinkingStr) {
							thinkingStr = tc
						}
					} else if c, ok := load.Content["content"].(string); ok && c != "" {
						if len(c) > len(thinkingStr) {
							thinkingStr = c
						}
					}
				}
			}
		}

		// Extract content from multi_load/iframe or text/plain
		if msg.MimeType == "multi_load/iframe" || msg.MimeType == "text/plain" {
			if msg.Content != "" {
				filtered := msg.Content
				// Remove think markers
				filtered = strings.ReplaceAll(filtered, "[(deep_think)]", "")
				// Remove multimodal_chat_think markers with regex-like approach
				for {
					idx := strings.Index(filtered, "[(multimodal_chat_think_")
					if idx < 0 {
						break
					}
					end := strings.Index(filtered[idx:], "]")
					if end < 0 {
						break
					}
					filtered = filtered[:idx] + filtered[idx+end+1:]
				}
				filtered = strings.TrimSpace(filtered)
				if filtered != "" && filtered != "[(deep_think)]" {
					if len(filtered) > len(contentStr) {
						contentStr = filtered
					}
				}
			}
		}
	}

	return contentStr, thinkingStr, nil, nil
}



const defaultQwenCNMaxQueryChars = 12000

// maxQueryChars returns the configured max query character limit, or the default.
func (c *Client) maxQueryChars() int {
	if c.config.MaxQueryChars > 0 {
		return c.config.MaxQueryChars
	}
	return defaultQwenCNMaxQueryChars
}

// qwencnEstimate is a provider-specific estimator for QwenCN's query size.
// QwenCN uses a single "content" field with system prepended, conversation parts joined by \n\n.
func qwencnEstimate(messages []providers.Message, tools []providers.Tool) int {
	var systemPrompt string
	var conversationParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemPrompt = string(msg.Content)
		case "user":
			conversationParts = append(conversationParts, string(msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var tcs []string
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, fmt.Sprintf(
						"##TOOL_CALL##\n{\"name\": \"%s\", \"input\": %s}\n##END_CALL##",
						tc.Function.Name, tc.Function.Arguments,
					))
				}
				content := string(msg.Content)
				if content != "" {
					conversationParts = append(conversationParts, "Assistant: "+content+"\n"+strings.Join(tcs, "\n"))
				} else {
					conversationParts = append(conversationParts, "Assistant: "+strings.Join(tcs, "\n"))
				}
			} else {
				conversationParts = append(conversationParts, "Assistant: "+string(msg.Content))
			}
		case "tool":
			conversationParts = append(conversationParts, "Tool Result: "+string(msg.Content))
		}
	}

	userContent := strings.Join(conversationParts, "\n\n")

	// Approximate tool prompt overhead if tools present
	toolPromptSize := 0
	if len(tools) > 0 {
		// engine.InjectTools will produce: "You have access to the following actions:\n\n" + BuildMarkerPrompt + few-shot
		promptHead := toolcall.BuildMarkerPrompt(tools, false)
		toolPromptSize = len("You have access to the following actions:\n\n") + len(promptHead) + 500 // few-shot overhead
	}

	// Final content = system (if present) + "\n\nUser: " + user content
	var b strings.Builder
	if systemPrompt != "" {
		b.WriteString(systemPrompt)
		b.WriteString("\n\nUser: ")
	}
	b.WriteString(userContent)

	return b.Len() + toolPromptSize
}
