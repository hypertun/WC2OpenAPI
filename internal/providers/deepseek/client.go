package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/user/wc2api/internal/config"
	"github.com/user/wc2api/internal/providers"
)

const (
	deviceID = "deepseek_to_api"
	osType   = "android"
	completionURL = "/api/v0/chat/completion"
	settingsURL   = "/api/v0/client/settings"
)

// modelTypeToPublicName maps DeepSeek's internal model_type to OpenAI-compatible model names.
// This is the single source of truth for model name lookup.
var modelTypeToPublicName = map[string]string{
	"default": "deepseek-v4-flash",
	"expert":  "deepseek-v4-pro",
}

// Client handles DeepSeek webchat interactions with DS2API-style auth
type Client struct {
	config     config.DeepSeekConfig
	httpClient *http.Client
	baseURL    *url.URL
	authToken  string
	sessionID  string
	loggedIn   bool
	lastLogin  time.Time
}

// New creates a new DeepSeek client with uTLS support
func New(cfg config.DeepSeekConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	// Create cookie jar for session management
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	httpClient := newUTLSHTTPClient(time.Duration(cfg.Timeout) * time.Second)
	httpClient.Jar = jar

	client := &Client{
		config:  cfg,
		baseURL: baseURL,
		httpClient: httpClient,
	}

	if err := client.login(); err != nil {
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	if err := client.createSession(); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return client, nil
}

// Name returns the provider name
func (c *Client) Name() string {
	return "deepseek"
}

// login performs login with DS2API-style Android client headers
func (c *Client) login() error {
	slog.Info("Attempting DeepSeek login", "email", c.config.Email)

	// Login request with DS2API-style payload
	payload := map[string]any{
		"email":     strings.TrimSpace(c.config.Email),
		"password":  strings.TrimSpace(c.config.Password),
		"device_id": deviceID,
		"os":        osType,
	}

	resp, err := c.doRequest(context.Background(), "POST", c.config.LoginURL, payload)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read login response: %w", err)
	}

	slog.Debug("Login response", "status", resp.StatusCode, "body", string(body))

	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse DS2API-style response
	var loginResp dsLoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return fmt.Errorf("failed to parse login response: %w", err)
	}

	if loginResp.Code != 0 {
		return fmt.Errorf("login failed: %s", loginResp.Msg)
	}

	// Extract token from response body (DS2API-style)
	if loginResp.Data.BizData.User.Token != "" {
		c.authToken = loginResp.Data.BizData.User.Token
	}

	if c.authToken == "" {
		return fmt.Errorf("no auth token found in response")
	}

	c.loggedIn = true
	c.lastLogin = time.Now()
	slog.Info("DeepSeek login successful")
	slog.Debug("Token acquired", "token_preview", c.authToken[:20]+"...")

	return nil
}

// createSession creates a chat session after login
func (c *Client) createSession() error {
	slog.Info("Creating DeepSeek session...")
	resp, err := c.doRequest(context.Background(), "POST", "/api/v0/chat_session/create", nil)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read session response: %w", err)
	}

	slog.Debug("Session creation response", "status", resp.StatusCode, "body", string(body))

	var sessionResp dsCreateSessionResponse
	if err := json.Unmarshal(body, &sessionResp); err != nil {
		return fmt.Errorf("failed to parse session response: %w", err)
	}

	if sessionResp.Code != 0 || sessionResp.Data.BizCode != 0 {
		return fmt.Errorf("session creation failed: %s", sessionResp.Msg)
	}

	c.sessionID = sessionResp.Data.BizData.ChatSession.ID
	slog.Info("Session created", "session_id", c.sessionID)
	return nil
}

// doRequest makes an HTTP request with DS2API-style Android client headers and auth
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	var contentType string
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
		contentType = "application/json"
	}

	reqURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	// DS2API-style Android client headers (critical for bypassing WAF)
	req.Header.Set("Host", "chat.deepseek.com")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("accept-charset", "UTF-8")
	req.Header.Set("User-Agent", "DeepSeek/5.2.1 Android/35")
	req.Header.Set("x-client-platform", "android")
	req.Header.Set("x-client-version", "5.2.1")
	req.Header.Set("x-client-locale", "zh_CN")

	if c.authToken != "" {
		req.Header.Set("authorization", "Bearer "+c.authToken)
	}

	return c.httpClient.Do(req)
}

// doRequestWithRetryAndPow wraps doRequest with auth-failure detection, retry, and PoW header
func (c *Client) doRequestWithRetryAndPow(ctx context.Context, method, path string, body interface{}, powHeader string) (*http.Response, error) {
	resp, err := c.doRequestWithPow(ctx, method, path, body, powHeader)
	if err != nil {
		return nil, err
	}

	if isAuthFailure(resp) {
		slog.Warn("Auth failure detected, attempting re-login")
		resp.Body.Close()

		if reLoginErr := c.login(); reLoginErr != nil {
			return nil, reLoginErr
		}
		if c.sessionID != "" {
			c.createSession()
		}

		return c.doRequestWithPow(ctx, method, path, body, powHeader)
	}

	return resp, nil
}

// doRequestWithPow makes an HTTP request with optional PoW header
func (c *Client) doRequestWithPow(ctx context.Context, method, path string, body interface{}, powHeader string) (*http.Response, error) {
	var bodyReader io.Reader
	var contentType string
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
		contentType = "application/json"
	}

	reqURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	// DS2API-style Android client headers (critical for bypassing WAF)
	req.Header.Set("Host", "chat.deepseek.com")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("accept-charset", "UTF-8")
	req.Header.Set("User-Agent", "DeepSeek/5.2.1 Android/35")
	req.Header.Set("x-client-platform", "android")
	req.Header.Set("x-client-version", "5.2.1")
	req.Header.Set("x-client-locale", "zh_CN")

	if c.authToken != "" {
		req.Header.Set("authorization", "Bearer "+c.authToken)
	}
	if powHeader != "" {
		req.Header.Set("x-ds-pow-response", powHeader)
	}

	return c.httpClient.Do(req)
}

// ensureAuthenticated checks and refreshes auth if needed
func (c *Client) ensureAuthenticated() error {
	refreshInterval := time.Duration(c.config.TokenRefreshInterval) * time.Second
	if !c.loggedIn || time.Since(c.lastLogin) > refreshInterval {
		if err := c.login(); err != nil {
			return err
		}
		return c.createSession()
	}
	return nil
}

// ListModels returns available DeepSeek models fetched from the settings API.
// Falls back to hardcoded defaults if the API call fails.
func (c *Client) ListModels() []providers.Model {
	models, err := c.fetchModelsFromAPI()
	if err != nil {
		slog.Warn("Failed to fetch models from API, using fallback", "error", err)
		return fallbackModels()
	}
	return models
}

// fetchModelsFromAPI calls the settings endpoint to get available model types
func (c *Client) fetchModelsFromAPI() ([]providers.Model, error) {
	dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.doRequest(dctx, "GET", settingsURL+"?scope=model", nil)
	if err != nil {
		return nil, fmt.Errorf("settings request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("settings request returned status %d", resp.StatusCode)
	}

	var result struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse settings response: %w", err)
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("settings API returned error code: %d", result.Code)
	}

	bizData, _ := result.Data["biz_data"].(map[string]interface{})
	settings, _ := bizData["settings"].(map[string]interface{})
	modelConfigs, _ := settings["model_configs"].(map[string]interface{})
	values, _ := modelConfigs["value"].([]interface{})

	var models []providers.Model
	for _, v := range values {
		cfg, _ := v.(map[string]interface{})
		modelType, _ := cfg["model_type"].(string)
		enabled, _ := cfg["enabled"].(bool)

		if !enabled {
			continue
		}

		publicName, ok := modelTypeToPublicName[modelType]
		if !ok {
			continue
		}

		models = append(models, providers.Model{
			ID:      publicName,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "deepseek",
		})
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no enabled models found in settings")
	}

	return appendNoThinkingVariants(models), nil
}

// fallbackModels returns the hardcoded model list used when the API is unreachable
func fallbackModels() []providers.Model {
	base := []providers.Model{
		{ID: "deepseek-v4-flash", Object: "model", Created: 1677610602, OwnedBy: "deepseek"},
		{ID: "deepseek-v4-pro", Object: "model", Created: 1677610602, OwnedBy: "deepseek"},
	}
	return appendNoThinkingVariants(base)
}

// appendNoThinkingVariants adds -nothinking variants for each model
func appendNoThinkingVariants(models []providers.Model) []providers.Model {
	result := make([]providers.Model, len(models)*2)
	for i, m := range models {
		result[i*2] = m
		result[i*2+1] = providers.Model{
			ID:      m.ID + "-nothinking",
			Object:  m.Object,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		}
	}
	return result
}

// Close cleans up the client
func (c *Client) Close() error {
	return nil
}

// GetPow gets the PoW header for completion requests
func (c *Client) GetPow(ctx context.Context) (string, error) {
	return c.GetPowForTarget(ctx, "/api/v0/chat/completion")
}

// GetPowForTarget gets the PoW header for a specific target path
func (c *Client) GetPowForTarget(ctx context.Context, targetPath string) (string, error) {
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = "/api/v0/chat/completion"
	}

	// Call create_pow_challenge endpoint
	payload := map[string]any{
		"target_path": targetPath,
	}
	resp, err := c.doRequest(ctx, "POST", "/api/v0/chat/create_pow_challenge", payload)
	if err != nil {
		return "", fmt.Errorf("create pow challenge failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read pow response: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse pow response: %w", err)
	}

	code, _ := result["code"].(float64)
	if code != 0 {
		return "", fmt.Errorf("pow challenge failed with code: %v", code)
	}

	data, _ := result["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	challenge, _ := bizData["challenge"].(map[string]any)

	// Solve the PoW challenge
	answer, err := ComputePow(ctx, challenge)
	if err != nil {
		return "", fmt.Errorf("failed to solve pow: %w", err)
	}

	// Build the PoW header
	return BuildPowHeader(challenge, answer)
}

// DS2API-style response structs
type dsLoginResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		BizCode int `json:"biz_code"`
		BizMsg  string `json:"biz_msg"`
		BizData struct {
			User struct {
				Token string `json:"token"`
			} `json:"user"`
		} `json:"biz_data"`
	} `json:"data"`
}

type dsCreateSessionResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		BizCode int `json:"biz_code"`
		BizMsg  string `json:"biz_msg"`
		BizData struct {
			ChatSession struct {
				ID string `json:"id"`
			} `json:"chat_session"`
		} `json:"biz_data"`
	} `json:"data"`
}

// isAuthFailure detects DS2API-style auth failures
func isAuthFailure(resp *http.Response) bool {
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return true
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	bodyStr := string(body)

	if strings.Contains(bodyStr, "40001") ||
		strings.Contains(bodyStr, "40002") ||
		strings.Contains(bodyStr, "40003") {
		return true
	}

	keywords := []string{"expired", "unauthorized", "not login", "invalid jwt"}
	for _, kw := range keywords {
		if strings.Contains(strings.ToLower(bodyStr), kw) {
			return true
		}
	}

	return false
}


