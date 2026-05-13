package duckai

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/user/wc2api/internal/providers"
)

const modelsURL = "/duckchat/v1/models"

var hardcodedModels = []string{
	"gpt-4o-mini",
	"gpt-5-mini",
	"claude-haiku-4-5",
	"meta-llama/Llama-4-Scout-17B-16E-Instruct",
	"mistral-small-2603",
	"tinfoil/gpt-oss-120b",
}

type modelsResponse struct {
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	EntityHasAccess bool   `json:"entityHasAccess"`
}

func (c *Client) ListModels() []providers.Model {
	return c.fetchModels()
}

var (
	modelsCache []providers.Model
	modelsMu    sync.Mutex
)

func (c *Client) fetchModels() []providers.Model {
	modelsMu.Lock()
	defer modelsMu.Unlock()

	if modelsCache != nil {
		return modelsCache
	}

	modelsCache = c.fetchModelsFromAPI()
	return modelsCache
}

func (c *Client) fetchModelsFromAPI() []providers.Model {
	req, err := http.NewRequest(http.MethodGet, c.baseURL.String()+modelsURL, nil)
	if err != nil {
		slog.Warn("failed to create models request, using fallback", "error", err)
		return buildFallbackModels()
	}

	setCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Warn("failed to fetch models, using fallback", "error", err)
		return buildFallbackModels()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("models endpoint returned non-200, using fallback", "status", resp.StatusCode, "body", string(body))
		return buildFallbackModels()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("failed to read models response, using fallback", "error", err)
		return buildFallbackModels()
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		slog.Warn("failed to parse models response, using fallback", "error", err)
		return buildFallbackModels()
	}

	created := time.Now().Unix()
	var models []providers.Model
	for _, m := range parsed.Models {
		if !m.EntityHasAccess {
			continue
		}
		models = append(models, providers.Model{
			ID:      m.ID,
			Object:  "model",
			Created: created,
			OwnedBy: "duckai",
		})
	}

	if len(models) == 0 {
		slog.Warn("no accessible models from API, using fallback")
		return buildFallbackModels()
	}

	slog.Info("fetched models from DuckDuckGo API", "count", len(models))
	return models
}

func buildFallbackModels() []providers.Model {
	created := time.Now().Unix()
	models := make([]providers.Model, len(hardcodedModels))
	for i, id := range hardcodedModels {
		models[i] = providers.Model{
			ID:      id,
			Object:  "model",
			Created: created,
			OwnedBy: "duckai",
		}
	}
	return models
}
