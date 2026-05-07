package qwen

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
)

const (
	completionURL = "/api/v2/chat/completions"
	modelsURL     = "/api/models"
	authURL       = "/api/v1/auths/"
	createChatURL = "/api/v2/chats/new"
	signinURL     = "/api/v2/auths/signin"
)

// Client handles Qwen webchat interactions with API-based authentication
type Client struct {
	config     config.QwenConfig
	httpClient *http.Client
	baseURL    *url.URL
	authToken  string
	loggedIn   bool
	lastLogin  time.Time
}

// New creates a new Qwen client with API-based authentication
func New(cfg config.QwenConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	// Create cookie jar for session management
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	httpClient := &http.Client{
		Jar:     jar,
		Timeout: time.Duration(cfg.Timeout) * time.Second,
	}

	client := &Client{
		config:     cfg,
		baseURL:    baseURL,
		httpClient: httpClient,
	}

	if err := client.login(); err != nil {
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	return client, nil
}

// Name returns the provider name
func (c *Client) Name() string {
	return "qwen"
}

// Close cleans up the provider (no-op for API-based auth)
func (c *Client) Close() error {
	return nil
}

// doRequest makes an HTTP request with Qwen-style browser headers
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	reqURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	// Qwen browser-like headers (from qwen2api qwen_client.py)
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", c.config.BaseURL+"/")
	req.Header.Set("Origin", c.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	return c.httpClient.Do(req)
}

// doRequestWithRetry wraps doRequest with auth-failure detection and retry
func (c *Client) doRequestWithRetry(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	resp, err := c.doRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}

	if isAuthFailure(resp) {
		slog.Warn("Auth failure detected, attempting re-login")
		resp.Body.Close()

		if reLoginErr := c.login(); reLoginErr != nil {
			return nil, reLoginErr
		}

		return c.doRequest(ctx, method, path, body)
	}

	return resp, nil
}

// isAuthFailure detects Qwen auth failures
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

	keywords := []string{"expired", "unauthorized", "not login", "invalid jwt", "token"}
	for _, kw := range keywords {
		if strings.Contains(strings.ToLower(bodyStr), kw) {
			return true
		}
	}

	return false
}

// ensureAuthenticated checks and refreshes auth if needed
func (c *Client) ensureAuthenticated() error {
	refreshInterval := time.Duration(c.config.TokenRefreshInterval) * time.Second
	if !c.loggedIn || time.Since(c.lastLogin) > refreshInterval {
		if err := c.login(); err != nil {
			return err
		}
	}
	return nil
}
