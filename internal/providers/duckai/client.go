package duckai

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/user/wc2api/internal/config"
)

const (
	statusURL = "/duckchat/v1/status"
	chatURL   = "/duckchat/v1/chat"
)

type Client struct {
	config     config.DuckAIConfig
	httpClient *http.Client
	baseURL    *url.URL

	rateLimiter *RateLimiter
}

func New(cfg config.DuckAIConfig) (*Client, error) {
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	httpClient := &http.Client{
		Timeout: time.Duration(cfg.Timeout) * time.Second,
		Jar:     jar,
	}

	client := &Client{
		config:      cfg,
		baseURL:     baseURL,
		httpClient:  httpClient,
		rateLimiter: NewRateLimiter(20, time.Minute, time.Second),
	}

	slog.Info("DuckAI provider initialized", "base_url", cfg.BaseURL)
	return client, nil
}

func (c *Client) Name() string {
	return "duckai"
}

func (c *Client) Close() error {
	return nil
}
