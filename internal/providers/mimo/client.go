package mimo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coocood/freecache"
	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/toolcall"
)

const (
	chatEndpoint  = "/open-apis/bot/chat"
	configEndpoint = "/open-apis/bot/config"
)

// sseEvent represents a single SSE data event from MiMo
type sseEvent struct {
	Type            string `json:"type,omitempty"`
	Content         string `json:"content,omitempty"`
	PromptTokens    int    `json:"promptTokens,omitempty"`
	CompletionTokens int   `json:"completionTokens,omitempty"`
	TotalTokens     int    `json:"totalTokens,omitempty"`
}

// MimoChatRequest is the JSON body sent to MiMo's chat API
type MimoChatRequest struct {
	MsgID          string            `json:"msgId"`
	ConversationID string            `json:"conversationId"`
	Query          string            `json:"query"`
	ModelConfig    MimoModelConfig   `json:"modelConfig"`
	MultiMedias    []json.RawMessage `json:"multiMedias"`
	Attachments    []json.RawMessage `json:"attachments"`
}

// MimoModelConfig is the model config inside the chat request
type MimoModelConfig struct {
	EnableThinking  bool    `json:"enableThinking"`
	Temperature     float64 `json:"temperature"`
	TopP            float64 `json:"topP"`
	WebSearchStatus string  `json:"webSearchStatus"`
	Model           string  `json:"model"`
}

// Client handles MiMo AI Studio webchat interactions
type Client struct {
	config          config.MiMoConfig
	httpClient      *http.Client
	baseURL         *url.URL
	cookieHeader    string

	modelCache *freecache.Cache
	toolEngine *toolcall.ToolCallEngine
}

// New creates a new MiMo client
func New(cfg config.MiMoConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("mimo: invalid base URL: %w", err)
	}

	cookieHeader := fmt.Sprintf("serviceToken=%s; userId=%s; xiaomichatbot_ph=%s",
		cfg.ServiceToken, cfg.UserID, cfg.XiaomiChatbotPH)

	return &Client{
		config:       cfg,
		baseURL:      baseURL,
		cookieHeader: cookieHeader,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		modelCache: freecache.NewCache(512 * 1024),
		toolEngine: toolcall.New(toolcall.DefaultConfig()),
	}, nil
}

// Name returns the provider name
func (c *Client) Name() string {
	return "mimo"
}

// Close cleans up the provider
func (c *Client) Close() error {
	return nil
}

// headers returns common headers for MiMo API requests
func (c *Client) headers() http.Header {
	h := http.Header{}
	h.Set("Accept", "*/*")
	h.Set("Content-Type", "application/json")
	h.Set("Origin", c.baseURL.String())
	h.Set("Referer", c.baseURL.String()+"/")
	h.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")
	h.Set("Cookie", c.cookieHeader)
	return h
}

// chatURL returns the full chat endpoint URL with the xiaomichatbot_ph param
func (c *Client) chatURL() string {
	return c.baseURL.String() + chatEndpoint + "?xiaomichatbot_ph=" + url.QueryEscape(c.config.XiaomiChatbotPH)
}

// buildChatRequest builds the MiMo API request body from the given parts
func (c *Client) buildChatRequest(query string, thinking bool, model string) MimoChatRequest {
	return MimoChatRequest{
		MsgID:          hex.EncodeToString(randomBytes(16)),
		ConversationID: hex.EncodeToString(randomBytes(16)),
		Query:          query,
		ModelConfig: MimoModelConfig{
			EnableThinking:  thinking,
			Temperature:     0.8,
			TopP:            0.95,
			WebSearchStatus: "disabled",
			Model:           model,
		},
		MultiMedias: []json.RawMessage{},
		Attachments: []json.RawMessage{},
	}
}

// streamChat sends a chat request to MiMo and returns an SSE event channel
func (c *Client) streamChat(ctx context.Context, reqBody MimoChatRequest) (<-chan sseEvent, error) {
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("mimo: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.chatURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("mimo: create request: %w", err)
	}
	httpReq.Header = c.headers()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mimo: http post: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("mimo: api error HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	// MiMo native SSE prefix events (same as Python _MIMO_SSE_PREFIXES)
	mimoPrefixes := map[string]bool{
		"webSearch": true, "getTime": true, "getTimeInfo": true, "sessionSearch": true,
		"imageSearch": true, "fileSearch": true, "getLocation": true, "webExtract": true,
		"getWeather": true, "calculator": true,
	}

	eventChan := make(chan sseEvent, 100)

	go func() {
		defer resp.Body.Close()
		defer close(eventChan)

		reader := bufio.NewReader(resp.Body)
		lineNum := 0

		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}

			lineStr := string(line)
			lineNum++

			if !strings.HasPrefix(lineStr, "data:") {
				continue
			}

			data := strings.TrimSpace(lineStr[5:])
			if data == "" {
				continue
			}

			// Skip list-type events
			if data[0] == '[' {
				continue
			}

			var event sseEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			// Filter MiMo native SSE prefix events
			if event.Type == "text" && mimoPrefixes[event.Content] {
				continue
			}

			select {
			case eventChan <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventChan, nil
}

// discoverModels fetches available models from MiMo's config endpoint
func (c *Client) discoverModels(ctx context.Context) ([]string, error) {
	configURL := c.baseURL.String() + configEndpoint
	req, err := http.NewRequestWithContext(ctx, "GET", configURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mimo: create discover request: %w", err)
	}
	req.Header = c.headers()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mimo: discover models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mimo: discover models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data *struct {
			ModelConfigList []struct {
				Model string `json:"model"`
			} `json:"modelConfigList"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mimo: discover decode: %w", err)
	}

	if result.Data == nil {
		return nil, fmt.Errorf("mimo: discover: no data in response")
	}

	models := make([]string, 0, len(result.Data.ModelConfigList))
	for _, mc := range result.Data.ModelConfigList {
		if mc.Model != "" {
			models = append(models, mc.Model)
		}
	}

	return models, nil
}

// getCachedModels returns cached models or fetches fresh ones (freecache-backed)
func (c *Client) getCachedModels(ctx context.Context) ([]string, error) {
	if data, err := c.modelCache.Get([]byte("models")); err == nil {
		var models []string
		if json.Unmarshal(data, &models) == nil {
			return models, nil
		}
	}

	models, err := c.discoverModels(ctx)
	if err != nil {
		// Return stale cache if available
		if data, err := c.modelCache.Get([]byte("models")); err == nil {
			var stale []string
			if json.Unmarshal(data, &stale) == nil {
				return stale, nil
			}
		}
		return nil, err
	}

	if data, err := json.Marshal(models); err == nil {
		c.modelCache.Set([]byte("models"), data, 3600)
	}
	return models, nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
