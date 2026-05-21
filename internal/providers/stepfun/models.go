package stepfun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	providers "github.com/user/wc2api/internal/providers"
)

// StepFun model names (fallback if API is unreachable).
// IDs are prefixed with "stepfun-" to enable router prefix matching.
var fallbackModels = []providers.Model{
	{ID: "stepfun-step3.5-flash", Object: "model", Created: time.Now().Unix(), OwnedBy: "stepfun"},
	{ID: "stepfun-step3", Object: "model", Created: time.Now().Unix(), OwnedBy: "stepfun"},
	{ID: "stepfun-deepseek-r1", Object: "model", Created: time.Now().Unix(), OwnedBy: "stepfun"},
	{ID: "stepfun-step-r1-v-mini", Object: "model", Created: time.Now().Unix(), OwnedBy: "stepfun"},
	{ID: "stepfun-step2", Object: "model", Created: time.Now().Unix(), OwnedBy: "stepfun"},
}

// nonLLMModels are entries from GetChatConfig that are not actual text chat models.
var nonLLMModels = map[string]bool{
	"studio-step2-creation":  true,
	"studio-dialogue-reason": true,
	"Diligence Check":        true,
	"image-reason":           true,
	"step-video":             true,
}

// ListModels returns available models from StepFun with freecache.
// Tries dynamic fetch from GetChatConfig endpoint first, falls back to hardcoded list.
func (c *Client) ListModels() []providers.Model {
	if data, err := c.modelCache.Get([]byte("models")); err == nil {
		var models []providers.Model
		if err := json.Unmarshal(data, &models); err == nil {
			return models
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, body, models, err := c.fetchModelsFromAPI(ctx)
	if err != nil || len(models) == 0 {
		var reason string
		if err != nil {
			reason = fmt.Sprintf("API error: %v", err)
		} else {
			reason = "no valid models returned"
		}
		slog.Warn("StepFun: failed to fetch models, using fallback",
			"reason", reason,
			"status", status,
			"response_preview", truncate(string(body), 500))
		models = fallbackModels
	}

	if data, err := json.Marshal(models); err == nil {
		c.modelCache.Set([]byte("models"), data, 3600)
	}
	return models
}

// fetchModelsFromAPI dynamically fetches models from StepFun's GetChatConfig endpoint.
func (c *Client) fetchModelsFromAPI(ctx context.Context) (int, []byte, []providers.Model, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL.String()+getChatConfigEndpoint, strings.NewReader("{}"))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header = c.headers()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	var body []byte

	if status != 200 {
		body, _ = io.ReadAll(resp.Body)
		return status, body, nil, nil
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return status, body, nil, err
	}

	var result struct {
		ChatConfig *struct {
			Models []struct {
				Model        string `json:"model"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
		} `json:"chatConfig"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return status, body, nil, err
	}

	if result.ChatConfig == nil {
		return status, body, nil, nil
	}

	now := time.Now().Unix()
	models := make([]providers.Model, 0, len(result.ChatConfig.Models))
	for _, m := range result.ChatConfig.Models {
		if m.Model == "" {
			continue
		}
		// Filter out non-LLM models
		if nonLLMModels[m.Model] {
			continue
		}
		models = append(models, providers.Model{
			ID:      "stepfun-" + m.Model, // Add stepfun- prefix for router matching
			Object:  "model",
			Created: now,
			OwnedBy: "stepfun",
		})
	}

	return status, body, models, nil
}

// truncate returns a truncated string with ellipsis if too long.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
