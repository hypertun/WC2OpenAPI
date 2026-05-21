package stepfun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	"github.com/user/wc2api/internal/toolcall"
)

const (
	createSessionEndpoint = "/api/agent/capy.agent.v1.AgentService/CreateChatSession"
	chatStreamEndpoint    = "/api/agent/capy.agent.v1.AgentService/ChatStream"
	refreshTokenEndpoint  = "/passport/proto.api.passport.v1.PassportService/RefreshToken"
	getChatConfigEndpoint = "/api/agent/capy.agent.v1.AgentService/GetChatConfig"
)

// frame represents a length-prefixed JSON message from the ChatStream endpoint.
type frame struct {
	Data json.RawMessage
}

// readFrames reads Connect-protocol frames from a reader.
// Each frame is: 1 byte flags + 4 bytes big-endian uint32 length + JSON bytes.
func readFrames(r io.Reader) (<-chan frame, <-chan error) {
	frames := make(chan frame, 50)
	errs := make(chan error, 1)

	go func() {
		defer close(frames)
		defer close(errs)

		reader := bufio.NewReader(r)
		frameNum := 0
		for {
			// Read 5-byte header: 1 byte flags + 4-byte length prefix
			headerBuf := make([]byte, 5)
			n, err := io.ReadFull(reader, headerBuf)
			if err != nil {
				slog.Debug("stepfun: readFrames header read", "bytes", n, "error", err, "eof", err == io.EOF)
				if err != io.EOF {
					errs <- fmt.Errorf("stepfun: read frame header: %w", err)
				}
				return
			}

		flags := headerBuf[0]
		length := binary.BigEndian.Uint32(headerBuf[1:5])

		if length == 0 {
				slog.Debug("stepfun: skipping zero-length frame")
				continue
			}

			// Error frames (flags == 2)
			if flags == 2 {
				errData := make([]byte, length)
				if _, readErr := io.ReadFull(reader, errData); readErr != nil {
					errs <- fmt.Errorf("stepfun: read error frame: %w", readErr)
					return
				}
				slog.Error("stepfun: server error frame", "text", string(bytes.TrimRight(errData, "\x00")))
				errs <- fmt.Errorf("stepfun: server error: %s",
					string(bytes.TrimRight(errData, "\x00")))
				return
			}

			data := make([]byte, length)
			_, err = io.ReadFull(reader, data)
			if err != nil {
				slog.Error("stepfun: read frame body error", "error", err)
				errs <- fmt.Errorf("stepfun: read frame body: %w", err)
				return
			}

			// Strip trailing null bytes
			data = bytes.TrimRight(data, "\x00")

			frameNum++

			select {
			case frames <- frame{Data: json.RawMessage(data)}:
			default:
				slog.Warn("stepfun: frame buffer full, dropping frame")
			}
		}
	}()

	return frames, errs
}

// streamEvent is the top-level wrapper for ChatStream response frames.
type streamEvent struct {
	Data *struct {
		Event *struct {
			StartEvent     *json.RawMessage `json:"startEvent,omitempty"`
			MessageEvent   *json.RawMessage `json:"messageEvent,omitempty"`
			ReasoningEvent *json.RawMessage `json:"reasoningEvent,omitempty"`
			TextEvent      *json.RawMessage `json:"textEvent,omitempty"`
			PipelineEvent  *json.RawMessage `json:"pipelineEvent,omitempty"`
			HeartBeatEvent *json.RawMessage `json:"heartBeatEvent,omitempty"`
			MessageDoneEvent *json.RawMessage `json:"messageDoneEvent,omitempty"`
			DoneEvent      *json.RawMessage `json:"doneEvent,omitempty"`
		} `json:"event"`
	} `json:"data"`
}

// reasoningPayload is the content of a reasoningEvent.
type reasoningPayload struct {
	Text string `json:"text"`
}

// textPayload is the content of a textEvent.
type textPayload struct {
	Text string `json:"text"`
}

// Client handles StepFun webchat interactions.
type Client struct {
	config     config.StepFunConfig
	httpClient *http.Client
	baseURL    *url.URL

	mu            sync.Mutex
	accessToken   string
	oasisWebid    string
	refreshToken  string
	tokenExpiry   time.Time
	chatSessionID string
	chatID        string

	modelCache *freecache.Cache
	toolEngine *toolcall.ToolCallEngine
}

// parseJWTExpiry extracts the exp claim from a JWT token.
// JWT format: header.payload.signature (base64url encoded)
func parseJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid JWT format")
	}

	payload := parts[1]
	// Add padding if needed
	padding := 4 - (len(payload) % 4)
	if padding != 4 {
		payload += strings.Repeat("=", padding)
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim in JWT")
	}

	return time.Unix(claims.Exp, 0), nil
}

// New creates a new StepFun client.
func New(cfg config.StepFunConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("stepfun: invalid base URL: %w", err)
	}

	// Split combined token "ACCESS...REFRESH" if present
	var accessToken, refreshToken string
	parts := strings.Split(cfg.OasisToken, "...")
	if len(parts) == 2 {
		accessToken = parts[0]
		refreshToken = parts[1]
		slog.Debug("stepfun: split combined token into access and refresh tokens")
	} else {
		accessToken = cfg.OasisToken
		slog.Debug("stepfun: no refresh token in config, using access token only")
	}

	// Parse expiry from access token
	var tokenExpiry time.Time
	if expiry, err := parseJWTExpiry(accessToken); err == nil {
		tokenExpiry = expiry
		slog.Debug("stepfun: parsed token expiry", "expiry", expiry.Format(time.RFC3339))
	} else {
		slog.Warn("stepfun: failed to parse JWT expiry", "error", err)
		// Default to 30 min validity if parsing fails
		tokenExpiry = time.Now().Add(30 * time.Minute)
	}

	return &Client{
		config:      cfg,
		baseURL:     baseURL,
		accessToken: accessToken,
		oasisWebid:  cfg.OasisWebid,
		refreshToken: refreshToken,
		tokenExpiry: tokenExpiry,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		modelCache: freecache.NewCache(512 * 1024),
		toolEngine: toolcall.New(toolcall.CompactConfig()),
	}, nil
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "stepfun"
}

// Close cleans up the provider.
func (c *Client) Close() error {
	return nil
}

// headers returns common headers for StepFun API requests.
// Builds the combined token cookie "ACCESS...REFRESH" if both parts exist.
func (c *Client) headers() http.Header {
	c.mu.Lock()
	accessToken := c.accessToken
	refreshToken := c.refreshToken
	oasisWebid := c.oasisWebid
	c.mu.Unlock()

	h := http.Header{}
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "en-US,en;q=0.7")
	h.Set("Content-Type", "application/json")
	h.Set("Connect-Protocol-Version", "1")
	h.Set("Origin", c.baseURL.String())
	h.Set("Referer", c.baseURL.String()+"/chats/new")
	h.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36")

	// Rebuild combined token: "ACCESS...REFRESH" if refresh token exists
	tokenValue := accessToken
	if refreshToken != "" {
		tokenValue = accessToken + "..." + refreshToken
	}
	h.Set("Cookie", fmt.Sprintf("i18next=en; Oasis-Webid=%s; is_pc_desktop=false; sidebar_push_state=false; Oasis-Token=%s; sidebar_state=false", oasisWebid, tokenValue))

	h.Set("Oasis-Appid", "10200")
	h.Set("Oasis-Language", "en")
	h.Set("Oasis-Platform", "web")
	h.Set("X-Waf-Client-Type", "fetch_sdk")
	h.Set("canary", "false")
	return h
}

// connectHeaders returns headers for the Connect protocol streaming endpoint.
func (c *Client) connectHeaders() http.Header {
	h := c.headers()
	h.Set("Content-Type", "application/connect+json")
	return h
}

// ensureValidToken checks if the access token is expired and refreshes if needed.
// If no refresh token exists, trust the configured access token directly.
func (c *Client) ensureValidToken(ctx context.Context) error {
	c.mu.Lock()
	
	// Check if token is still valid
	if time.Now().Before(c.tokenExpiry) {
		c.mu.Unlock()
		return nil
	}

	// No refresh token — can't refresh, use expired token anyway
	if c.refreshToken == "" {
		slog.Warn("stepfun: access token expired but no refresh token available, using expired token")
		c.mu.Unlock()
		return nil
	}

	slog.Debug("stepfun: access token expired, refreshing")
	// IMPORTANT: Release the lock before calling refreshTokenLocked,
	// since it calls headers() which needs the lock
	c.mu.Unlock()
	
	return c.refreshTokenLocked(ctx)
}

// refreshTokenLocked refreshes the access token. Must be called with c.mu held.
func (c *Client) refreshTokenLocked(ctx context.Context) error {
	slog.Debug("stepfun: starting token refresh request")
	
	// Use a fresh context with a 30-second timeout instead of inheriting the request context,
	// which might have already expired. The HTTP client has a 120-second timeout as backup.
	refreshCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	req, err := http.NewRequestWithContext(refreshCtx, "POST",
		c.baseURL.String()+refreshTokenEndpoint, strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("stepfun: create refresh request: %w", err)
	}
	req.Header = c.headers()

	slog.Debug("stepfun: sending refresh token request", "endpoint", c.baseURL.String()+refreshTokenEndpoint)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stepfun: refresh token: %w", err)
	}
	defer resp.Body.Close()
	slog.Debug("stepfun: refresh response received", "status", resp.StatusCode)

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("stepfun: refresh failed", "status", resp.StatusCode, "body", string(body[:min(len(body), 200)]))
		return fmt.Errorf("stepfun: refresh token HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var result struct {
		AccessToken struct {
			Raw      string `json:"raw"`
			Duration int    `json:"duration"`
		} `json:"accessToken"`
		RefreshToken struct {
			Raw string `json:"raw"`
		} `json:"refreshToken"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("stepfun: decode error", "error", err, "body_len", len(body), "body_preview", string(body[:min(len(body), 100)]))
		return fmt.Errorf("stepfun: decode refresh response: %w", err)
	}

	if result.AccessToken.Raw != "" {
		c.accessToken = result.AccessToken.Raw
		c.tokenExpiry = time.Now().Add(time.Duration(result.AccessToken.Duration) * time.Second)
		slog.Info("stepfun: access token refreshed", "new_expiry", c.tokenExpiry.Format(time.RFC3339), "duration_seconds", result.AccessToken.Duration)
	}
	if result.RefreshToken.Raw != "" {
		c.refreshToken = result.RefreshToken.Raw
		slog.Debug("stepfun: refresh token rotated")
	}

	return nil
}

// ensureSession creates a new chat session if one doesn't exist.
func (c *Client) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	sessionID := c.chatSessionID
	c.mu.Unlock()

	if sessionID != "" {
		return nil
	}

	slog.Debug("stepfun: creating new chat session")
	if err := c.ensureValidToken(ctx); err != nil {
		return err
	}

 	body := `{"type":"TYPE_INCOGNITO"}`
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL.String()+createSessionEndpoint, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("stepfun: create session request: %w", err)
	}
	req.Header = c.headers()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Error("stepfun: create session HTTP error", "error", err)
		return fmt.Errorf("stepfun: create session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("stepfun: create session failed", "status", resp.StatusCode, "response", string(body[:min(len(body), 200)]))
		return fmt.Errorf("stepfun: create session HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var result struct {
		ChatSession *struct {
			ChatSessionID string `json:"chatSessionId"`
			ChatID        string `json:"chatId"`
		} `json:"chatSession"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("stepfun: decode session response: %w", err)
	}

	if result.ChatSession == nil || result.ChatSession.ChatSessionID == "" {
		return fmt.Errorf("stepfun: empty session response")
	}

	c.mu.Lock()
	c.chatSessionID = result.ChatSession.ChatSessionID
	c.chatID = result.ChatSession.ChatID
	c.mu.Unlock()

	return nil
}

// encodeFrame encodes a JSON body as a Connect-protocol frame:
// 1 byte flags (0x00 = uncompressed data) + 4 bytes big-endian length + body.
func encodeFrame(body []byte) []byte {
	frame := make([]byte, 5+len(body))
	frame[0] = 0x00 // flags: uncompressed data frame
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)))
	copy(frame[5:], body)
	return frame
}

// chatStream sends a chat request to StepFun using the Connect protocol
// and returns a channel of parsed stream events.
func (c *Client) chatStream(ctx context.Context, query string, model string, enableReasoning bool) (<-chan streamEvent, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}

	c.mu.Lock()
	sessionID := c.chatSessionID
	c.mu.Unlock()

	// Strip "stepfun-" prefix from model ID since the API expects bare names (e.g. "deepseek-r1", not "stepfun-deepseek-r1")
	apiModel := strings.TrimPrefix(model, "stepfun-")

	reqBody := map[string]interface{}{
		"message": map[string]interface{}{
			"chatSessionId": sessionID,
			"content": map[string]interface{}{
				"userMessage": map[string]interface{}{
					"qa": map[string]interface{}{
						"content": query,
					},
				},
			},
		},
		"config": map[string]interface{}{
			"model":           apiModel,
			"enableReasoning": enableReasoning,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("stepfun: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL.String()+chatStreamEndpoint, bytes.NewReader(encodeFrame(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("stepfun: create request: %w", err)
	}
	httpReq.Header = c.connectHeaders()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stepfun: http post: %w", err)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("stepfun: api error HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	frames, errs := readFrames(resp.Body)

	eventChan := make(chan streamEvent, 50)

	go func() {
		defer close(eventChan)
		defer resp.Body.Close()

		frameCount := 0
		for {
			select {
			case f, ok := <-frames:
			if !ok {
				slog.Debug("stepfun: frames channel closed", "frame_count", frameCount)
				return
			}
			frameCount++
			var event streamEvent
				if err := json.Unmarshal(f.Data, &event); err != nil {
					slog.Debug("stepfun: unmarshal error", "error", err)
					continue
				}
				select {
				case eventChan <- event:
				case <-ctx.Done():
					slog.Debug("stepfun: context cancelled")
					return
				}
			case err, ok := <-errs:
				if ok && err != nil {
					slog.Error("stepfun: stream error", "error", err)
				}
				return
			case <-ctx.Done():
				slog.Debug("stepfun: context done")
				return
			}
		}
	}()

	return eventChan, nil
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
