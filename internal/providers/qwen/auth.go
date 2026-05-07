package qwen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// login performs API-based login to Qwen via POST /api/v2/auths/signin
func (c *Client) login() error {
	slog.Info("Attempting Qwen API login", "email", c.config.Email)

	// Hash password with SHA256 (Qwen expects hashed password)
	hashedPassword := hashPassword(c.config.Password)

	// Build request payload
	payload := map[string]string{
		"email":    c.config.Email,
		"password": hashedPassword,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.config.Timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+signinURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create signin request: %w", err)
	}

	// Set headers matching browser request (from curl example, minimal required)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", c.config.BaseURL+"/auth")
	req.Header.Set("Origin", c.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	// Encode JSON body
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(jsonBody))
	req.ContentLength = int64(len(jsonBody))

	// Execute request (cookiejar will auto-store Set-Cookie)
	slog.Debug("Sending signin request", "url", c.config.BaseURL+signinURL)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("signin request failed: %w", err)
	}
	defer resp.Body.Close()

	slog.Debug("Signin response", "status", resp.StatusCode)

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("signin failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Extract token from Set-Cookie header using http.Cookies
	cookies := resp.Cookies()
	var tokenCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "token" {
			tokenCookie = c
			break
		}
	}
	if tokenCookie == nil {
		return fmt.Errorf("token cookie not found in response")
	}
	token := tokenCookie.Value
	if token == "" {
		return fmt.Errorf("token cookie value is empty")
	}

	if token == "" {
		return fmt.Errorf("extracted token is empty")
	}

	c.authToken = token
	c.loggedIn = true
	c.lastLogin = time.Now()
	slog.Info("Qwen API login successful")
	previewLen := 20
	if len(token) < previewLen {
		previewLen = len(token)
	}
	slog.Debug("Token acquired", "token_preview", token[:previewLen]+"...")

	return nil
}

// verifyToken checks if the current token is valid
func (c *Client) verifyToken() bool {
	if c.authToken == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.config.BaseURL+authURL, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", c.config.BaseURL+"/")
	req.Header.Set("Origin", c.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	// Check if response contains "role": "user" to confirm valid token
	// or check for WAF interception (treat as valid for now)
	return true
}

// refreshToken re-login to get a fresh token
func (c *Client) refreshToken() error {
	slog.Info("Refreshing Qwen token...")

	// Clear current token
	c.authToken = ""
	c.loggedIn = false

	// Re-login via API
	if err := c.login(); err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	return nil
}

// hashPassword computes SHA256 hash of password and returns hex string
func hashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%x", sum)
}
