package qwencn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coocood/freecache"
	"github.com/user/wc2api/internal/config"
	providers "github.com/user/wc2api/internal/providers"
	"github.com/user/wc2api/internal/toolcall"
)

const (
	chatPath      = "/api/v2/chat"
	modelListHost = "https://chat2-api.qianwen.com"
	modelListPath = "/api/v1/model/list"
	modelCacheTTL = 3600 // 1 hour in seconds
)

// Client handles Qwen CN (qianwen.com) webchat interactions using SSO ticket auth
type Client struct {
	config      config.QwenCNConfig
	httpClient  *http.Client
	baseURL     *url.URL

	mu         sync.Mutex
	sessionIDs map[string]string // reqID -> sessionID for cleanup

	deviceID   string              // generated once, reused for model list API
	modelCache *freecache.Cache
	toolEngine *toolcall.ToolCallEngine // unified tool call parsing/validation/retry
}

// New creates a new Qwen CN client with SSO ticket authentication
func New(cfg config.QwenCNConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	// Use ResponseHeaderTimeout instead of Client.Timeout.
	// Client.Timeout covers the entire HTTP lifecycle including body reads,
	// which kills long-running SSE streams mid-generation.
	// ResponseHeaderTimeout only applies to waiting for the initial response headers,
	// allowing the stream to run as long as data keeps flowing.
	timeout := time.Duration(cfg.Timeout) * time.Second
	httpClient := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: timeout,
		},
	}

	return &Client{
		config:      cfg,
		baseURL:     baseURL,
		httpClient:  httpClient,
		sessionIDs:  make(map[string]string),
		deviceID:    uuid(),
		modelCache:  freecache.NewCache(512 * 1024),
		toolEngine:  toolcall.New(toolcall.CompactConfig()),
	}, nil
}

// Name returns the provider name
func (c *Client) Name() string {
	return "qwen-cn"
}

// Close cleans up the provider
func (c *Client) Close() error {
	return nil
}

// defaultHeaders returns browser-like headers for Qwen CN API requests
func (c *Client) defaultHeaders() http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json, text/event-stream, text/plain, */*")
	h.Set("Accept-Language", "zh-CN,zh;q=0.9")
	h.Set("Cache-Control", "no-cache")
	h.Set("Origin", "https://www.qianwen.com")
	h.Set("Pragma", "no-cache")
	h.Set("Referer", "https://www.qianwen.com/")
	h.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
	h.Set("Content-Type", "application/json")
	h.Set("x-device-id", c.deviceID)
	h.Set("x-platform", "pc_tongyi")
	h.Set("x-wpk-bid", "66ur41cs-cntu1744")
	return h
}

// ticketCookie returns the Cookie header value for SSO ticket auth
func (c *Client) ticketCookie() string {
	return "tongyi_sso_ticket=" + c.config.Ticket
}

// uuid generates a UUID v4 string
func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback if crypto/rand fails (should not happen)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	// Set version (4) and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// nonce generates a 12-character random hex string
func nonce() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano()%281474976710656)[:12]
	}
	return hex.EncodeToString(b)
}

// extractTextContent extracts text from a message content that can be string or array
func extractTextContent(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if typ, _ := m["type"].(string); typ == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// isTransientError returns true for HTTP status codes that are safe to retry.
func isTransientError(status int) bool {
	return status == 429 || status == 502 || status == 503 || status == 504
}

// doRequest sends a chat request to Qwen CN API and returns the response stream.
// Retries once on transient errors (429, 502, 503, 504).
func (c *Client) doRequest(ctx context.Context, reqBody map[string]interface{}, reqID string) (*http.Response, error) {
	const maxAttempts = 2 // initial + 1 retry

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := c.doRequestOnce(ctx, reqBody, reqID)
		if err != nil {
			// Check if the error contains a transient status code
			var status int
			if n, scanErr := fmt.Sscanf(err.Error(), "QwenCN API error: status %d", &status); scanErr == nil && n == 1 && isTransientError(status) {
				lastErr = err
				backoff := time.Duration(1<<attempt) * time.Second // 1s, 2s
				slog.Warn("QwenCN: transient error, retrying",
					"status", status, "attempt", attempt+1, "backoff_ms", backoff.Milliseconds())
				time.Sleep(backoff)
				continue
			}
			return nil, err
		}
		return resp, nil
	}
	return nil, fmt.Errorf("QwenCN API: all %d attempts failed, last error: %w", maxAttempts, lastErr)
}

// doRequestOnce sends a single chat request to Qwen CN API and returns the response stream.
func (c *Client) doRequestOnce(ctx context.Context, reqBody map[string]interface{}, reqID string) (*http.Response, error) {
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	queryParams := url.Values{}
	queryParams.Set("biz_id", "ai_qwen")
	queryParams.Set("chat_client", "h5")
	queryParams.Set("device", "pc")
	queryParams.Set("fr", "pc")
	queryParams.Set("pr", "qwen")
	queryParams.Set("ut", uuid())
	queryParams.Set("nonce", nonce())
	queryParams.Set("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))

	u := c.baseURL.ResolveReference(&url.URL{Path: chatPath, RawQuery: queryParams.Encode()})

	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header = c.defaultHeaders()
	req.Header.Set("Cookie", c.ticketCookie())
	req.Header.Set("x-chat-id", reqID)
	req.Header.Set("x-wpk-reqid", reqID)
	req.Header.Set("x-wpk-traceid", uuid())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("QwenCN API error: status %d, body: %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// modelListResponse represents the API response from the model list endpoint
type modelListResponse struct {
	TraceID string            `json:"trace_id"`
	Code    int               `json:"code"`
	Msg     string            `json:"msg"`
	Success bool              `json:"success"`
	Data    []modelListEntry  `json:"data"`
}

type modelListEntry struct {
	ModelCode       string   `json:"modelCode"`
	DisplayModelName string  `json:"displayModelName"`
	Show            bool     `json:"show"`
	LegacyModelCode string   `json:"legacyModelCode"`
}

// fetchModels calls the model list API and returns models + ID mappings.
func (c *Client) fetchModels() ([]providers.Model, map[string]string, error) {
	u := fmt.Sprintf("%s%s?biz_id=ai_qwen&chat_client=h5&device=pc&fr=pc&pr=qwen&ut=%s&la=zh-CN&tz=Asia%%2FSingapore&wv=2.8.6&ve=2.8.6",
		modelListHost, modelListPath, c.deviceID)

	req, err := http.NewRequestWithContext(context.Background(), "GET", u, http.NoBody)
	if err != nil {
		return nil, nil, fmt.Errorf("create model list request: %w", err)
	}

	req.Header = c.defaultHeaders()
	req.Header.Set("Cookie", c.ticketCookie())
	req.Header.Set("x-deviceid", c.deviceID)
	req.Header.Set("x-platform", "pc_tongyi")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("model list request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("model list API status %d: %s", resp.StatusCode, string(body))
	}

	var mlResp modelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&mlResp); err != nil {
		return nil, nil, fmt.Errorf("model list decode: %w", err)
	}

	if mlResp.Code != 0 || !mlResp.Success {
		return nil, nil, fmt.Errorf("model list API error: code=%d msg=%s", mlResp.Code, mlResp.Msg)
	}

	var models []providers.Model
	idMap := make(map[string]string, len(mlResp.Data))
	now := time.Now()

	for _, entry := range mlResp.Data {
		if !entry.Show {
			continue
		}
		userFacing := "qwen-cn-" + entry.ModelCode
		models = append(models, providers.Model{
			ID:      userFacing,
			Object:  "model",
			Created: now.Unix(),
			OwnedBy: "qwencn",
		})
		if entry.LegacyModelCode != "" {
			idMap[entry.ModelCode] = entry.LegacyModelCode
		}
	}

	if len(models) == 0 {
		return nil, nil, fmt.Errorf("model list returned no show:true models")
	}

	return models, idMap, nil
}

// getModels returns cached models, fetching from API if expired or absent.
// On API failure it returns the fallback hardcoded list.
func (c *Client) getModels() []providers.Model {
	// Try freecache first
	if data, err := c.modelCache.Get([]byte("models")); err == nil {
		var models []providers.Model
		if err := json.Unmarshal(data, &models); err == nil {
			return models
		}
	}

	models, idMap, err := c.fetchModels()
	if err != nil {
		slog.Warn("QwenCN: failed to fetch model list, using fallback", "error", err)
		models = fallbackModels()
		idMap = nil
	}

	// Store in freecache (both models and idmap)
	if data, err := json.Marshal(models); err == nil {
		c.modelCache.Set([]byte("models"), data, modelCacheTTL)
	}
	if idMap != nil {
		if data, err := json.Marshal(idMap); err == nil {
			c.modelCache.Set([]byte("idmap"), data, modelCacheTTL)
		}
	}

	if err != nil {
		slog.Warn("QwenCN: using fallback models", "count", len(models))
	}
	return models
}

// sessionCleanup deletes a chat session after use
func (c *Client) sessionCleanup(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("biz_id", "ai_qwen")
	q.Set("chat_client", "h5")
	q.Set("device", "pc")
	q.Set("fr", "pc")
	q.Set("pr", "qwen")
	q.Set("ut", "5b68c267-cd8e-fd0e-148a-18345bc9a104")

	u := c.baseURL.ResolveReference(&url.URL{Path: "/api/v2/session/delete", RawQuery: q.Encode()})

	body := map[string]interface{}{
		"session_id": sessionID,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(jsonBody))
	if err != nil {
		slog.Warn("QwenCN session cleanup: failed to create request", "error", err)
		return
	}

	req.Header = c.defaultHeaders()
	req.Header.Set("Cookie", c.ticketCookie())
	req.Header.Set("X-Platform", "pc_tongyi")
	req.Header.Set("X-DeviceId", "5b68c267-cd8e-fd0e-148a-18345bc9a104")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Warn("QwenCN session cleanup: request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	slog.Debug("QwenCN session cleaned up", "sessionID", sessionID, "status", resp.StatusCode)
}
