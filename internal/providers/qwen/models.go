package qwen

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

const modelCacheTTL = 3600 // 1 hour in seconds

// Qwen model names (fallback if API is unreachable)
var fallbackModels = []providers.Model{
	{ID: "qwen3.5-flash", Object: "model", Created: 1704067200, OwnedBy: "qwen"},
	{ID: "qwen3.6-plus", Object: "model", Created: 1704067200, OwnedBy: "qwen"},
}

// ListModels returns available models from Qwen with 1-hour freecache.
// Tries dynamic fetch from API first, falls back to hardcoded list.
func (c *Client) ListModels() []providers.Model {
	if data, err := c.modelCache.Get([]byte("models")); err == nil {
		var models []providers.Model
		if err := json.Unmarshal(data, &models); err == nil {
			return models
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := c.fetchModelsFromAPI(ctx)
	if err != nil || len(models) == 0 {
		slog.Warn("Qwen: failed to fetch models, using fallback", "error", err)
		models = fallbackModels
	}

	if data, err := json.Marshal(models); err == nil {
		c.modelCache.Set([]byte("models"), data, modelCacheTTL)
	}
	return models
}

// fetchModelsFromAPI dynamically fetches models from Qwen API
func (c *Client) fetchModelsFromAPI(ctx context.Context) ([]providers.Model, error) {
	if c.authToken == "" {
		return nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", c.config.BaseURL+modelsURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", c.config.BaseURL+"/")
	req.Header.Set("Origin", c.config.BaseURL)
	req.Header.Set("Connection", "keep-alive")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, nil
	}

	var result struct {
		Data []providers.Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Data, nil
}
