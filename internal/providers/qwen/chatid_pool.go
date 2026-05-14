package qwen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"

	"github.com/user/wc2api/internal/config"
)

// ChatID represents a pre-warmed chat session
type ChatID struct {
	ID        string
	Email     string
	Model     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ChatIDPool manages a pool of pre-warmed chat sessions to reduce latency
type ChatIDPool struct {
	config     config.QwenConfig
	httpClient *http.Client
	baseURL    *url.URL
	pool       map[string][]ChatID // email -> []ChatID
	mu         sync.RWMutex
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// NewChatIDPool creates a new chat ID pool with pre-warming capabilities
func NewChatIDPool(cfg config.QwenConfig) (*ChatIDPool, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	// Create HTTP client with cookie jar for session management
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	httpClient := &http.Client{
		Jar:     jar,
		Timeout: time.Duration(cfg.Timeout) * time.Second,
	}

	pool := &ChatIDPool{
		config:     cfg,
		httpClient: httpClient,
		baseURL:    baseURL,
		pool:       make(map[string][]ChatID),
		stopCh:     make(chan struct{}),
	}

	// Start background pre-warmer if enabled
	if cfg.ChatIDPreWarmSize > 0 {
		pool.wg.Add(1)
		go pool.preWarmer()
	}

	return pool, nil
}

// Close shuts down the chat ID pool and stops the background pre-warmer
func (p *ChatIDPool) Close() error {
	close(p.stopCh)
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pool = make(map[string][]ChatID)
	return nil
}

// GetChatID retrieves a pre-warmed chat ID for the given email and model
func (p *ChatIDPool) GetChatID(ctx context.Context, email, model string) (string, error) {
	p.mu.RLock()
	chatIDs, exists := p.pool[email]
	p.mu.RUnlock()

	if !exists || len(chatIDs) == 0 {
		return "", fmt.Errorf("no pre-warmed chat ID available for %s", email)
	}

	// Find the first valid (non-expired) chat ID
	for i, chatID := range chatIDs {
		if time.Now().Before(chatID.ExpiresAt) {
			// Remove used chat ID from pool
			p.mu.Lock()
			p.pool[email] = append(p.pool[email][:i], p.pool[email][i+1:]...)
			p.mu.Unlock()
			return chatID.ID, nil
		}
	}

	return "", fmt.Errorf("no valid pre-warmed chat ID available for %s", email)
}

// ReturnChatID returns a chat ID to the pool for reuse
func (p *ChatIDPool) ReturnChatID(email, model, chatID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pool[email] = append(p.pool[email], ChatID{
		ID:        chatID,
		Email:     email,
		Model:     model,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Duration(p.config.ChatIDTTL) * time.Second),
	})
}

// preWarmer runs in the background to maintain a pool of pre-warmed chat IDs
func (p *ChatIDPool) preWarmer() {
	defer p.wg.Done()
	ticker := time.NewTicker(time.Duration(p.config.ChatIDPreWarmInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.warmUpChatIDs()
		}
	}
}

// warmUpChatIDs creates new chat sessions to maintain the pool size
// NOTE: Pre-warming is currently disabled as the config structure only supports a single account
func (p *ChatIDPool) warmUpChatIDs() {
	// Pre-warming not implemented for single-account configuration
	// To enable: refactor config to support multiple accounts like the original design
}

// warmUpSingleChatID creates a single chat session and adds it to the pool
// NOTE: Not used when pre-warming is disabled
func (p *ChatIDPool) warmUpSingleChatID(ctx context.Context, email, model string) error {
	// Pre-warming not implemented for single-account configuration
	return fmt.Errorf("pre-warming not enabled")
}

// createChatSession creates a new chat session via the Qwen API
func (p *ChatIDPool) createChatSession(token, model string) (string, error) {
	ts := time.Now().Unix()
	body := map[string]interface{}{
		"title":    fmt.Sprintf("prewarm_%d", ts),
		"models":   []string{model},
		"chat_mode": "normal",
		"chat_type": "t2t",
		"timestamp": ts,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	reqURL := p.baseURL.ResolveReference(&url.URL{Path: createChatURL})
	req, err := http.NewRequest("POST", reqURL.String(), bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", p.config.BaseURL+"/")
	req.Header.Set("Origin", p.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create chat failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Success bool   `json:"success"`
		Data    struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success || result.Data.ID == "" {
		return "", fmt.Errorf("Qwen API returned error or missing id")
	}

	return result.Data.ID, nil
}

// getPoolSize returns the current size of the pool for a given email
func (p *ChatIDPool) getPoolSize(email string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pool[email])
}